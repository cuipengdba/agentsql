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

func TestOriginalDatabaseDatasourceMetadataUpdateKeepsGatewayBindingAndAudit(t *testing.T) {
	ctx := context.Background()
	service, principal, _, _, token := serviceFixture(t)
	user, revision, err := service.Store.User(ctx, principal.User.ID)
	require.NoError(t, err)
	user.Admin = true
	require.NoError(t, service.Store.put(ctx, userKey(user.ID), user, revision))
	principal, err = service.Identity.Verify(ctx, token)
	require.NoError(t, err)
	prior, bindingRevision, _, err := service.bindingSnapshot(ctx, principal, "10")
	require.NoError(t, err)
	prior.Configuration = "server-owned-database-config"
	require.NoError(t, service.Store.put(ctx, bindingKey(principal, "10"), prior, bindingRevision))
	other := prior
	other.Datasource, other.Name = "11", "Other database"
	require.NoError(t, service.Store.put(ctx, bindingKey(principal, "11"), other, 0))
	handler, err := NewHandler(service, "test")
	require.NoError(t, err)
	config := browserDatabaseMetadataConfig(t, "AGENTSQL_GATEWAY_MANAGED")
	require.True(t, validDatabaseMetadataConfiguration(config))
	require.False(t, validDatabaseMetadataConfiguration(browserDatabaseMetadataConfig(t, "attacker-host")))
	request := func(credential, name, description, kind, wire string) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(map[string]any{"id": 10, "name": name, "description": description,
			"type": kind, "configuration": wire})
		require.NoError(t, err)
		r := httptest.NewRequest(http.MethodPost, "/api/v1/datasource/update", strings.NewReader(string(body)))
		if credential != "" {
			r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+credential)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	assertOriginal := func() {
		t.Helper()
		current, err := service.binding(ctx, principal, "10")
		require.NoError(t, err)
		require.Equal(t, prior.Name, current.Name)
		require.Equal(t, prior.BindingID, current.BindingID)
		require.Equal(t, prior.Configuration, current.Configuration)
	}
	require.Equal(t, http.StatusUnauthorized, request("", "Renamed", "New description", "pg", config).Code)
	foreign := testUser(t, "2", "foreign-db-admin")
	foreign.Admin = true
	require.NoError(t, service.Store.CreateUser(ctx, foreign))
	_, foreignToken, err := service.Identity.Login(ctx, foreign.Account, "Synthetic-password-1")
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, request(foreignToken, "Renamed", "New description", "pg", config).Code)
	require.Equal(t, http.StatusConflict, request(token, other.Name, "New description", "pg", config).Code)
	require.Equal(t, http.StatusUnprocessableEntity, request(token, "Renamed", "New description", "mysql", config).Code)
	require.Equal(t, http.StatusUnprocessableEntity, request(token, "Renamed", "New description", "pg", browserDatabaseMetadataConfig(t, "attacker-host")).Code)
	assertOriginal()
	_, err = service.Store.db.ExecContext(ctx, `CREATE TRIGGER reject_database_metadata_audit BEFORE INSERT ON chatbi_objects
		WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(FAIL, 'audit offline'); END`)
	require.NoError(t, err)
	require.NotEqual(t, http.StatusOK, request(token, "Renamed", "New description", "pg", config).Code)
	assertOriginal()
	_, err = service.Store.db.ExecContext(ctx, `DROP TRIGGER reject_database_metadata_audit`)
	require.NoError(t, err)
	response := request(token, "Renamed", "New description", "pg", config)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.NotContains(t, response.Body.String(), prior.Configuration)
	require.NotContains(t, response.Body.String(), prior.ConnectorToken)
	current, err := service.binding(ctx, principal, "10")
	require.NoError(t, err)
	require.Equal(t, "Renamed", current.Name)
	require.Equal(t, "New description", current.Description)
	require.Equal(t, prior.Name, current.ResolveName, "gateway mapping must keep the original name")
	require.Equal(t, prior.BindingID, current.BindingID)
	require.Equal(t, prior.Configuration, current.Configuration)
	require.Equal(t, prior.ConnectorToken, current.ConnectorToken)
	require.Equal(t, prior.AgentKey, current.AgentKey)
	require.Equal(t, http.StatusOK, request(token, "Renamed again", "Changed", "pg", config).Code)
	current, err = service.binding(ctx, principal, "10")
	require.NoError(t, err)
	require.Equal(t, prior.Name, current.ResolveName)
	audits, err := service.Store.list(ctx, "management-audit", "system", "system")
	require.NoError(t, err)
	count := 0
	for _, raw := range audits {
		var entry struct {
			Action string `json:"action"`
		}
		require.NoError(t, json.Unmarshal(raw, &entry))
		if entry.Action == "database.datasource-update" {
			count++
		}
	}
	require.Equal(t, 2, count)
}
