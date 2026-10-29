package chatbi

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDatabaseDatasourceDeleteRemovesOnlyOwnerAliasAfterAudit(t *testing.T) {
	ctx := context.Background()
	service, principal, gateway, _, token := serviceFixture(t)
	user, revision, err := service.Store.User(ctx, principal.User.ID)
	require.NoError(t, err)
	user.Admin = true
	require.NoError(t, service.Store.put(ctx, userKey(user.ID), user, revision))
	principal, err = service.Identity.Verify(ctx, token)
	require.NoError(t, err)
	binding, err := service.binding(ctx, principal, "10")
	require.NoError(t, err)
	other := binding
	other.Datasource, other.Name = "11", "other"
	require.NoError(t, service.Store.put(ctx, bindingKey(principal, "11"), other, 0))
	chat, err := service.Start(ctx, principal, "10", "before deletion", 0)
	require.NoError(t, err)
	handler, err := NewHandler(service, "test")
	require.NoError(t, err)
	path := "/api/v1/datasource/delete/10/fixture"
	require.Equal(t, http.StatusUnauthorized,
		accountRequest(t, handler, http.MethodPost, path, `{}`, "").Code)
	require.Equal(t, http.StatusNotFound,
		accountRequest(t, handler, http.MethodPost, "/api/v1/datasource/delete/10/wrong", `{}`, token).Code)
	require.Equal(t, http.StatusNotFound,
		accountRequest(t, handler, http.MethodPost, "/api/v1/datasource/delete/11/fixture", `{}`, token).Code)
	user, revision, err = service.Store.User(ctx, principal.User.ID)
	require.NoError(t, err)
	user.Admin = false
	require.NoError(t, service.Store.put(ctx, userKey(user.ID), user, revision))
	require.NotEqual(t, http.StatusOK,
		accountRequest(t, handler, http.MethodPost, path, `{}`, token).Code)
	user, revision, err = service.Store.User(ctx, principal.User.ID)
	require.NoError(t, err)
	user.Admin = true
	require.NoError(t, service.Store.put(ctx, userKey(user.ID), user, revision))
	_, err = service.Store.db.ExecContext(ctx, `CREATE TRIGGER reject_database_delete_audit BEFORE INSERT ON chatbi_objects
		WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(FAIL, 'audit offline'); END`)
	require.NoError(t, err)
	require.NotEqual(t, http.StatusOK,
		accountRequest(t, handler, http.MethodPost, path, `{}`, token).Code)
	_, err = service.binding(ctx, principal, "10")
	require.NoError(t, err, "failed audit must preserve the binding")
	_, err = service.Store.db.ExecContext(ctx, `DROP TRIGGER reject_database_delete_audit`)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK,
		accountRequest(t, handler, http.MethodPost, path, `{}`, token).Code)
	_, err = service.binding(ctx, principal, "10")
	require.ErrorIs(t, err, ErrNotFound)
	_, err = service.binding(ctx, principal, "11")
	require.NoError(t, err, "another alias must remain available")
	require.ErrorIs(t, service.Store.ConfigureConnections(ctx, ConnectionConfig{Bindings: []Binding{binding}}), ErrConflict,
		"a retired datasource ID must not be rebound to another database and expose old chats")
	check := func(ctx context.Context) error { _, err := service.Identity.Verify(ctx, token); return err }
	_, err = service.Ask(ctx, principal, chat.ID, "after deletion", check, nil)
	require.Error(t, err, "old conversations cannot query a removed datasource")
	require.Equal(t, 0, gateway.queries)
}
