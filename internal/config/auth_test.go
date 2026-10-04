package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAuthConfigFailClosedValidation(t *testing.T) {
	validOIDC := OIDCConfig{Enabled: true, IssuerURL: "https://id.example.test", ClientID: "agentsql",
		RedirectURL: "https://agentsql.example.test/api/v1/auth/oidc/callback", TenantID: "tenant_default",
		UsernameClaim: "preferred_username", GroupsClaim: "groups", GroupRoleMap: map[string][]string{"admins": {"role_default_admin"}}}
	require.NoError(t, (AuthConfig{MFA: MFAConfig{Enabled: true, Issuer: "AgentSQL"}, OIDC: validOIDC}).Validate())

	plaintextLDAP := LDAPConfig{Enabled: true, URL: "ldap://directory.example.test:389", UserBaseDN: "dc=example,dc=test",
		UserFilter: "(uid={username})", GroupBaseDN: "dc=example,dc=test", GroupFilter: "(member={user_dn})",
		TenantID: "tenant_default", GroupRoleMap: map[string][]string{"admins": {"role_default_admin"}}}
	require.ErrorContains(t, (AuthConfig{LDAP: plaintextLDAP}).Validate(), "start_tls=true")

	badOIDC := validOIDC
	badOIDC.IssuerURL = "http://id.example.test"
	require.ErrorContains(t, (AuthConfig{OIDC: badOIDC}).Validate(), "HTTPS")

	badLDAP := plaintextLDAP
	badLDAP.StartTLS = true
	badLDAP.UserFilter = "(uid=admin)"
	require.ErrorContains(t, (AuthConfig{LDAP: badLDAP}).Validate(), "{username}")
}
