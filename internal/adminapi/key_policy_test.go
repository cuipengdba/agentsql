package adminapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestKeyPolicyAPIRequiresMatchingRedisDatabase(t *testing.T) {
	fixture := newAdminFixture(t)
	ctx := context.Background()
	_, err := fixture.store.Datasources().Create(ctx, model.Datasource{ID: "redis-policy", Name: "Redis policy",
		DBType: "redis", Host: "redis.test", Port: 6379, Database: "0", Username: "agent_reader",
		TLSMode: "strict", TLSCAFile: "ca.crt", ConnLimit: 2, StmtTimeoutMS: 2000}, "test-password")
	require.NoError(t, err)
	for _, test := range []struct {
		name, datasource, scope string
		status                  int
	}{
		{"allowed bounded prefix", "redis-policy", "db0:customers:*", http.StatusOK},
		{"wrong database", "redis-policy", "db1:customers:*", http.StatusUnprocessableEntity},
		{"global wildcard", "redis-policy", "db0:*", http.StatusUnprocessableEntity},
		{"sql datasource", "ds-1", "db0:customers:*", http.StatusUnprocessableEntity},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := `{"id":"` + test.name + `","agent_id":"agent-1","datasource_id":"` + test.datasource +
				`","object_type":"key","object_name":"` + test.scope + `","action":"allow"}`
			status, response := fixture.request(http.MethodPost, "/api/v1/policies", fixture.adminToken, body)
			require.Equal(t, test.status, status, response)
		})
	}
}

func TestHashFieldPolicyAPIRequiresExactKeyAndMatchingDatasource(t *testing.T) {
	fixture := newAdminFixture(t)
	ctx := context.Background()
	_, err := fixture.store.Datasources().Create(ctx, model.Datasource{ID: "redis-hash-policy", Name: "Redis Hash policy",
		DBType: "redis", Host: "redis.test", Port: 6379, Database: "0", Username: "agent_reader",
		TLSMode: "strict", TLSCAFile: "ca.crt", ConnLimit: 2, StmtTimeoutMS: 2000}, "test-password")
	require.NoError(t, err)
	for _, test := range []struct {
		name, datasource, scope string
		status                  int
	}{
		{"allowed field", "redis-hash-policy", "db0:customers:1#phone", http.StatusOK},
		{"wrong database", "redis-hash-policy", "db1:customers:1#phone", http.StatusUnprocessableEntity},
		{"prefix key", "redis-hash-policy", "db0:customers:*#phone", http.StatusUnprocessableEntity},
		{"sql datasource", "ds-1", "db0:customers:1#phone", http.StatusUnprocessableEntity},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := `{"id":"hash-` + test.name + `","agent_id":"agent-1","datasource_id":"` + test.datasource +
				`","object_type":"key_field","object_name":"` + test.scope + `","action":"allow"}`
			status, response := fixture.request(http.MethodPost, "/api/v1/policies", fixture.adminToken, body)
			require.Equal(t, test.status, status, response)
		})
	}
}
