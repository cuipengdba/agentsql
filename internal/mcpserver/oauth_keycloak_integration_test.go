package mcpserver

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/rbac"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// TestOAuthKeycloakIntegration is opt-in because it requires a real isolated
// Keycloak realm and a short-lived access token. The test transport only maps
// the Windows host's loopback listener into Docker; signature and claims are
// verified by the production verifier against the real OIDC discovery/JWKS.
func TestOAuthKeycloakIntegration(t *testing.T) {
	issuer := os.Getenv("AGENTSQL_TEST_OAUTH_ISSUER")
	subject := os.Getenv("AGENTSQL_TEST_OAUTH_SUBJECT")
	tokenFile := os.Getenv("AGENTSQL_TEST_OAUTH_TOKEN_FILE")
	if issuer == "" || subject == "" || tokenFile == "" {
		t.Skip("requires isolated Keycloak issuer, subject and access-token file")
	}
	tokenBytes, err := os.ReadFile(tokenFile)
	require.NoError(t, err)
	token := strings.TrimSpace(string(tokenBytes))
	require.NotEmpty(t, token)
	fixture := newMCPFixture(t, "readonly")
	ctx := context.Background()
	roles := rbac.NewService(fixture.runtime.Store.RBAC())
	require.NoError(t, roles.Bootstrap(ctx, "admin", "test-password"))
	user, err := fixture.runtime.Store.HumanAuth().CreateExternalIdentity(ctx, model.User{
		ID: "keycloak-user", TenantID: rbac.DefaultTenantID, Username: "keycloak-user", DisplayName: "Keycloak User",
	}, "oidc", subject)
	require.NoError(t, err)
	require.NoError(t, fixture.runtime.Store.RBAC().SetUserRoles(ctx, user.TenantID, user.ID, []string{rbac.OperatorRoleID}))
	cfg := statefulHTTPTestConfig(100)
	cfg.MCP.OAuth = config.MCPOAuthConfig{
		Enabled: true, IssuerURL: issuer, ResourceURL: "http://127.0.0.1:7780/mcp/oauth",
		TenantID: rbac.DefaultTenantID, RequiredScope: "agentsql:mcp",
	}
	cfg.Auth.OIDC = config.OIDCConfig{
		Enabled: true, IssuerURL: issuer, TenantID: rbac.DefaultTenantID, ClientID: "agentsql-inspector",
		RedirectURL:   "http://127.0.0.1:7780/api/v1/auth/oidc/callback",
		UsernameClaim: "preferred_username", GroupsClaim: "groups",
		GroupRoleMap: map[string][]string{"operators": {rbac.OperatorRoleID}},
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	client := &http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect forbidden") },
		Transport: &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if address == "127.0.0.1:8180" {
				address = "host.docker.internal:8180"
			}
			return dialer.DialContext(ctx, network, address)
		}},
	}
	verifier, err := newOAuthAccessVerifierWithClient(ctx, cfg.MCP.OAuth, fixture.runtime, client)
	require.NoError(t, err)
	principal, err := verifier.Verify(ctx, token, fixture.agent.ID)
	require.NoError(t, err)
	require.Equal(t, user.ID, principal.UserID)
	handler, err := NewHTTPHandler(fixture.runtime, cfg, zerolog.Nop(), WithOAuthAccessVerifier(verifier))
	require.NoError(t, err)
	request := modernRequest(fixture.handlers.apiKey, modernDiscoverCall())
	request.URL.Path = oauthMCPPath
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set(oauthAgentHeader, fixture.handlers.apiKey)
	request.Header.Set("Mcp-Method", "server/discover")
	request.Header.Del("Mcp-Name")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.NoError(t, fixture.runtime.Store.RBAC().SetUserRoles(ctx, user.TenantID, user.ID, nil))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request.Clone(ctx))
	require.Equal(t, http.StatusForbidden, response.Code, "live local role revocation must block an otherwise valid Keycloak token")
}
