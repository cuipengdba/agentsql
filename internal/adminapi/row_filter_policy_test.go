package adminapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestAdminRowFilterPolicyRequiresSafePostgresShape(t *testing.T) {
	fixture := newAdminFixture(t)
	status, body := fixture.request(http.MethodPost, "/api/v1/policies", fixture.adminToken,
		`{"id":"row-safe","agent_id":"agent-1","datasource_id":"ds-1","object_type":"table","object_name":"public.customers","row_filter":"id = 1","action":"allow"}`)
	require.Equal(t, http.StatusOK, status, body)
	stored, err := fixture.store.Policies().Get(context.Background(), "row-safe")
	require.NoError(t, err)
	require.Equal(t, "id = 1", *stored.RowFilter)

	for _, input := range []string{
		`{"id":"row-or","agent_id":"agent-1","datasource_id":"ds-1","object_type":"table","object_name":"public.customers","row_filter":"id = 1 OR id = 2","action":"allow"}`,
		`{"id":"row-wide","agent_id":"agent-1","datasource_id":"ds-1","object_type":"table","object_name":"public.*","row_filter":"id = 1","action":"allow"}`,
		`{"id":"row-deny","agent_id":"agent-1","datasource_id":"ds-1","object_type":"table","object_name":"public.customers","row_filter":"id = 1","action":"deny"}`,
	} {
		status, body = fixture.request(http.MethodPost, "/api/v1/policies", fixture.adminToken, input)
		require.Equal(t, http.StatusUnprocessableEntity, status, body)
	}

	_, err = fixture.store.Datasources().Create(context.Background(), model.Datasource{
		ID: "mysql-row-ds", Name: "MySQL", DBType: "mysql", Host: "db", Port: 3306,
		Database: "app", Username: "user", ConnLimit: 5, StmtTimeoutMS: 5000, RowLimit: 1000,
	}, "database-password")
	require.NoError(t, err)
	status, body = fixture.request(http.MethodPost, "/api/v1/policies", fixture.adminToken,
		`{"id":"row-mysql","agent_id":"agent-1","datasource_id":"mysql-row-ds","object_type":"table","object_name":"public.customers","row_filter":"id = 1","action":"allow"}`)
	require.Equal(t, http.StatusUnprocessableEntity, status, body)
}
