package chatbi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOriginalExistingDatasourceConnectionCheckUsesOnlyBoundGateway(t *testing.T) {
	ctx := context.Background()
	service, principal, gateway, _, token := serviceFixture(t)
	handler, err := NewHandler(service, "test")
	require.NoError(t, err)
	configuration, err := databaseDisplayConfiguration()
	require.NoError(t, err)
	requestBody := func(id, kind, config string) string {
		t.Helper()
		body, err := json.Marshal(map[string]any{"id": id, "type": kind, "configuration": config})
		require.NoError(t, err)
		return string(body)
	}
	path := "/api/v1/datasource/check"
	valid := requestBody("10", "pg", configuration)
	require.Equal(t, http.StatusUnauthorized, accountRequest(t, handler, http.MethodPost, path, valid, token).Code)
	require.Zero(t, gateway.schemaCalls)
	user, revision, err := service.Store.User(ctx, principal.User.ID)
	require.NoError(t, err)
	user.Admin = true
	require.NoError(t, service.Store.put(ctx, userKey(user.ID), user, revision))
	principal, err = service.Identity.Verify(ctx, token)
	require.NoError(t, err)
	require.Equal(t, http.StatusUnprocessableEntity,
		accountRequest(t, handler, http.MethodPost, path, requestBody("", "pg", configuration), token).Code)
	require.Equal(t, http.StatusUnprocessableEntity,
		accountRequest(t, handler, http.MethodPost, path, requestBody("10", "pg", "forged-host"), token).Code)
	require.Equal(t, http.StatusUnprocessableEntity,
		accountRequest(t, handler, http.MethodPost, path,
			requestBody("10", "pg", browserDatabaseMetadataConfig(t, "attacker-host")), token).Code)
	require.Equal(t, http.StatusUnprocessableEntity,
		accountRequest(t, handler, http.MethodPost, path, requestBody("10", "mysql", configuration), token).Code)
	require.Zero(t, gateway.schemaCalls, "browser connection settings must never probe a target")
	response := accountRequest(t, handler, http.MethodPost, path, valid, token)
	require.Equal(t, http.StatusOK, response.Code)
	require.Contains(t, response.Body.String(), `"data":true`)
	require.Equal(t, 1, gateway.schemaCalls)

	gateway.schemaErr = ErrGateway
	require.NotEqual(t, http.StatusOK, accountRequest(t, handler, http.MethodPost, path, valid, token).Code)
	gateway.schemaErr = nil
	gateway.afterSchema = func() {
		gateway.afterSchema = nil
		binding, currentRevision, _, err := service.bindingSnapshot(ctx, principal, "10")
		require.NoError(t, err)
		binding.Description = "changed during probe"
		require.NoError(t, service.Store.put(ctx, bindingKey(principal, "10"), binding, currentRevision))
	}
	require.NotEqual(t, http.StatusOK, accountRequest(t, handler, http.MethodPost, path, valid, token).Code,
		"a changed server binding must not return a successful connection check")
}
