package chatbi

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

type scopedHistoryGateway struct {
	*fakeGateway
	denied ID
	outage ID
}

func (gateway *scopedHistoryGateway) Schema(ctx context.Context, principal Principal, binding Binding) ([]Column, error) {
	if binding.Datasource == gateway.denied {
		return nil, ErrGatewaySourceDenied
	}
	if binding.Datasource == gateway.outage {
		return nil, ErrGateway
	}
	return gateway.fakeGateway.Schema(ctx, principal, binding)
}

func TestChatListHidesOnlyRevokedGatewaySource(t *testing.T) {
	ctx := context.Background()
	service, principal, gateway, _, token := serviceFixture(t)
	binding, err := service.binding(ctx, principal, "10")
	require.NoError(t, err)
	other := binding
	other.Datasource, other.Name = "11", "still authorized"
	require.NoError(t, service.Store.put(ctx, bindingKey(principal, other.Datasource), other, 0))
	deniedChat, err := service.Start(ctx, principal, "10", "revoked source", 0)
	require.NoError(t, err)
	_, err = service.Start(ctx, principal, "11", "live source", 0)
	require.NoError(t, err)
	scoped := &scopedHistoryGateway{fakeGateway: gateway, denied: "10"}
	service.Gateway = scoped
	chats, err := service.Chats(ctx, principal)
	require.NoError(t, err)
	require.Len(t, chats, 1)
	require.Equal(t, "live source", chats[0].Brief)
	require.Empty(t, chats[0].Records)
	_, err = service.ReadChat(ctx, principal, deniedChat.ID, false)
	require.Error(t, err, "revoked source must not be readable directly")
	require.Zero(t, gateway.queries)

	handler, err := NewHandler(service, "test")
	require.NoError(t, err)
	response := accountRequest(t, handler, http.MethodGet, "/api/v1/chat/list", "", token)
	require.Equal(t, http.StatusOK, response.Code)
	require.NotContains(t, response.Body.String(), "revoked source")
	require.Contains(t, response.Body.String(), "live source")

	scoped.outage = "11"
	_, err = service.Chats(ctx, principal)
	require.True(t, errors.Is(err, ErrGateway), "gateway outage for a live source must fail closed: %v", err)
	require.NotEqual(t, http.StatusOK, accountRequest(t, handler, http.MethodGet, "/api/v1/chat/list", "", token).Code)
}

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
	_, err = service.Start(ctx, principal, "11", "still authorized", 0)
	require.NoError(t, err)
	handler, err := NewHandler(service, "test")
	require.NoError(t, err)
	listPath := "/api/v1/chat/list"
	before := accountRequest(t, handler, http.MethodGet, listPath, "", token)
	require.Equal(t, http.StatusOK, before.Code)
	require.Contains(t, before.Body.String(), "before deletion")
	require.Contains(t, before.Body.String(), "still authorized")
	gateway.schemaErr = ErrGateway
	require.NotEqual(t, http.StatusOK,
		accountRequest(t, handler, http.MethodGet, listPath, "", token).Code,
		"list must fail closed when the live gateway schema is unavailable")
	gateway.schemaErr = nil
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
	after := accountRequest(t, handler, http.MethodGet, listPath, "", token)
	require.Equal(t, http.StatusOK, after.Code)
	require.NotContains(t, after.Body.String(), "before deletion")
	require.Contains(t, after.Body.String(), "still authorized")
	require.ErrorIs(t, service.Store.ConfigureConnections(ctx, ConnectionConfig{Bindings: []Binding{binding}}), ErrConflict,
		"a retired datasource ID must not be rebound to another database and expose old chats")
	check := func(ctx context.Context) error { _, err := service.Identity.Verify(ctx, token); return err }
	_, err = service.Ask(ctx, principal, chat.ID, "after deletion", check, nil)
	require.Error(t, err, "old conversations cannot query a removed datasource")
	require.Equal(t, 0, gateway.queries)
}
