package adminapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestPolicyPreflightExplainsOnlyDefiniteDenial(t *testing.T) {
	fixture := newAdminFixture(t)
	before, err := fixture.store.AuditLogs().Page(context.Background(), 1, 1)
	require.NoError(t, err)
	status, _ := fixture.request(http.MethodPost, "/api/v1/policies/preflight", "",
		`{"agent_id":"agent-1","datasource_id":"ds-1","sql":"SELECT id FROM public.customers LIMIT 1"}`)
	require.Equal(t, http.StatusUnauthorized, status)
	status, body := fixture.request(http.MethodPost, "/api/v1/policies/preflight", fixture.adminToken,
		`{"agent_id":"agent-1","datasource_id":"ds-1","sql":"SELECT id FROM public.customers LIMIT 1"}`)
	require.Equal(t, http.StatusOK, status, body)
	require.Contains(t, body, `"decision":"unknown"`)
	require.Contains(t, body, `"allow_policy_ids":["policy-1"]`)
	require.NotContains(t, body, "SELECT id")
	status, body = fixture.request(http.MethodPost, "/api/v1/policies/preflight", fixture.adminToken,
		`{"agent_id":"agent-1","datasource_id":"ds-1","sql":"SELECT id FROM public.secrets LIMIT 1"}`)
	require.Equal(t, http.StatusOK, status, body)
	require.Contains(t, body, `"decision":"deny"`)
	require.Contains(t, body, `"state":"deny"`)
	after, err := fixture.store.AuditLogs().Page(context.Background(), 1, 1)
	require.NoError(t, err)
	require.Equal(t, before.Total, after.Total, "offline preflight must not write execution audit")
	_, err = fixture.store.Policies().Create(context.Background(), model.Policy{
		ID: "policy-deny", AgentID: "agent-1", DatasourceID: "ds-1",
		ObjectType: "table", ObjectName: "public.customers", Action: "deny",
	})
	require.NoError(t, err)
	status, body = fixture.request(http.MethodPost, "/api/v1/policies/preflight", fixture.adminToken,
		`{"agent_id":"agent-1","datasource_id":"ds-1","sql":"SELECT id FROM public.customers LIMIT 1"}`)
	require.Equal(t, http.StatusOK, status, body)
	require.Contains(t, body, `"decision":"deny"`)
	require.Contains(t, body, `"deny_policy_ids":["policy-deny"]`)
}
