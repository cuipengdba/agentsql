package adminapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMaskRuleScopeDTOValidationAndFullPut(t *testing.T) {
	fixture := newAdminFixture(t)
	status, body := fixture.request(http.MethodPost, "/api/v1/mask_rules", fixture.adminToken,
		`{"id":"quoted-scope","datasource_id":"ds-1","schema_name":"租户\"甲","table_name":"Customer\"档案","column_name":"phone","sensitive_type":"phone","algo":"mask"}`)
	require.Equal(t, http.StatusOK, status, body)
	created := decodeMaskRuleResponse(t, body)
	require.Equal(t, `租户"甲`, created.SchemaName)
	require.Equal(t, `Customer"档案`, created.TableName)

	status, body = fixture.request(http.MethodGet, "/api/v1/mask_rules?datasource_id=ds-1&page_size=100", fixture.adminToken, "")
	require.Equal(t, http.StatusOK, status, body)
	require.Equal(t, `租户"甲`, decodeMaskRuleListResponse(t, body)[0].SchemaName)

	status, body = fixture.request(http.MethodPut, "/api/v1/mask_rules/quoted-scope", fixture.adminToken,
		`{"datasource_id":"ds-1","schema_name":"","table_name":"","column_name":"phone","sensitive_type":"phone","algo":"mask"}`)
	require.Equal(t, http.StatusOK, status, body)
	stored, err := fixture.store.MaskRules().Get(context.Background(), "quoted-scope")
	require.NoError(t, err)
	require.Empty(t, stored.SchemaName)
	require.Empty(t, stored.TableName)

	status, body = fixture.request(http.MethodPut, "/api/v1/mask_rules/quoted-scope", fixture.adminToken,
		`{"datasource_id":"ds-1","schema_name":"MixedCase","table_name":"客户表","column_name":"phone","sensitive_type":"phone","algo":"mask"}`)
	require.Equal(t, http.StatusOK, status, body)
	status, body = fixture.request(http.MethodPut, "/api/v1/mask_rules/quoted-scope", fixture.adminToken,
		`{"datasource_id":"ds-1","column_name":"phone","sensitive_type":"phone","algo":"mask"}`)
	require.Equal(t, http.StatusOK, status, body)
	stored, err = fixture.store.MaskRules().Get(context.Background(), "quoted-scope")
	require.NoError(t, err)
	require.Empty(t, stored.SchemaName, "omitted schema_name in a full PUT must become an empty string")
	require.Empty(t, stored.TableName, "omitted table_name in a full PUT must become an empty string")
}

func TestMaskRuleScopeIdentifierValidation(t *testing.T) {
	invalid := []struct {
		name, schema, table string
	}{
		{name: "schema wildcard", schema: "tenant*", table: "customers"},
		{name: "table wildcard", table: "customer%"},
		{name: "schema dot", schema: "tenant.public", table: "customers"},
		{name: "table dot", table: "public.customers"},
		{name: "schema control", schema: "tenant\u0000x", table: "customers"},
		{name: "table leading whitespace", table: " customers"},
		{name: "too long", table: strings.Repeat("表", maxExternalIdentifierRunes+1)},
	}
	for index, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAdminFixture(t)
			datasourceID := "ds-1"
			payload, err := json.Marshal(maskRuleInput{ID: fmt.Sprintf("invalid-%d", index), DatasourceID: &datasourceID,
				SchemaName: test.schema, TableName: test.table, ColumnName: "phone", SensitiveType: "phone", Algo: "mask"})
			require.NoError(t, err)
			status, body := fixture.request(http.MethodPost, "/api/v1/mask_rules", fixture.adminToken, string(payload))
			require.Equal(t, http.StatusUnprocessableEntity, status, body)
			require.Contains(t, body, "INVALID_MASK_RULE")
		})
	}

	fixture := newAdminFixture(t)
	status, body := fixture.request(http.MethodPost, "/api/v1/mask_rules", fixture.adminToken,
		`{"id":"schema-without-table","datasource_id":"ds-1","schema_name":"tenant","column_name":"phone","sensitive_type":"phone","algo":"mask"}`)
	require.Equal(t, http.StatusUnprocessableEntity, status, body)
	require.Contains(t, body, "INVALID_MASK_RULE")
	require.Contains(t, body, "schema_name must be provided together with table_name")
}

func TestMaskRulePhysicalAndAlgorithmScopeConflicts(t *testing.T) {
	t.Run("physical key", func(t *testing.T) {
		fixture := newAdminFixture(t)
		for index := 0; index < 2; index++ {
			status, body := fixture.request(http.MethodPost, "/api/v1/mask_rules", fixture.adminToken,
				fmt.Sprintf(`{"id":"physical-%d","datasource_id":"ds-1","schema_name":"Sales","table_name":"Customers","column_name":" PHONE ","sensitive_type":"phone","algo":"mask"}`, index))
			if index == 0 {
				require.Equal(t, http.StatusOK, status, body)
			} else {
				require.Equal(t, http.StatusConflict, status, body)
				require.Contains(t, body, "MASK_RULE_CONFLICT")
			}
		}
	})

	t.Run("enabled different algorithms", func(t *testing.T) {
		fixture := newAdminFixture(t)
		status, body := fixture.request(http.MethodPost, "/api/v1/mask_rules", fixture.adminToken,
			`{"id":"global-block","column_name":"secret","sensitive_type":"generic","algo":"block"}`)
		require.Equal(t, http.StatusOK, status, body)
		status, body = fixture.request(http.MethodPost, "/api/v1/mask_rules", fixture.adminToken,
			`{"id":"table-mask","datasource_id":"ds-1","table_name":"customers","column_name":"secret","sensitive_type":"phone","algo":"mask"}`)
		require.Equal(t, http.StatusConflict, status, body)
		require.Contains(t, body, "MASK_RULE_SCOPE_CONFLICT")
		require.Contains(t, body, "align the algorithms or delete one rule")
	})

	t.Run("same algorithm and different tables coexist", func(t *testing.T) {
		fixture := newAdminFixture(t)
		for id, table := range map[string]string{"global-mask": "", "customers-mask": "customers", "orders-mask": "orders"} {
			status, body := fixture.request(http.MethodPost, "/api/v1/mask_rules", fixture.adminToken,
				fmt.Sprintf(`{"id":%q,"datasource_id":"ds-1","table_name":%q,"column_name":"phone","sensitive_type":"phone","algo":"mask"}`, id, table))
			require.Equal(t, http.StatusOK, status, body)
		}
	})

	t.Run("disabled side does not conflict and enabling via PUT does", func(t *testing.T) {
		fixture := newAdminFixture(t)
		status, body := fixture.request(http.MethodPost, "/api/v1/mask_rules", fixture.adminToken,
			`{"id":"global-disabled","datasource_id":"ds-1","column_name":"draft_only","sensitive_type":"generic","algo":"block","enabled":false}`)
		require.Equal(t, http.StatusOK, status, body)
		status, body = fixture.request(http.MethodPost, "/api/v1/mask_rules", fixture.adminToken,
			`{"id":"table-enabled","datasource_id":"ds-1","table_name":"customers","column_name":"draft_only","sensitive_type":"phone","algo":"mask"}`)
		require.Equal(t, http.StatusOK, status, body)

		status, body = fixture.request(http.MethodPost, "/api/v1/mask_rules", fixture.adminToken,
			`{"id":"global-enabled","datasource_id":"ds-1","column_name":"enable_later","sensitive_type":"generic","algo":"block"}`)
		require.Equal(t, http.StatusOK, status, body)
		status, body = fixture.request(http.MethodPost, "/api/v1/mask_rules", fixture.adminToken,
			`{"id":"table-disabled","datasource_id":"ds-1","table_name":"customers","column_name":"enable_later","sensitive_type":"phone","algo":"mask","enabled":false}`)
		require.Equal(t, http.StatusOK, status, body)
		status, body = fixture.request(http.MethodPut, "/api/v1/mask_rules/table-disabled", fixture.adminToken,
			`{"datasource_id":"ds-1","table_name":"customers","column_name":"enable_later","sensitive_type":"phone","algo":"mask","enabled":true}`)
		require.Equal(t, http.StatusConflict, status, body)
		require.Contains(t, body, "MASK_RULE_SCOPE_CONFLICT")
	})

	t.Run("range type and params define equality", func(t *testing.T) {
		fixture := newAdminFixture(t)
		payloads := []struct {
			body       string
			wantStatus int
		}{
			{body: `{"id":"range-global","datasource_id":"ds-1","column_name":"amount","sensitive_type":"number","algo":"range","range_bucket_width":10}`, wantStatus: http.StatusOK},
			{body: `{"id":"range-same","datasource_id":"ds-1","table_name":"orders","column_name":"amount","sensitive_type":"number","algo":"range","range_bucket_width":10}`, wantStatus: http.StatusOK},
			{body: `{"id":"range-different","datasource_id":"ds-1","table_name":"invoices","column_name":"amount","sensitive_type":"number","algo":"range","range_bucket_width":20}`, wantStatus: http.StatusConflict},
		}
		for _, test := range payloads {
			status, body := fixture.request(http.MethodPost, "/api/v1/mask_rules", fixture.adminToken, test.body)
			require.Equal(t, test.wantStatus, status, body)
			if status == http.StatusConflict {
				require.Contains(t, body, "MASK_RULE_SCOPE_CONFLICT")
			}
		}
	})
}
