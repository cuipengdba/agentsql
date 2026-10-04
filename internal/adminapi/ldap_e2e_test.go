package adminapi

import (
	"context"
	"os"
	"testing"

	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/stretchr/testify/require"
)

// TestLDAPServiceE2E is opt-in so CI can point it at either OpenLDAP or AD
// without embedding directory credentials in the repository.
func TestLDAPServiceE2E(t *testing.T) {
	endpoint := os.Getenv("AGENTSQL_LDAP_TEST_URL")
	if endpoint == "" {
		t.Skip("AGENTSQL_LDAP_TEST_URL is not configured")
	}
	bindDN, bindPasswordEnv := os.Getenv("AGENTSQL_LDAP_TEST_BIND_DN"), ""
	if bindDN != "" {
		bindPasswordEnv = "AGENTSQL_LDAP_TEST_BIND_PASSWORD"
	}
	value := config.LDAPConfig{
		Enabled: true, URL: endpoint, StartTLS: os.Getenv("AGENTSQL_LDAP_TEST_STARTTLS") == "1",
		CAFile: os.Getenv("AGENTSQL_LDAP_TEST_CA_FILE"), ServerName: os.Getenv("AGENTSQL_LDAP_TEST_SERVER_NAME"), BindDN: bindDN,
		BindPasswordEnv: bindPasswordEnv, UserBaseDN: os.Getenv("AGENTSQL_LDAP_TEST_USER_BASE_DN"),
		UserFilter: os.Getenv("AGENTSQL_LDAP_TEST_USER_FILTER"), UsernameAttribute: os.Getenv("AGENTSQL_LDAP_TEST_USERNAME_ATTRIBUTE"),
		GroupBaseDN: os.Getenv("AGENTSQL_LDAP_TEST_GROUP_BASE_DN"), GroupFilter: os.Getenv("AGENTSQL_LDAP_TEST_GROUP_FILTER"),
		GroupNameAttribute: os.Getenv("AGENTSQL_LDAP_TEST_GROUP_ATTRIBUTE"), TenantID: "tenant_test",
		GroupRoleMap: map[string][]string{"test": {"role_test"}},
	}
	require.NoError(t, (config.AuthConfig{LDAP: value}).Validate())
	authenticator, err := newLDAPAuthenticator(value)
	require.NoError(t, err)
	identity, err := authenticator.authenticate(context.Background(), os.Getenv("AGENTSQL_LDAP_TEST_USERNAME"), os.Getenv("AGENTSQL_LDAP_TEST_PASSWORD"))
	require.NoError(t, err)
	require.NotEmpty(t, identity.Subject)
	require.NotEmpty(t, identity.Username)
	require.NotEmpty(t, identity.Groups)
}
