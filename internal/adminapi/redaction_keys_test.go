package adminapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/bootstrap"
	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestRedactionKeysReadOnlyObservation(t *testing.T) {
	fixture := newAdminFixture(t)
	ctx := context.Background()
	require.NoError(t, fixture.store.RedactionKeys().RegisterStandby(ctx, "10", strings.Repeat("a", 64), "next", "revision-2", nil))
	require.NoError(t, fixture.store.RedactionKeys().RegisterStandby(ctx, "2", strings.Repeat("2", 64), "current", "revision-1", nil))

	status, body := fixture.request(http.MethodGet, "/api/v1/redaction/keys", fixture.adminToken, "")
	require.Equal(t, http.StatusOK, status, body)
	var response struct {
		Code int `json:"code"`
		Data struct {
			Registered []redactionKeyVersionDTO `json:"registered"`
			Observed   redactionKeyObservedDTO  `json:"observed"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &response))
	require.Zero(t, response.Code)
	require.Equal(t, "unavailable", response.Data.Observed.Status)
	require.Len(t, response.Data.Registered, 2)
	require.Equal(t, []string{"2", "10"}, []string{response.Data.Registered[0].ID, response.Data.Registered[1].ID})
	require.NotContains(t, body, "key_b64")
	require.NotContains(t, body, "key_file")

	status, body = fixture.request(http.MethodGet, "/api/v1/redaction/keys", "", "")
	require.Equal(t, http.StatusUnauthorized, status, body)
	require.Contains(t, body, `"code":401`)
}

func TestRedactionKeysProjectsObservedAndDriftBesideRegistered(t *testing.T) {
	cfg := adminTestConfig(filepath.Join(t.TempDir(), "observed.db"))
	encoded := base64.StdEncoding.EncodeToString([]byte("0123456789abcdefghijklmnopqrstuv"))
	cfg.Redaction.HashKeys = &config.RedactionHashKeysConfig{
		ActiveVersion: 1,
		Keys:          []config.RedactionHashKeySpec{{ID: 1, KeyB64: &encoded}},
	}
	runtime, err := bootstrap.Assemble(context.Background(), cfg, []byte(adminTestSecret))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })
	handler, err := NewHandler(Deps{
		Runtime: runtime, Config: cfg, AdminUsername: "admin", AdminPassword: "password",
		TokenKey: DeriveTokenKey([]byte(adminTestSecret)),
	}, zerolog.Nop())
	require.NoError(t, err)
	token, _, err := issueAdminToken(DeriveTokenKey([]byte(adminTestSecret)), time.Now(), "observed-test")
	require.NoError(t, err)
	request := func() string {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/redaction/keys", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		handler.ServeHTTP(recorder, req)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		return recorder.Body.String()
	}

	body := request()
	require.Contains(t, body, `"registered":[]`)
	require.Contains(t, body, `"status":"available"`)
	require.Contains(t, body, `"active_version":1`)
	require.Contains(t, body, `"unsatisfied":[3]`)
	require.Contains(t, body, `"number":3`)
	require.NotContains(t, body, encoded)
	require.NotContains(t, body, "key_b64")
	require.NotContains(t, body, "key_file")

	result, available := runtime.RedactionReconciliation()
	require.True(t, available)
	require.NoError(t, runtime.Store.RedactionKeys().RegisterStandby(context.Background(), "1", result.Observed.Keys[0].Commitment, "active", result.Observed.Revision, nil))
	_, _, _, err = runtime.Store.RedactionKeys().MarkActiveCAS(context.Background(), "1")
	require.NoError(t, err)
	require.NoError(t, runtime.RerunRedactionReconciliation(context.Background()))
	body = request()
	require.Contains(t, body, `"state":"active"`)
	require.Contains(t, body, `"ready":true`)
	require.Contains(t, body, `"unsatisfied":[]`)
}
