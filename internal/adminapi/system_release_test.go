package adminapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/cuipengdba/agentsql/internal/version"
	"github.com/stretchr/testify/require"
)

func TestSystemReleaseRequiresAdminAuditPermission(t *testing.T) {
	fixture := newAdminFixture(t)
	status, _ := fixture.request(http.MethodGet, "/api/v1/system/release", "", "")
	require.Equal(t, http.StatusUnauthorized, status)
	status, body := fixture.request(http.MethodGet, "/api/v1/system/release", fixture.adminToken, "")
	require.Equal(t, http.StatusOK, status)
	var response struct {
		Code int `json:"code"`
		Data struct {
			Version     string `json:"version"`
			UpgradeMode string `json:"upgrade_mode"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &response))
	require.Zero(t, response.Code)
	require.Equal(t, version.Version, response.Data.Version)
	require.Equal(t, "check-and-dry-run", response.Data.UpgradeMode)
}
