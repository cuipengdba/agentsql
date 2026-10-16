package adminapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/stretchr/testify/require"
)

func TestAuditOutboxStatusRouteAndState(t *testing.T) {
	fixture := newAdminFixture(t)
	status, _ := fixture.request(http.MethodGet, "/api/v1/audit/outbox-status", "", "")
	require.Equal(t, http.StatusUnauthorized, status)
	status, body := fixture.request(http.MethodGet, "/api/v1/audit/outbox-status", fixture.adminToken, "")
	require.Equal(t, http.StatusOK, status)
	require.Contains(t, body, `"state":"not_applicable"`)

	ctx, err := store.WithTenant(context.Background(), store.DefaultTenantID)
	require.NoError(t, err)
	handler := &Handler{deps: Deps{Runtime: fixture.runtime,
		Config: config.Config{Store: config.StoreConfig{Audit: &config.AuditStoreConfig{Separate: true}}}}}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/audit/outbox-status", nil).WithContext(ctx)
	recorder := httptest.NewRecorder()
	handler.auditOutboxStatus(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"enabled":true`)
	require.Contains(t, recorder.Body.String(), `"state":"healthy"`)
	require.Contains(t, recorder.Body.String(), `"pending":0`)
}
