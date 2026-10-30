package chatbi

import (
	"context"
	"crypto/aes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func browserDatabaseMetadataConfig(t *testing.T, host string) string {
	t.Helper()
	plain, err := json.Marshal(map[string]any{
		"host": host, "port": 1, "username": "AGENTSQL_GATEWAY_MANAGED", "password": "",
		"database": "AGENTSQL_GATEWAY_MANAGED", "dbSchema": "AGENTSQL_GATEWAY_MANAGED",
		"extraJdbc": "", "filename": "", "sheets": []any{}, "mode": "service_name",
		"timeout": 30, "lowVersion": true, "ssl": false, "poolSize": 5, "engine": "hive",
	})
	require.NoError(t, err)
	block, err := aes.NewCipher([]byte("SQLBot1234567890"))
	require.NoError(t, err)
	padding := aes.BlockSize - len(plain)%aes.BlockSize
	for range padding {
		plain = append(plain, byte(padding))
	}
	encoded := make([]byte, len(plain))
	for offset := 0; offset < len(plain); offset += aes.BlockSize {
		block.Encrypt(encoded[offset:offset+aes.BlockSize], plain[offset:offset+aes.BlockSize])
	}
	return base64.StdEncoding.EncodeToString(encoded)
}

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
	require.Equal(t, http.StatusOK, accountRequest(t, handler, http.MethodPost, path,
		requestBody("10", "pg", browserDatabaseMetadataConfig(t, "AGENTSQL_GATEWAY_MANAGED")), token).Code,
		"SQLBot v1.10.3 edit form adds a harmless engine=hive field")

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

func TestOriginalExistingDatasourceSchemaDiscoveryUsesCurrentGateway(t *testing.T) {
	ctx := context.Background()
	service, principal, base, _, token := serviceFixture(t)
	user, revision, err := service.Store.User(ctx, principal.User.ID)
	require.NoError(t, err)
	user.Admin = true
	require.NoError(t, service.Store.put(ctx, userKey(user.ID), user, revision))
	principal, err = service.Identity.Verify(ctx, token)
	require.NoError(t, err)
	gateway := &selectionTestGateway{fakeGateway: base, columns: []Column{
		{Schema: "reports", Table: "monthly", Name: "amount"},
		{Schema: "public", Table: "sales", Name: "amount"},
		{Schema: "public", Table: "sales", Name: "region"},
	}}
	service.Gateway = gateway
	handler, err := NewHandler(service, "test")
	require.NoError(t, err)
	configuration, err := databaseDisplayConfiguration()
	require.NoError(t, err)
	body, err := json.Marshal(map[string]any{"id": "10", "type": "pg", "configuration": configuration})
	require.NoError(t, err)
	path := "/api/v1/datasource/getSchemaByConf"
	require.Equal(t, http.StatusUnprocessableEntity,
		accountRequest(t, handler, http.MethodPost, path, `{"type":"pg"}`, token).Code)
	require.Equal(t, http.StatusUnprocessableEntity,
		accountRequest(t, handler, http.MethodPost, path,
			`{"id":"10","type":"pg","configuration":"forged"}`, token).Code)
	require.Zero(t, gateway.schemaCalls)
	response := accountRequest(t, handler, http.MethodPost, path, string(body), token)
	require.Equal(t, http.StatusOK, response.Code)
	require.Contains(t, response.Body.String(), `"data":["public","reports"]`)
	require.NotContains(t, response.Body.String(), "AGENTSQL_GATEWAY_MANAGED")
	require.Equal(t, 1, gateway.schemaCalls)

	gateway.schemaErr = ErrGateway
	require.NotEqual(t, http.StatusOK, accountRequest(t, handler, http.MethodPost, path, string(body), token).Code)
	gateway.schemaErr = nil
	gateway.afterSchema = func() {
		gateway.afterSchema = nil
		binding, currentRevision, _, err := service.bindingSnapshot(ctx, principal, "10")
		require.NoError(t, err)
		binding.Description = "changed during discovery"
		require.NoError(t, service.Store.put(ctx, bindingKey(principal, "10"), binding, currentRevision))
	}
	require.NotEqual(t, http.StatusOK, accountRequest(t, handler, http.MethodPost, path, string(body), token).Code)
}
