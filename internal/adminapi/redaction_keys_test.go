package adminapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

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
