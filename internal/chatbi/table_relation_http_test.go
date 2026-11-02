package chatbi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTableRelationOriginalRoutesAreScopedAuditedAndRevocable(t *testing.T) {
	ctx := context.Background()
	service, principal, base, _, token := serviceFixture(t)
	user, revision, err := service.Store.User(ctx, principal.User.ID)
	require.NoError(t, err)
	user.Admin = true
	require.NoError(t, service.Store.put(ctx, userKey(user.ID), user, revision))
	principal, err = service.Identity.Verify(ctx, token)
	require.NoError(t, err)
	columns := []Column{{Schema: "public", Table: "orders", Name: "buyer_id"},
		{Schema: "public", Table: "buyers", Name: "id"}}
	gateway := &selectionTestGateway{fakeGateway: base, columns: columns}
	service.Gateway = gateway
	handler, err := NewHandler(service, "test")
	require.NoError(t, err)
	request := func(path, body, credential string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		if credential != "" {
			r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+credential)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	orders := browserTableID("10", "public", "orders")
	buyers := browserTableID("10", "public", "buyers")
	graph := []map[string]any{
		{"id": orders, "shape": "er-rect", "position": map[string]any{"x": 10, "y": 20}},
		{"id": buyers, "shape": "er-rect", "position": map[string]any{"x": 200, "y": 20}},
		{"shape": "edge", "source": map[string]any{"cell": orders, "port": browserFieldID(orders, "buyer_id")},
			"target": map[string]any{"cell": buyers, "port": browserFieldID(buyers, "id")}},
	}
	raw, err := json.Marshal(graph)
	require.NoError(t, err)
	get := "/api/v1/table_relation/get/10"
	save := "/api/v1/table_relation/save/10"
	var auditsBefore int
	require.NoError(t, service.Store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM chatbi_objects WHERE kind='management-audit'`).Scan(&auditsBefore))
	require.Equal(t, http.StatusUnauthorized, request(save, string(raw), "").Code)
	require.Equal(t, http.StatusOK, request(get, "", token).Code)
	require.Equal(t, http.StatusUnprocessableEntity, request(save, `[{"shape":"edge"}]`, token).Code)
	require.Equal(t, http.StatusOK, request(save, string(raw), token).Code)
	var auditsAfter int
	require.NoError(t, service.Store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM chatbi_objects WHERE kind='management-audit'`).Scan(&auditsAfter))
	require.Equal(t, auditsBefore+1, auditsAfter)
	current, err := service.binding(ctx, principal, "10")
	require.NoError(t, err)
	require.NotEmpty(t, current.TableRelations)
	require.Contains(t, request(get, "", token).Body.String(), `"shape":"edge"`)

	other := testUser(t, "2", "other-relation-admin")
	other.Admin = true
	require.NoError(t, service.Store.CreateUser(ctx, other))
	_, otherToken, err := service.Identity.Login(ctx, other.Account, "Synthetic-password-1")
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, request(get, "", otherToken).Code)
	require.Equal(t, http.StatusNotFound, request(save, string(raw), otherToken).Code)
	_, err = service.Store.db.ExecContext(ctx, `CREATE TRIGGER reject_relation_audit BEFORE INSERT ON chatbi_objects
		WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(FAIL, 'audit offline'); END`)
	require.NoError(t, err)
	require.NotEqual(t, http.StatusOK, request(save, `[]`, token).Code)
	unchanged, err := service.binding(ctx, principal, "10")
	require.NoError(t, err)
	require.Equal(t, current.TableRelations, unchanged.TableRelations,
		"audit failure must roll back the binding update")
	_, err = service.Store.db.ExecContext(ctx, `DROP TRIGGER reject_relation_audit`)
	require.NoError(t, err)

	gateway.columns = columns[:1]
	require.Equal(t, http.StatusForbidden, request(get, "", token).Code,
		"revoking a related table must fail closed")
	require.Equal(t, http.StatusUnprocessableEntity, request(save, string(raw), token).Code)
	gateway.columns = columns
	current.SelectedTables = "!"
	_, revision, _, err = service.bindingSnapshot(ctx, principal, "10")
	require.NoError(t, err)
	require.NoError(t, service.Store.put(ctx, bindingKey(principal, "10"), current, revision))
	require.Equal(t, http.StatusForbidden, request(get, "", token).Code)
}
