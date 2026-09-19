package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/cuipengdba/agentsql/internal/audit"
	"github.com/cuipengdba/agentsql/internal/discovery"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/stretchr/testify/require"
)

const discoverySentinel = "T38_RAW_SAMPLE_SENTINEL_7b19"

type fakeDiscoveryRunner struct {
	mu        sync.Mutex
	request   discovery.ScanRequest
	result    discovery.ScanResult
	err       error
	rawSample string
}

func (runner *fakeDiscoveryRunner) Discover(_ context.Context, _, _ string, request discovery.ScanRequest) (discovery.ScanResult, error) {
	runner.mu.Lock()
	runner.request = request
	_ = runner.rawSample // deliberately consumed only in-process
	runner.mu.Unlock()
	return runner.result, runner.err
}

func TestDiscoveryRouteBearerDTOAuditAndSentinel(t *testing.T) {
	runner := &fakeDiscoveryRunner{rawSample: discoverySentinel, result: discovery.ScanResult{
		Scope:    discovery.Scope{DatasourceID: "ds-1", Tables: []discovery.TableRef{{Schema: "public", Table: "customers"}}, Sampling: true, SampleRows: 10},
		Stats:    discovery.Stats{TablesRequested: 1, TablesScanned: 1, ColumnsSeen: 2, CandidateColumns: 1, SampledColumns: 1, SampledValuesCount: 10, FindingsCount: 1},
		Findings: []discovery.Finding{{Schema: "public", Table: "customers", Column: "phone", DataType: "text", Category: discovery.CategoryPhone, Signals: []discovery.Signal{{Name: "column_name", Count: 1}}, Confidence: discovery.ConfidenceHigh, Sampled: true, MatchedSamples: 10, EligibleSamples: 10, Applicable: true}},
	}}
	fixture := newAdminFixtureWithDiscovery(t, runner)
	body := `{"tables":[{"schema":"public","table":"customers"}],"categories":["phone"]}`
	status, _ := fixture.request(http.MethodPost, "/api/v1/datasources/ds-1/discover", "", body)
	require.Equal(t, http.StatusUnauthorized, status)
	status, response := fixture.request(http.MethodPost, "/api/v1/datasources/ds-1/discover", fixture.adminToken, body)
	require.Equal(t, http.StatusOK, status, response)
	require.NotContains(t, response, discoverySentinel)
	require.NotContains(t, response, "preview")

	page, err := fixture.store.AuditLogs().Page(context.Background(), 1, 10)
	require.NoError(t, err)
	require.NotEmpty(t, page.List)
	log := page.List[0]
	require.Equal(t, audit.ActionDiscover, *log.Action)
	require.Equal(t, "admin", *log.ActorType)
	require.Equal(t, "admin", *log.ActorID)
	require.NotContains(t, *log.DetailsJSON, discoverySentinel)
	streamJSON, err := json.Marshal(auditToStreamView(log))
	require.NoError(t, err)
	require.NotContains(t, string(streamJSON), discoverySentinel)
	require.NotContains(t, fixture.logs.String(), discoverySentinel)
	var details map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(*log.DetailsJSON), &details))
	require.Equal(t, []string{"candidate_columns", "canonical_tables", "columns_seen", "duration_ms", "findings_count", "sample_rows", "sampled_columns", "sampled_values_count", "sampling", "tables_requested", "tables_scanned"}, sortedJSONKeys(details))

	status, response = fixture.request(http.MethodPost, "/api/v1/datasources/ds-1/discover", fixture.adminToken, `{"tables":[{"schema":"public","table":"customers"}],"sampling":false,"sample_rows":10}`)
	require.Equal(t, http.StatusUnprocessableEntity, status, response)
	status, response = fixture.request(http.MethodPost, "/api/v1/datasources/ds-1/discover", fixture.adminToken, `{"tables":[{"schema":"public","table":"customers"}],"sample_rows":0}`)
	require.Equal(t, http.StatusUnprocessableEntity, status, response)
	status, response = fixture.request(http.MethodPost, "/api/v1/datasources/ds-1/discover", fixture.adminToken, `{"tables":[{"schema":"public","table":"customers"}],"unknown":true}`)
	require.Equal(t, http.StatusBadRequest, status, response)
	status, response = fixture.request(http.MethodPost, "/api/v1/datasources/ds-1/discover", fixture.adminToken, `{"tables":[]}`+strings.Repeat(" ", (1<<20)+1))
	require.Equal(t, http.StatusRequestEntityTooLarge, status, response)
}

func TestDiscoveryPermissionErrorIsSafeAndAudited(t *testing.T) {
	runner := &fakeDiscoveryRunner{err: discovery.NewPortError(discovery.CodePermissionDenied)}
	fixture := newAdminFixtureWithDiscovery(t, runner)
	status, body := fixture.request(http.MethodPost, "/api/v1/datasources/ds-1/discover", fixture.adminToken, `{"tables":[{"schema":"public","table":"customers"}]}`)
	require.Equal(t, http.StatusForbidden, status, body)
	require.Contains(t, body, "DISCOVERY_PERMISSION_DENIED")
	require.Contains(t, body, "数据源账号无权读取请求范围")
	require.NotContains(t, body, discoverySentinel)
	page, err := fixture.store.AuditLogs().Page(context.Background(), 1, 1)
	require.NoError(t, err)
	require.Contains(t, *page.List[0].DetailsJSON, "DISCOVERY_PERMISSION_DENIED")
}

func TestDiscoveryStableErrorMappingsAndAuditBarrier(t *testing.T) {
	for _, test := range []struct {
		name   string
		code   discovery.ErrorCode
		status int
	}{
		{name: "scope invisible", code: discovery.CodeScopeNotVisible, status: http.StatusForbidden},
		{name: "rate limited", code: discovery.CodeRateLimited, status: http.StatusTooManyRequests},
		{name: "timeout", code: discovery.CodeTimeout, status: http.StatusGatewayTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAdminFixtureWithDiscovery(t, &fakeDiscoveryRunner{err: discovery.NewPortError(test.code)})
			status, body := fixture.request(http.MethodPost, "/api/v1/datasources/ds-1/discover", fixture.adminToken, `{"tables":[{"schema":"public","table":"customers"}]}`)
			require.Equal(t, test.status, status, body)
			require.Contains(t, body, string(test.code))
		})
	}
	t.Run("audit unavailable discards successful result", func(t *testing.T) {
		fixture := newAdminFixtureWithDiscovery(t, &fakeDiscoveryRunner{result: discovery.ScanResult{Findings: []discovery.Finding{}}})
		fixture.runtime.ManagementAudit = nil
		status, body := fixture.request(http.MethodPost, "/api/v1/datasources/ds-1/discover", fixture.adminToken, `{"tables":[{"schema":"public","table":"customers"}]}`)
		require.Equal(t, http.StatusServiceUnavailable, status, body)
		require.Contains(t, body, "AUDIT_UNAVAILABLE")
	})
	t.Run("missing datasource", func(t *testing.T) {
		fixture := newAdminFixtureWithDiscovery(t, &fakeDiscoveryRunner{})
		status, _ := fixture.request(http.MethodPost, "/api/v1/datasources/missing/discover", fixture.adminToken, `{"tables":[{"schema":"public","table":"customers"}]}`)
		require.Equal(t, http.StatusNotFound, status)
	})
}

func TestDiscoveryApplyDisabledIdempotentConflictAndStrictDTO(t *testing.T) {
	fixture := newAdminFixture(t)
	requestBody := `{"items":[{"schema":"public","table":"customers","column":" Phone ","category":"phone","sensitive_type":"phone","algo":"mask"}]}`
	status, body := fixture.request(http.MethodPost, "/api/v1/datasources/ds-1/discover/apply", fixture.adminToken, requestBody)
	require.Equal(t, http.StatusUnprocessableEntity, status, body)
	requestBody = `{"items":[{"schema":"public","table":"customers","column":"PHONE","category":"phone","sensitive_type":"phone","algo":"mask"}]}`
	status, body = fixture.request(http.MethodPost, "/api/v1/datasources/ds-1/discover/apply", fixture.adminToken, requestBody)
	require.Equal(t, http.StatusOK, status, body)
	require.Contains(t, body, `"created":1`)
	rules, err := fixture.store.MaskRules().ListByDatasource(context.Background(), "ds-1")
	require.NoError(t, err)
	require.Len(t, rules, 1)
	require.False(t, rules[0].Enabled)
	require.Empty(t, rules[0].SchemaName)
	require.Equal(t, "customers", rules[0].TableName)
	require.Equal(t, "phone", rules[0].ColumnName)
	enabled, err := fixture.store.MaskRules().ListEnabledByDatasource(context.Background(), "ds-1")
	require.NoError(t, err)
	require.Empty(t, enabled, "disabled discovery draft must not enter runtime redaction")

	status, body = fixture.request(http.MethodPost, "/api/v1/datasources/ds-1/discover/apply", fixture.adminToken, requestBody)
	require.Equal(t, http.StatusOK, status, body)
	require.Contains(t, body, `"created":0`)
	require.Contains(t, body, `"existing":1`)

	status, body = fixture.request(http.MethodPost, "/api/v1/datasources/ds-1/discover/apply", fixture.adminToken, strings.Replace(requestBody, `"algo":"mask"`, `"algo":"mask","enabled":true`, 1))
	require.Equal(t, http.StatusBadRequest, status, body)
	status, body = fixture.request(http.MethodPost, "/api/v1/datasources/ds-1/discover/apply", fixture.adminToken, strings.Replace(requestBody, `"phone","sensitive_type":"phone"`, `"future","sensitive_type":"future"`, 1))
	require.Equal(t, http.StatusUnprocessableEntity, status, body)

	current := rules[0]
	current.SensitiveType = "email"
	_, err = fixture.store.MaskRules().Update(context.Background(), current)
	require.NoError(t, err)
	status, body = fixture.request(http.MethodPost, "/api/v1/datasources/ds-1/discover/apply", fixture.adminToken, requestBody)
	require.Equal(t, http.StatusConflict, status, body)
	require.Contains(t, body, `"conflicts":1`)
}

func TestDiscoveryApplyAcceptsAllSixRunnableCategories(t *testing.T) {
	fixture := newAdminFixture(t)
	body := `{"items":[` +
		`{"schema":"public","table":"customers","column":"phone","category":"phone","sensitive_type":"phone","algo":"mask"},` +
		`{"schema":"public","table":"customers","column":"email","category":"email","sensitive_type":"email","algo":"mask"},` +
		`{"schema":"public","table":"customers","column":"idcard","category":"idcard","sensitive_type":"idcard","algo":"mask"},` +
		`{"schema":"public","table":"customers","column":"bankcard","category":"bankcard","sensitive_type":"bankcard","algo":"mask"},` +
		`{"schema":"public","table":"customers","column":"ip","category":"ip","sensitive_type":"ip","algo":"mask"},` +
		`{"schema":"public","table":"customers","column":"birthdate","category":"birthdate","sensitive_type":"birthdate","algo":"mask"}` +
		`]}`
	status, response := fixture.request(http.MethodPost, "/api/v1/datasources/ds-1/discover/apply", fixture.adminToken, body)
	require.Equal(t, http.StatusOK, status, response)
	require.Contains(t, response, `"created":6`)
	rules, err := fixture.store.MaskRules().ListByDatasource(context.Background(), "ds-1")
	require.NoError(t, err)
	require.Len(t, rules, 6)
	for _, rule := range rules {
		require.False(t, rule.Enabled)
		require.Empty(t, rule.SchemaName)
		require.Equal(t, "customers", rule.TableName)
	}

	status, response = fixture.request(http.MethodPost, "/api/v1/datasources/ds-1/discover/apply", fixture.adminToken,
		`{"items":[{"schema":"public","table":"customers","column":"bad","category":"phone","sensitive_type":"email","algo":"mask"}]}`)
	require.Equal(t, http.StatusUnprocessableEntity, status, response)
	require.Contains(t, response, "DISCOVERY_NOT_APPLICABLE")

	status, response = fixture.request(http.MethodPost, "/api/v1/datasources/ds-1/discover/apply", fixture.adminToken,
		`{"items":[{"schema":"public","table":"customers","column":"name","category":"phone","sensitive_type":"phone","algo":"hash"}]}`)
	require.Equal(t, http.StatusUnprocessableEntity, status, response)
	require.Contains(t, response, "DISCOVERY_NOT_APPLICABLE")
}

func TestDiscoveryApplyRejectsRangeNumberAndDate(t *testing.T) {
	tests := []struct {
		name string
		item string
	}{
		{name: "range algorithm", item: `{"schema":"public","table":"customers","column":"phone","category":"phone","sensitive_type":"phone","algo":"range"}`},
		{name: "number sensitive type", item: `{"schema":"public","table":"customers","column":"amount","category":"phone","sensitive_type":"number","algo":"mask"}`},
		{name: "date sensitive type", item: `{"schema":"public","table":"customers","column":"created_at","category":"birthdate","sensitive_type":"date","algo":"mask"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAdminFixture(t)
			status, body := fixture.request(http.MethodPost, "/api/v1/datasources/ds-1/discover/apply", fixture.adminToken,
				`{"items":[`+test.item+`]}`)
			require.Equal(t, http.StatusUnprocessableEntity, status, body)
			require.Contains(t, body, "DISCOVERY_NOT_APPLICABLE")
			rules, err := fixture.store.MaskRules().ListByDatasource(context.Background(), "ds-1")
			require.NoError(t, err)
			require.Empty(t, rules)
		})
	}
}

func TestDiscoveryApplyTypedStoreErrorsMapTo422(t *testing.T) {
	recorder := httptest.NewRecorder()
	handler := &Handler{}
	handler.writeDiscoveryApplyError(recorder, errors.Join(store.ErrInvalidDiscoveryDraft, errors.New("internal draft detail")))
	require.Equal(t, http.StatusUnprocessableEntity, recorder.Code)
	require.Contains(t, recorder.Body.String(), "DISCOVERY_NOT_APPLICABLE")
	require.NotContains(t, recorder.Body.String(), "internal draft detail")
}

func TestDiscoveryApplyGlobalCoverageAndAmbiguousAggregation(t *testing.T) {
	fixture := newAdminFixture(t)
	_, err := fixture.store.MaskRules().Create(context.Background(), model.MaskRule{ID: "global-email", ColumnName: "email", SensitiveType: "email", Algo: "mask", Enabled: true})
	require.NoError(t, err)
	status, body := fixture.request(http.MethodPost, "/api/v1/datasources/ds-1/discover/apply", fixture.adminToken, `{"items":[{"schema":"public","table":"customers","column":"email","category":"email","sensitive_type":"email","algo":"mask"}]}`)
	require.Equal(t, http.StatusOK, status, body)
	require.Contains(t, body, `"covered_by_global":1`)

	status, body = fixture.request(http.MethodPost, "/api/v1/datasources/ds-1/discover/apply", fixture.adminToken, `{"items":[{"schema":"public","table":"customers","column":"contact","category":"phone","sensitive_type":"phone","algo":"mask"},{"schema":"archive","table":"customers","column":"CONTACT","category":"email","sensitive_type":"email","algo":"mask"}]}`)
	require.Equal(t, http.StatusOK, status, body)
	require.Contains(t, body, `"ambiguous":1`)
	rules, err := fixture.store.MaskRules().ListByDatasource(context.Background(), "ds-1")
	require.NoError(t, err)
	for _, rule := range rules {
		require.NotEqual(t, "contact", rule.ColumnName)
	}

	page, err := fixture.store.AuditLogs().Page(context.Background(), 1, 10)
	require.NoError(t, err)
	require.Equal(t, audit.ActionDiscoverApply, *page.List[0].Action)
	var details map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(*page.List[0].DetailsJSON), &details))
	require.Equal(t, []string{"ambiguous", "column_keys", "conflict", "covered_by_global", "created", "existing", "requested"}, sortedJSONKeys(details))
}

func TestDiscoveryApplyUsesTableOnlyPhysicalKeys(t *testing.T) {
	fixture := newAdminFixture(t)
	body := `{"items":[` +
		`{"schema":"public","table":"customers","column":"phone","category":"phone","sensitive_type":"phone","algo":"mask"},` +
		`{"schema":"archive","table":"suppliers","column":"PHONE","category":"phone","sensitive_type":"phone","algo":"mask"}` +
		`]}`
	status, response := fixture.request(http.MethodPost, "/api/v1/datasources/ds-1/discover/apply", fixture.adminToken, body)
	require.Equal(t, http.StatusOK, status, response)
	require.Contains(t, response, `"created":2`)

	rules, err := fixture.store.MaskRules().ListByDatasource(context.Background(), "ds-1")
	require.NoError(t, err)
	require.Len(t, rules, 2)
	require.Equal(t, []string{"customers", "suppliers"}, []string{rules[0].TableName, rules[1].TableName})
	for _, rule := range rules {
		require.Empty(t, rule.SchemaName, "PostgreSQL/MySQL discovery drafts must be table-only")
		require.Equal(t, "phone", rule.ColumnName)
		require.Equal(t, "mask", rule.Algo)
		require.False(t, rule.Enabled)
	}
}

func TestDiscoveryApplyEnabledGlobalCoverageAndDisabledGlobalDoesNotBlock(t *testing.T) {
	fixture := newAdminFixture(t)
	datasourceID := "ds-1"
	_, err := fixture.store.MaskRules().Create(context.Background(), model.MaskRule{
		ID: "enabled-global-phone", DatasourceID: &datasourceID, ColumnName: "phone",
		SensitiveType: "phone", Algo: "mask", Enabled: true,
	})
	require.NoError(t, err)
	_, err = fixture.store.MaskRules().Create(context.Background(), model.MaskRule{
		ID: "disabled-global-email", DatasourceID: &datasourceID, ColumnName: "email",
		SensitiveType: "email", Algo: "mask", Enabled: false,
	})
	require.NoError(t, err)

	body := `{"items":[` +
		`{"schema":"public","table":"customers","column":"phone","category":"phone","sensitive_type":"phone","algo":"mask"},` +
		`{"schema":"public","table":"suppliers","column":"phone","category":"phone","sensitive_type":"phone","algo":"mask"},` +
		`{"schema":"public","table":"customers","column":"email","category":"email","sensitive_type":"email","algo":"mask"}` +
		`]}`
	status, response := fixture.request(http.MethodPost, "/api/v1/datasources/ds-1/discover/apply", fixture.adminToken, body)
	require.Equal(t, http.StatusOK, status, response)
	require.Contains(t, response, `"covered_by_global":2`)
	require.Contains(t, response, `"created":1`)

	rules, err := fixture.store.MaskRules().ListByDatasource(context.Background(), "ds-1")
	require.NoError(t, err)
	var tableEmail *model.MaskRule
	for index := range rules {
		if rules[index].TableName == "customers" && rules[index].ColumnName == "email" {
			tableEmail = &rules[index]
		}
	}
	require.NotNil(t, tableEmail, "a disabled global draft must not cover a new table-only draft")
	require.False(t, tableEmail.Enabled)

	page, err := fixture.store.AuditLogs().Page(context.Background(), 1, 1)
	require.NoError(t, err)
	require.Contains(t, *page.List[0].DetailsJSON, `"covered_by_global":2`)
}

func TestDiscoveryApplyConcurrentSameKeyCreatesExactlyOne(t *testing.T) {
	fixture := newAdminFixture(t)
	body := `{"items":[{"schema":"public","table":"customers","column":"email","category":"email","sensitive_type":"email","algo":"mask"}]}`
	const workers = 12
	statuses := make(chan int, workers)
	responses := make(chan string, workers)
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			status, response := fixture.request(http.MethodPost, "/api/v1/datasources/ds-1/discover/apply", fixture.adminToken, body)
			statuses <- status
			responses <- response
		}()
	}
	wait.Wait()
	close(statuses)
	close(responses)
	for status := range statuses {
		require.Equal(t, http.StatusOK, status)
	}
	created := 0
	for response := range responses {
		if strings.Contains(response, `"created":1`) {
			created++
		}
		require.NotContains(t, response, discoverySentinel)
	}
	require.Equal(t, 1, created)
	rules, err := fixture.store.MaskRules().ListByDatasource(context.Background(), "ds-1")
	require.NoError(t, err)
	require.Len(t, rules, 1)
}

func sortedJSONKeys(values map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
