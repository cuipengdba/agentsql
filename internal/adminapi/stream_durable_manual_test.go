package adminapi

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/bootstrap"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/rbac"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/stretchr/testify/require"
)

// Set the DSN to an isolated empty PostgreSQL database. Two independent
// gateway store connections prove that the SSE source is shared and durable.
func TestDurableAuditStreamAcrossGatewaysManual(t *testing.T) {
	dsn := os.Getenv("AGENTSQL_TEST_AUDIT_STREAM_GATEWAYS_DSN")
	if dsn == "" {
		t.Skip("set AGENTSQL_TEST_AUDIT_STREAM_GATEWAYS_DSN to run cross-gateway SSE acceptance")
	}
	ctx := t.Context()
	options := store.MetadataOptions{Driver: store.DialectPostgres, PostgresDSN: dsn, AutoMigrate: true}
	secret := []byte("0123456789abcdef0123456789abcdef")
	first, err := store.OpenMetadata(ctx, options, secret)
	require.NoError(t, err)
	defer first.Close()
	second, err := store.OpenMetadata(ctx, options, secret)
	require.NoError(t, err)
	defer second.Close()
	require.True(t, second.DurableAuditStream())
	_, err = first.RBAC().CreateTenant(ctx, model.Tenant{ID: "tenant_other", Name: "Other", Status: "active"})
	require.NoError(t, err)
	adminTenant, err := store.WithTenant(ctx, store.DefaultTenantID)
	require.NoError(t, err)
	otherTenant, err := store.WithTenant(ctx, "tenant_other")
	require.NoError(t, err)
	_, err = first.AuditLogs().Insert(otherTenant, model.AuditLog{Decision: "deny"})
	require.NoError(t, err)
	_, err = first.AuditLogs().Insert(adminTenant, model.AuditLog{Decision: "allow"})
	require.NoError(t, err)
	handler := &Handler{
		deps:        Deps{Runtime: &bootstrap.Runtime{Store: second}},
		streamSlots: make(chan struct{}, 1), heartbeat: time.Hour,
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		principal := rbac.Principal{UserID: "stream-user", TenantID: store.DefaultTenantID}
		request = request.WithContext(context.WithValue(request.Context(), principalContextKey{}, principal))
		handler.stream(writer, request)
	}))
	defer server.Close()
	request, err := http.NewRequest(http.MethodGet, server.URL, nil)
	require.NoError(t, err)
	request.Header.Set("Last-Event-ID", "0")
	client := &http.Client{Timeout: 7 * time.Second}
	response, err := client.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	reader := bufio.NewReader(response.Body)
	require.Contains(t, readSSEFrame(t, reader), "event: hello")
	history := readSSEFrame(t, reader)
	require.Contains(t, history, "id: 1\n")
	require.Contains(t, history, `"decision":"allow"`)
	require.NotContains(t, history, `"decision":"deny"`)
	_, err = first.AuditLogs().Insert(otherTenant, model.AuditLog{Decision: "deny"})
	require.NoError(t, err)
	_, err = first.AuditLogs().Insert(adminTenant, model.AuditLog{Decision: "allow"})
	require.NoError(t, err)
	live := readSSEFrame(t, reader)
	require.True(t, strings.Contains(live, "id: 2\n"), live)
	require.NotContains(t, live, `"decision":"deny"`)
}
