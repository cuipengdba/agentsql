package config

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMCPOAuthConfigRejectsAmbiguousResourceAndIssuer(t *testing.T) {
	valid := MCPOAuthConfig{
		Enabled: true, IssuerURL: "https://id.example.test/realms/agentsql",
		ResourceURL: "https://mcp.example.test/mcp/oauth", TenantID: "tenant_default", RequiredScope: "agentsql:mcp",
	}
	require.NoError(t, valid.Validate())
	local := valid
	local.IssuerURL = "http://127.0.0.1:8080/realms/test"
	local.ResourceURL = "http://127.0.0.1:7780/mcp/oauth"
	require.NoError(t, local.Validate())
	for _, item := range []struct {
		name string
		edit func(*MCPOAuthConfig)
	}{
		{"missing tenant", func(value *MCPOAuthConfig) { value.TenantID = "" }},
		{"remote HTTP issuer", func(value *MCPOAuthConfig) { value.IssuerURL = "http://id.example.test" }},
		{"wrong resource path", func(value *MCPOAuthConfig) { value.ResourceURL = "https://mcp.example.test/mcp" }},
		{"query on resource", func(value *MCPOAuthConfig) { value.ResourceURL += "?token=bad" }},
		{"resource credentials", func(value *MCPOAuthConfig) { value.ResourceURL = "https://user@host.test/mcp/oauth" }},
		{"multiple scopes", func(value *MCPOAuthConfig) { value.RequiredScope = "agentsql:mcp other" }},
	} {
		t.Run(item.name, func(t *testing.T) {
			candidate := valid
			item.edit(&candidate)
			require.Error(t, candidate.Validate())
		})
	}
	require.Equal(t, "agentsql:mcp", (Config{}).EffectiveMCP().OAuth.RequiredScope)
}

func TestMCPOAuthRequiresMatchingConfiguredOIDCIdentitySource(t *testing.T) {
	base, err := Parse([]byte(fmt.Sprintf(validConfig, filepath.ToSlash(filepath.Join(t.TempDir(), "agentsql.db")))))
	require.NoError(t, err)
	base.MCP.OAuth = MCPOAuthConfig{
		Enabled: true, IssuerURL: "https://id.example.test", ResourceURL: "https://mcp.example.test/mcp/oauth",
		TenantID: "tenant_default", RequiredScope: "agentsql:mcp",
	}
	require.ErrorContains(t, base.Validate(), "must match enabled")
	base.Auth.OIDC = OIDCConfig{
		Enabled: true, IssuerURL: "https://other.example.test", ClientID: "agentsql",
		RedirectURL: "https://mcp.example.test/api/v1/auth/oidc/callback", TenantID: "tenant_default",
		UsernameClaim: "preferred_username", GroupsClaim: "groups",
		GroupRoleMap: map[string][]string{"operators": {"role_default_operator"}},
	}
	require.ErrorContains(t, base.Validate(), "must match enabled")
	base.Auth.OIDC.IssuerURL = base.MCP.OAuth.IssuerURL
	base.Auth.OIDC.TenantID = "other-tenant"
	require.ErrorContains(t, base.Validate(), "must match enabled")
	base.Auth.OIDC.TenantID = base.MCP.OAuth.TenantID
	require.NoError(t, base.Validate())
}
