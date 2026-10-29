package chatbi

import (
	"context"
	"crypto/aes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type selectionTestGateway struct {
	*fakeGateway
	columns []Column
}

func (g *selectionTestGateway) Schema(_ context.Context, _ Principal, _ Binding) ([]Column, error) {
	g.schemaCalls++
	if g.afterSchema != nil {
		g.afterSchema()
	}
	if g.schemaErr != nil {
		return nil, g.schemaErr
	}
	return g.columns, nil
}

func TestDatabaseSelectedTablesRestrictSchemaAndExecution(t *testing.T) {
	columns := []Column{{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"},
		{Schema: "public", Table: "people", Name: "name", DataType: "text"}}
	for _, dialect := range []string{"pg", "mysql"} {
		t.Run(dialect, func(t *testing.T) {
			binding := Binding{Dialect: dialect, Type: dialect, SelectedTables: `[{"schema":"public","table":"sales"}]`}
			filtered, err := filterDatabaseSchema(binding, columns)
			require.NoError(t, err)
			require.Len(t, filtered, 1)
			require.Equal(t, "sales", filtered[0].Table)
			_, err = filterDatabaseSchema(binding, columns[1:])
			require.ErrorIs(t, err, ErrGateway, "revoked selected table must fail closed")
			allow := "SELECT amount FROM public.sales"
			deny := "SELECT name FROM public.people"
			if dialect == "mysql" {
				allow = "SELECT amount FROM public.sales"
			}
			require.NoError(t, authorizeDatabaseSelectedSQL(binding, allow))
			require.ErrorIs(t, authorizeDatabaseSelectedSQL(binding, deny), ErrGateway)
			require.ErrorIs(t, authorizeDatabaseSelectedSQL(binding,
				"SELECT s.amount FROM public.sales s JOIN public.people p ON s.amount=p.name"), ErrGateway)
			require.ErrorIs(t, authorizeDatabaseSelectedSQL(binding,
				"SELECT amount FROM other.sales"), ErrGateway)
			binding.SelectedTables = "!"
			require.ErrorIs(t, authorizeDatabaseSelectedSQL(binding, allow), ErrGateway)
			binding.SelectedTables = `[ {"schema":"public","table":"sales"}, {"schema":"public","table":"sales"} ]`
			_, err = databaseSelectedTables(binding)
			require.ErrorIs(t, err, ErrGateway)
		})
	}
}

func TestOriginalDatabaseTableSelectionIsScopedAuditedAndRevocable(t *testing.T) {
	ctx := context.Background()
	service, principal, base, _, token := serviceFixture(t)
	user, revision, err := service.Store.User(ctx, principal.User.ID)
	require.NoError(t, err)
	user.Admin = true
	require.NoError(t, service.Store.put(ctx, userKey(user.ID), user, revision))
	principal, err = service.Identity.Verify(ctx, token)
	require.NoError(t, err)
	bound, boundRevision, _, err := service.bindingSnapshot(ctx, principal, "10")
	require.NoError(t, err)
	bound.Configuration = "server-owned-database-credential"
	require.NoError(t, service.Store.put(ctx, bindingKey(principal, "10"), bound, boundRevision))
	gateway := &selectionTestGateway{fakeGateway: base, columns: []Column{
		{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"},
		{Schema: "public", Table: "people", Name: "name", DataType: "text"},
	}}
	service.Gateway = gateway
	handler, err := NewHandler(service, "test")
	require.NoError(t, err)
	request := func(method, path, body, credential string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if credential != "" {
			r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+credential)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	choose := "/api/v1/datasource/chooseTables/10"
	choices := "/api/v1/agentsql/datasource/databaseTables/10"
	assertSelection := func(want string) Binding {
		t.Helper()
		current, err := service.binding(ctx, principal, "10")
		require.NoError(t, err)
		require.Equal(t, want, current.SelectedTables)
		return current
	}
	listing := request(http.MethodGet, "/api/v1/datasource/list", "", token)
	require.Equal(t, http.StatusOK, listing.Code)
	require.NotContains(t, listing.Body.String(), bound.Configuration)
	var sourceList struct {
		Data []struct {
			Configuration string `json:"configuration"`
			TypeName      string `json:"type_name"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(listing.Body.Bytes(), &sourceList))
	require.Len(t, sourceList.Data, 1)
	require.Equal(t, "PostgreSQL", sourceList.Data[0].TypeName)
	encoded, err := base64.StdEncoding.DecodeString(sourceList.Data[0].Configuration)
	require.NoError(t, err)
	require.NotEmpty(t, encoded)
	require.Zero(t, len(encoded)%aes.BlockSize)
	block, err := aes.NewCipher([]byte("SQLBot1234567890"))
	require.NoError(t, err)
	plain := make([]byte, len(encoded))
	for offset := 0; offset < len(encoded); offset += aes.BlockSize {
		block.Decrypt(plain[offset:offset+aes.BlockSize], encoded[offset:offset+aes.BlockSize])
	}
	padding := int(plain[len(plain)-1])
	require.GreaterOrEqual(t, padding, 1)
	require.LessOrEqual(t, padding, aes.BlockSize)
	plain = plain[:len(plain)-padding]
	var display map[string]any
	require.NoError(t, json.Unmarshal(plain, &display))
	require.Equal(t, "AGENTSQL_GATEWAY_MANAGED", display["host"])
	require.Equal(t, "AGENTSQL_GATEWAY_MANAGED", display["database"])
	require.Equal(t, "", display["password"])
	require.NotContains(t, string(plain), bound.Configuration)
	require.NotContains(t, string(plain), bound.AgentKey)
	require.NotContains(t, string(plain), bound.ConnectorToken)
	byConf := request(http.MethodPost, "/api/v1/datasource/getTablesByConf",
		`{"id":10,"type":"pg","configuration":"untrusted-client-connection"}`, token)
	require.Equal(t, http.StatusOK, byConf.Code)
	require.Contains(t, byConf.Body.String(), `"tableName":"sales"`)
	require.Contains(t, byConf.Body.String(), `"tableName":"people"`)
	require.NotContains(t, byConf.Body.String(), bound.Configuration)
	require.Equal(t, http.StatusUnprocessableEntity,
		request(http.MethodPost, "/api/v1/datasource/getTablesByConf", `{"type":"pg"}`, token).Code)
	require.Equal(t, http.StatusUnauthorized, request(http.MethodPost, choose, `[]`, "").Code)
	require.Equal(t, http.StatusUnprocessableEntity,
		request(http.MethodPost, choose, `[{"table_name":"unknown"}]`, token).Code)
	assertSelection("")
	require.Equal(t, http.StatusOK, request(http.MethodGet, choices, "", token).Code)
	other := testUser(t, "2", "other-db-admin")
	other.Admin = true
	require.NoError(t, service.Store.CreateUser(ctx, other))
	_, otherToken, err := service.Identity.Login(ctx, other.Account, "Synthetic-password-1")
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound,
		request(http.MethodPost, choose, `[{"table_name":"sales"}]`, otherToken).Code)
	require.Equal(t, http.StatusNotFound, request(http.MethodGet, choices, "", otherToken).Code)
	require.Equal(t, http.StatusNotFound, request(http.MethodPost, "/api/v1/datasource/getTablesByConf",
		`{"id":10,"type":"pg"}`, otherToken).Code)

	require.Equal(t, http.StatusOK,
		request(http.MethodPost, choose, `[{"table_name":"sales"}]`, token).Code)
	current := assertSelection(`[{"schema":"public","table":"sales"}]`)
	filtered, err := service.schema(ctx, principal, current)
	require.NoError(t, err)
	require.Len(t, filtered, 1)
	require.Equal(t, "sales", filtered[0].Table)
	tableList := request(http.MethodPost, "/api/v1/datasource/tableList/10", `{}`, token)
	require.Equal(t, http.StatusOK, tableList.Code)
	require.Contains(t, tableList.Body.String(), `"table_name":"sales"`)
	require.NotContains(t, tableList.Body.String(), `"table_name":"people"`)
	byConf = request(http.MethodPost, "/api/v1/datasource/getTablesByConf", `{"id":10,"type":"pg"}`, token)
	require.Equal(t, http.StatusOK, byConf.Code)
	require.Contains(t, byConf.Body.String(), `"tableName":"people"`)
	_, err = service.query(ctx, principal, current, "SELECT name FROM public.people")
	require.ErrorIs(t, err, ErrGateway)
	require.Equal(t, 0, base.queries, "unselected table reached gateway query")
	_, err = service.query(ctx, principal, current, "SELECT amount FROM public.sales")
	require.NoError(t, err)
	require.Equal(t, 1, base.queries)
	response := request(http.MethodGet, choices, "", token)
	require.Equal(t, http.StatusOK, response.Code)
	var listed struct {
		Data []struct {
			Name    string `json:"table_name"`
			Checked bool   `json:"checked"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &listed))
	require.Len(t, listed.Data, 2)
	require.Equal(t, "people", listed.Data[0].Name)
	require.False(t, listed.Data[0].Checked)
	require.True(t, listed.Data[1].Checked)
	gateway.columns = gateway.columns[1:]
	missing := request(http.MethodGet, choices, "", token)
	require.NotEqual(t, http.StatusOK, missing.Code)
	require.NotContains(t, missing.Body.String(), "people")
	require.NotEqual(t, http.StatusOK, request(http.MethodPost, choose, `[]`, token).Code)
	gateway.columns = []Column{
		{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"},
		{Schema: "public", Table: "people", Name: "name", DataType: "text"},
	}

	_, err = service.Store.db.ExecContext(ctx, `CREATE TRIGGER reject_database_selection_audit BEFORE INSERT ON chatbi_objects
		WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(FAIL, 'audit offline'); END`)
	require.NoError(t, err)
	require.NotEqual(t, http.StatusOK, request(http.MethodPost, choose, `[]`, token).Code)
	assertSelection(current.SelectedTables)
	_, err = service.Store.db.ExecContext(ctx, `DROP TRIGGER reject_database_selection_audit`)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, request(http.MethodPost, choose, `[]`, token).Code)
	current = assertSelection("!")
	filtered, err = service.schema(ctx, principal, current)
	require.NoError(t, err)
	require.Empty(t, filtered)
	_, err = service.query(ctx, principal, current, "SELECT amount FROM public.sales")
	require.ErrorIs(t, err, ErrGateway)

	gateway.afterSchema = func() {
		gateway.afterSchema = nil
		binding, rev, _, snapshotErr := service.bindingSnapshot(ctx, principal, "10")
		require.NoError(t, snapshotErr)
		require.NoError(t, service.Store.delete(ctx, bindingKey(principal, binding.Datasource), rev))
	}
	w := request(http.MethodPost, choose, `[{"table_name":"sales"}]`, token)
	require.NotEqual(t, http.StatusOK, w.Code)
	require.NotContains(t, w.Body.String(), "people")
	require.Equal(t, http.StatusNotFound, request(http.MethodGet, choices, "", token).Code)
}
