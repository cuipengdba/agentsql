package mcpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/rbac"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/stretchr/testify/require"
)

type sqlbotTestVerifier struct {
	binding store.SQLBotBinding
	err     error
}

func (v *sqlbotTestVerifier) VerifySQLBot(context.Context, string, string) (rbac.Principal, store.SQLBotBinding, error) {
	return rbac.Principal{UserID: v.binding.AgentSQLUserID, TenantID: v.binding.TenantID}, v.binding, v.err
}

func TestSQLBotScopedIdentityIsAlwaysVerified(t *testing.T) {
	b := store.SQLBotBinding{ID: "binding", TenantID: "tenant", AgentID: "agent", AgentSQLUserID: "user", DatasourceID: "ds", Revision: 1}
	v := &sqlbotTestVerifier{binding: b}
	calls := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		human, ok := delegatedHumanFromContext(r.Context())
		require.True(t, ok)
		require.Equal(t, "user", human.userID)
		scope, ok := sqlbotScope(r.Context())
		require.True(t, ok)
		require.Equal(t, b, scope)
		w.WriteHeader(http.StatusNoContent)
	})
	serve := func(required bool, header string) int {
		r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
		r = r.WithContext(context.WithValue(r.Context(), requestIdentityContextKey{}, requestIdentity{agent: model.Agent{ID: "agent", TenantID: "tenant", Level: "readonly"}, plainKey: "test-agent-key"}))
		r.Header.Set(humanDelegationHeader, header)
		w := httptest.NewRecorder()
		humanDelegationMiddleware(required, nil, next, sqlbotHTTPGate{verifier: v}).ServeHTTP(w, r)
		return w.Code
	}
	require.Equal(t, http.StatusNoContent, serve(false, "Bearer SQB1.test"))
	require.Equal(t, http.StatusNoContent, serve(true, "Bearer SQB1.test"))
	v.err = errors.New("revoked")
	require.Equal(t, http.StatusUnauthorized, serve(false, "Bearer SQB1.test"))
	require.Equal(t, http.StatusUnauthorized, serve(false, "Bearer forged"))
	require.Equal(t, 2, calls)
}

func TestSQLBotScopeRejectsOtherDatasourcesAndWriteTools(t *testing.T) {
	gate := sqlbotHTTPGate{}
	b := store.SQLBotBinding{DatasourceID: "ds", SchemaName: "public", DatasourceDBType: "postgres"}
	cases := []struct {
		name, body string
		allowed    bool
	}{
		{"discovery", `{"method":"tools/call","params":{"name":"list_datasources","arguments":{}}}`, true},
		{"read", `{"method":"tools/call","params":{"name":"query","arguments":{"datasource_id":"ds","sql":"SELECT id FROM public.customers WHERE id=1 LIMIT 1"}}}`, true},
		{"other source", `{"method":"tools/call","params":{"name":"query","arguments":{"datasource_id":"other","sql":"SELECT id FROM public.customers LIMIT 1"}}}`, false},
		{"write", `{"method":"tools/call","params":{"name":"execute_write","arguments":{"datasource_id":"ds","sql":"DELETE FROM public.customers"}}}`, false},
		{"write approval", `{"method":"tools/call","params":{"name":"request_approval","arguments":{"datasource_id":"ds","sql":"DELETE FROM public.customers"}}}`, false},
		{"read approval", `{"method":"tools/call","params":{"name":"request_approval","arguments":{"datasource_id":"ds","sql":"SELECT id FROM public.customers LIMIT 1"}}}`, true},
		{"schema", `{"method":"tools/call","params":{"name":"list_schema","arguments":{"datasource_id":"ds","table":"other.customers"}}}`, false},
		{"B5", `{"method":"tools/call","params":{"name":"open_session","arguments":{}}}`, false},
		{"stacked", `{"method":"tools/call","params":{"name":"query","arguments":{"datasource_id":"ds","sql":"SELECT 1; DELETE FROM public.customers"}}}`, false},
		{"unknown", `{"method":"resources/read","params":{}}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(tc.body))
			require.Equal(t, tc.allowed, gate.allowed(r, b))
		})
	}
	require.False(t, gate.allowed(httptest.NewRequest(http.MethodGet, "/mcp", nil), b))
}

func TestSQLBotReadOnlyRejectsSideEffectShapes(t *testing.T) {
	for _, sql := range []string{
		"SELECT id INTO new_table FROM public.customers",
		"EXPLAIN SELECT id FROM public.customers",
		"EXPLAIN ANALYZE SELECT id FROM public.customers",
		"WITH d AS (DELETE FROM public.customers RETURNING id) SELECT id FROM d",
		"SELECT id FROM public.customers FOR UPDATE",
		"SELECT pg_sleep(10)",
		"SELECT public.custom_function(id) FROM public.customers",
		"SELECT nextval('customer_seq')",
	} {
		require.False(t, sqlbotReadOnly(sql), sql)
	}
	for _, sql := range []string{
		"SELECT count(*) AS total FROM public.customers WHERE id > 0",
		"SELECT city, sum(amount) AS total FROM public.orders WHERE id > 0 GROUP BY city LIMIT 20",
		"SELECT id, phone FROM public.customers WHERE id > 0 LIMIT 5",
	} {
		require.True(t, sqlbotReadOnly(sql), sql)
	}
}

func TestSQLBotMySQLReadOnlyIsBoundedToSelectsAndKnownFunctions(t *testing.T) {
	gate := sqlbotHTTPGate{}
	mysqlBinding := store.SQLBotBinding{DatasourceID: "mysql-ds", SchemaName: "app", DatasourceDBType: "mysql"}
	mysqlRequest := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"method":"tools/call","params":{"name":"query","arguments":{"datasource_id":"mysql-ds","sql":"SELECT COUNT(*) AS total FROM app.orders"}}}`))
	require.True(t, gate.allowed(mysqlRequest, mysqlBinding))
	mysqlBinding.DatasourceDBType = ""
	mysqlRequest = httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"method":"tools/call","params":{"name":"query","arguments":{"datasource_id":"mysql-ds","sql":"SELECT COUNT(*) AS total FROM app.orders"}}}`))
	require.False(t, gate.allowed(mysqlRequest, mysqlBinding), "missing verified datasource type fails closed")

	for _, sql := range []string{
		"SELECT id, amount FROM app.orders WHERE id > 0 LIMIT 10",
		"SELECT city, SUM(amount) AS total FROM app.orders GROUP BY city LIMIT 20",
		"SELECT COUNT(*) AS total FROM app.orders WHERE id > 0",
		"SELECT COUNT(`o`.`id`) AS `order_count` FROM `app`.`orders` `o` LIMIT 1000",
	} {
		require.True(t, sqlbotReadOnlyForDatasource(sql, "mysql"), sql)
	}
	for _, sql := range []string{
		"SELECT id FROM app.orders INTO OUTFILE '/tmp/rows'",
		"SELECT id FROM app.orders FOR UPDATE",
		"SELECT GET_LOCK('side_effect', 10)",
		"SELECT id FROM app.orders WHERE GET_LOCK('side_effect', 10) = 1",
		"SELECT LOAD_FILE('/etc/passwd')",
		"SELECT id FROM app.orders WHERE SLEEP(1) = 0",
		"SELECT @x := id FROM app.orders",
		"SELECT id FROM app.orders; DELETE FROM app.orders",
		"WITH d AS (DELETE FROM app.orders RETURNING id) SELECT id FROM d",
		"EXPLAIN SELECT id FROM app.orders",
	} {
		require.False(t, sqlbotReadOnlyForDatasource(sql, "mysql"), sql)
	}
	require.False(t, sqlbotReadOnlyForDatasource("SELECT 1", "unknown"))
}
