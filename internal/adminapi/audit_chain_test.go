package adminapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/bootstrap"
	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type auditChainTestProvider struct {
	manifest store.ChainManifest
	err      error
	entered  chan struct{}
	release  chan struct{}
	once     sync.Once
	domains  chan string
}

func (provider *auditChainTestProvider) ChainManifestForDomain(ctx context.Context, domain string) (store.ChainManifest, error) {
	if provider.domains != nil {
		provider.domains <- domain
	}
	if provider.entered != nil {
		provider.once.Do(func() { close(provider.entered) })
	}
	if provider.release != nil {
		select {
		case <-provider.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return provider.manifest, provider.err
}

type auditChainTestHarness struct {
	store   *store.Store
	runtime *bootstrap.Runtime
	db      *sql.DB
	cfg     config.Config
	token   string
}

func newAuditChainTestHarness(t *testing.T) *auditChainTestHarness {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit-chain.db")
	opened, err := store.OpenWithSecret(context.Background(), path, []byte(adminTestSecret))
	require.NoError(t, err)
	database, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	runtime := &bootstrap.Runtime{Store: opened}
	token, _, err := issueAdminToken(DeriveTokenKey([]byte(adminTestSecret)), time.Now(), "audit-chain-test")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
		require.NoError(t, runtime.Close())
	})
	return &auditChainTestHarness{
		store: opened, runtime: runtime, db: database, cfg: adminTestConfig(path), token: "Bearer " + token,
	}
}

func (harness *auditChainTestHarness) handler(t *testing.T, provider ChainManifestProvider) http.Handler {
	t.Helper()
	handler, err := NewHandler(Deps{
		Runtime: harness.runtime, Config: harness.cfg, AdminUsername: "admin", AdminPassword: "password",
		TokenKey: DeriveTokenKey([]byte(adminTestSecret)), ChainManifests: provider,
	}, zerolog.Nop())
	require.NoError(t, err)
	return handler
}

func auditChainRequest(handler http.Handler, method, target, token string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, nil)
	if token != "" {
		request.Header.Set("Authorization", token)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func decodeAuditChainResponse(t *testing.T, recorder *httptest.ResponseRecorder, target any) (int, string) {
	t.Helper()
	var response struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	if target != nil {
		require.NoError(t, json.Unmarshal(response.Data, target))
	}
	return response.Code, response.Msg
}

func (harness *auditChainTestHarness) seedAndProvision(t *testing.T) {
	t.Helper()
	for index := 0; index < 2; index++ {
		_, err := harness.store.AuditLogs().Insert(context.Background(), model.AuditLog{Decision: "allow"})
		require.NoError(t, err)
	}
	provisioner := store.NewChainProvisioner(
		harness.db, store.DialectSQLite, chainDomainManagement, keylessChainManifest{}, store.BackfillConfig{},
	)
	require.NoError(t, provisioner.Provision(context.Background(), "admin-api-test"))
}

func TestAuditChainStatus(t *testing.T) {
	harness := newAuditChainTestHarness(t)
	handler := harness.handler(t, nil)

	recorder := auditChainRequest(handler, http.MethodGet, "/api/v1/audit-chain/status", harness.token)
	require.Equal(t, http.StatusOK, recorder.Code)
	var status auditChainStatusView
	code, _ := decodeAuditChainResponse(t, recorder, &status)
	require.Zero(t, code)
	require.Equal(t, "management", status.ChainID)
	require.Equal(t, "DISABLED", status.Status)
	require.Nil(t, status.Mode)
	require.Zero(t, status.HeadSeq)
	require.Nil(t, status.HeadID)
	require.Nil(t, status.ProtectedSince)
	require.Nil(t, status.GenesisAt)
	require.Nil(t, status.ObservedInstance)
	require.Nil(t, status.Verification.Result)
	require.Nil(t, status.Verification.LastVerifiedAt)
	require.Nil(t, status.Verification.LastVerifiedHeadSeq)
	require.Nil(t, status.Verification.BreakSeq)
	require.Nil(t, status.Verification.BreakID)
	require.Nil(t, status.Verification.BreakReason)
	for _, field := range []string{
		`"chain_id"`, `"status"`, `"mode"`, `"head_seq"`, `"head_id"`, `"protected_since"`,
		`"genesis_at"`, `"observed_instance"`, `"verification"`, `"result"`,
		`"last_verified_at"`, `"last_verified_head_seq"`, `"break_seq"`, `"break_id"`, `"break_reason"`,
	} {
		require.Contains(t, recorder.Body.String(), field)
	}

	invalid := auditChainRequest(handler, http.MethodGet, "/api/v1/audit-chain/status?domain=invalid", harness.token)
	require.Equal(t, http.StatusBadRequest, invalid.Code)
	require.Contains(t, invalid.Body.String(), "CHAIN_DOMAIN_INVALID")

	_, err := harness.db.Exec("DELETE FROM chain_state WHERE chain_id = 'management'")
	require.NoError(t, err)
	missing := auditChainRequest(handler, http.MethodGet, "/api/v1/audit-chain/status", harness.token)
	require.Equal(t, http.StatusNotFound, missing.Code)
	require.Contains(t, missing.Body.String(), `domain \"management\"`)
}

func TestAuditChainVerifyValidAndTamperedRemainHTTP200(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		harness := newAuditChainTestHarness(t)
		harness.seedAndProvision(t)
		handler := harness.handler(t, &auditChainTestProvider{manifest: keylessChainManifest{}})
		recorder := auditChainRequest(handler, http.MethodPost, "/api/v1/audit-chain/verify", harness.token)
		require.Equal(t, http.StatusOK, recorder.Code)
		var result auditChainVerifyView
		decodeAuditChainResponse(t, recorder, &result)
		require.Equal(t, "VALID_AT_OBSERVED_HEAD", result.Result)
		require.Equal(t, int64(2), result.HeadSeq)
		require.Equal(t, int64(2), result.Total)
		require.Zero(t, result.Unchained)
		require.Nil(t, result.Break)

		statusRecorder := auditChainRequest(handler, http.MethodGet, "/api/v1/audit-chain/status", harness.token)
		require.Equal(t, http.StatusOK, statusRecorder.Code)
		var status auditChainStatusView
		decodeAuditChainResponse(t, statusRecorder, &status)
		require.NotNil(t, status.Verification.Result)
		require.Equal(t, "VALID_AT_OBSERVED_HEAD", *status.Verification.Result)
		require.NotNil(t, status.Verification.LastVerifiedAt)
		require.NotNil(t, status.Verification.LastVerifiedHeadSeq)
		require.Equal(t, int64(2), *status.Verification.LastVerifiedHeadSeq)
	})

	t.Run("tampered payload", func(t *testing.T) {
		harness := newAuditChainTestHarness(t)
		harness.seedAndProvision(t)
		_, err := harness.db.Exec(`UPDATE audit_logs SET details_json = '{"tampered":true}' WHERE chain_seq = 2`)
		require.NoError(t, err)
		handler := harness.handler(t, &auditChainTestProvider{manifest: keylessChainManifest{}})
		recorder := auditChainRequest(handler, http.MethodPost, "/api/v1/audit-chain/verify", harness.token)
		require.Equal(t, http.StatusOK, recorder.Code)
		var result auditChainVerifyView
		decodeAuditChainResponse(t, recorder, &result)
		require.Equal(t, "self_mismatch", result.Result)
		require.NotNil(t, result.Break)
		require.Equal(t, "self_mismatch", result.Break.Reason)
		require.Equal(t, int64(2), result.Break.Seq)
	})
}

func TestAuditChainVerifySingleFlight(t *testing.T) {
	harness := newAuditChainTestHarness(t)
	provider := &auditChainTestProvider{
		manifest: keylessChainManifest{}, entered: make(chan struct{}), release: make(chan struct{}),
	}
	handler := harness.handler(t, provider)
	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		firstDone <- auditChainRequest(handler, http.MethodPost, "/api/v1/audit-chain/verify", harness.token)
	}()
	select {
	case <-provider.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first verification did not reach manifest provider")
	}
	second := auditChainRequest(handler, http.MethodPost, "/api/v1/audit-chain/verify", harness.token)
	require.Equal(t, http.StatusConflict, second.Code)
	require.Contains(t, second.Body.String(), "CHAIN_VERIFY_IN_PROGRESS")
	close(provider.release)
	first := <-firstDone
	require.Equal(t, http.StatusOK, first.Code)
}

func TestAuditChainVerifyRateLimit(t *testing.T) {
	harness := newAuditChainTestHarness(t)
	handler := harness.handler(t, &auditChainTestProvider{manifest: keylessChainManifest{}})
	first := auditChainRequest(handler, http.MethodPost, "/api/v1/audit-chain/verify", harness.token)
	require.Equal(t, http.StatusOK, first.Code)
	second := auditChainRequest(handler, http.MethodPost, "/api/v1/audit-chain/verify", harness.token)
	require.Equal(t, http.StatusTooManyRequests, second.Code)
	var failure auditChainErrorView
	decodeAuditChainResponse(t, second, &failure)
	require.Equal(t, "CHAIN_VERIFY_RATE_LIMITED", failure.ErrorCode)
	require.Greater(t, failure.RetryAfterSeconds, int64(0))
	require.LessOrEqual(t, failure.RetryAfterSeconds, int64(300))
}

func TestAuditChainVerifyUnavailable(t *testing.T) {
	tests := []struct {
		name     string
		provider ChainManifestProvider
	}{
		{name: "nil provider"},
		{name: "provider error", provider: &auditChainTestProvider{err: errors.New("sentinel manifest failure")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newAuditChainTestHarness(t)
			handler := harness.handler(t, test.provider)
			recorder := auditChainRequest(handler, http.MethodPost, "/api/v1/audit-chain/verify", harness.token)
			require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
			require.Contains(t, recorder.Body.String(), "CHAIN_VERIFIER_UNAVAILABLE")
			require.Contains(t, recorder.Body.String(), "chain verifier is unavailable")
			require.NotContains(t, recorder.Body.String(), "sentinel")
		})
	}
}

func TestAuditChainAutoDomainAndAuthorization(t *testing.T) {
	harness := newAuditChainTestHarness(t)
	harness.cfg.Store.Audit = &config.AuditStoreConfig{Separate: true, Driver: "postgres", DSN: "postgres://unused"}
	provider := &auditChainTestProvider{manifest: keylessChainManifest{}, domains: make(chan string, 1)}
	handler := harness.handler(t, provider)

	unauthorized := auditChainRequest(handler, http.MethodGet, "/api/v1/audit-chain/status", "")
	require.Equal(t, http.StatusUnauthorized, unauthorized.Code)

	verified := auditChainRequest(handler, http.MethodPost, "/api/v1/audit-chain/verify", harness.token)
	require.Equal(t, http.StatusInternalServerError, verified.Code)
	require.Equal(t, chainDomainTraffic, <-provider.domains)
	require.NotContains(t, verified.Body.String(), "postgres://unused")
}
