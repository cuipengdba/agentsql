package mcpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/rbac"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestOAuthMCPRequiresIndependentHumanAndAgentCredentials(t *testing.T) {
	fixture := newMCPFixture(t, "readonly")
	cfg := statefulHTTPTestConfig(100)
	cfg.MCP.OAuth = config.MCPOAuthConfig{
		Enabled: true, IssuerURL: "https://id.example.test", ResourceURL: "http://127.0.0.1:7780/mcp/oauth",
		TenantID: fixture.agent.TenantID, RequiredScope: "agentsql:mcp",
	}
	cfg.Auth.OIDC = config.OIDCConfig{
		Enabled: true, IssuerURL: cfg.MCP.OAuth.IssuerURL, TenantID: fixture.agent.TenantID,
		ClientID: "agentsql-console", RedirectURL: "https://mcp.example.test/api/v1/auth/oidc/callback",
		UsernameClaim: "preferred_username", GroupsClaim: "groups",
		GroupRoleMap: map[string][]string{"operators": {rbac.OperatorRoleID}},
	}
	verifier := &testHumanVerifier{principal: rbac.Principal{TenantID: fixture.agent.TenantID, UserID: "human-1"}}
	handler, err := NewHTTPHandler(fixture.runtime, cfg, zerolog.Nop(), WithOAuthAccessVerifier(verifier))
	require.NoError(t, err)
	metadata := httptest.NewRecorder()
	handler.ServeHTTP(metadata, httptest.NewRequest(http.MethodGet, oauthMetadataPath, nil))
	require.Equal(t, http.StatusOK, metadata.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(metadata.Body.Bytes(), &body))
	require.Equal(t, cfg.MCP.OAuth.ResourceURL, body["resource"])
	require.Equal(t, []any{cfg.MCP.OAuth.IssuerURL}, body["authorization_servers"])

	request := func(agentKey, bearer string) *http.Request {
		result := modernRequest(fixture.handlers.apiKey, modernDiscoverCall())
		result.URL.Path = oauthMCPPath
		result.Header.Set("Mcp-Method", "server/discover")
		result.Header.Del("Mcp-Name")
		result.Header.Del("Authorization")
		if agentKey != "" {
			result.Header.Set(oauthAgentHeader, agentKey)
		}
		if bearer != "" {
			result.Header.Set("Authorization", bearer)
		}
		return result
	}
	serve := func(request *http.Request) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}
	missingBoth := serve(request("", ""))
	require.Equal(t, http.StatusUnauthorized, missingBoth.Code)
	require.Contains(t, missingBoth.Header().Get("WWW-Authenticate"), oauthMetadataURL(cfg.MCP.OAuth.ResourceURL))
	require.Equal(t, http.StatusForbidden, serve(request("", "Bearer human-token")).Code)
	require.Equal(t, http.StatusForbidden, serve(request("wrong-agent", "Bearer human-token")).Code)
	require.Equal(t, http.StatusUnauthorized, serve(request(fixture.handlers.apiKey, "")).Code)
	require.Equal(t, http.StatusUnauthorized, serve(request(fixture.handlers.apiKey, "Bearer wrong-human")).Code)
	withLegacyHuman := request(fixture.handlers.apiKey, "Bearer human-token")
	withLegacyHuman.Header.Set(humanDelegationHeader, "Bearer human-token")
	require.Equal(t, http.StatusUnauthorized, serve(withLegacyHuman).Code)
	withQueryToken := request(fixture.handlers.apiKey, "Bearer human-token")
	withQueryToken.URL.RawQuery = "access_token=human-token"
	require.Equal(t, http.StatusUnauthorized, serve(withQueryToken).Code)
	require.Equal(t, http.StatusOK, serve(request(fixture.handlers.apiKey, "Bearer human-token")).Code)
	require.Equal(t, 2, verifier.calls, "valid and invalid human tokens must reach verifier; missing credentials must not")

	legacy := modernRequest(fixture.handlers.apiKey, modernDiscoverCall())
	legacy.Header.Set("Mcp-Method", "server/discover")
	legacy.Header.Del("Mcp-Name")
	require.Equal(t, http.StatusOK, serve(legacy).Code, "existing modern Agent Key endpoint stays compatible")
	verifier.principal.TenantID = "wrong-tenant"
	require.Equal(t, http.StatusUnauthorized, serve(request(fixture.handlers.apiKey, "Bearer human-token")).Code)
}

func TestOAuthMCPDisabledHasNoProtectedResource(t *testing.T) {
	fixture := newMCPFixture(t, "readonly")
	handler, err := NewHTTPHandler(fixture.runtime, statefulHTTPTestConfig(100), zerolog.Nop())
	require.NoError(t, err)
	for _, path := range []string{oauthMCPPath, oauthMetadataPath} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, strings.NewReader("")))
		require.Equal(t, http.StatusNotFound, response.Code)
	}
}
