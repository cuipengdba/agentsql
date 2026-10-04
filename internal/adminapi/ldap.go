package adminapi

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/go-ldap/ldap/v3"
)

type ldapAuthenticator struct {
	config       config.LDAPConfig
	tlsConfig    *tls.Config
	bindPassword string
}

type ldapIdentity struct {
	Subject, Username string
	Groups            []string
}

func newLDAPAuthenticator(value config.LDAPConfig) (*ldapAuthenticator, error) {
	parsed, err := url.Parse(value.URL)
	if err != nil {
		return nil, err
	}
	serverName := value.ServerName
	if serverName == "" {
		serverName = parsed.Hostname()
	}
	if net.ParseIP(serverName) == nil && strings.TrimSpace(serverName) == "" {
		return nil, errors.New("LDAP TLS server name is required")
	}
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	if value.CAFile != "" {
		contents, readErr := os.ReadFile(value.CAFile)
		if readErr != nil {
			return nil, fmt.Errorf("read LDAP CA file: %w", readErr)
		}
		if !roots.AppendCertsFromPEM(contents) {
			return nil, errors.New("LDAP CA file contains no certificates")
		}
	}
	authenticator := &ldapAuthenticator{config: value, tlsConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverName, RootCAs: roots}}
	if value.BindPasswordEnv != "" {
		authenticator.bindPassword = os.Getenv(value.BindPasswordEnv)
		if authenticator.bindPassword == "" {
			return nil, fmt.Errorf("environment variable %s is required", value.BindPasswordEnv)
		}
	}
	return authenticator, nil
}

func (handler *Handler) ldapLogin(writer http.ResponseWriter, request *http.Request) {
	if handler.ldap == nil {
		handler.fail(writer, http.StatusNotFound, "not found")
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if decodeJSON(writer, request, &input) != nil || strings.TrimSpace(input.Username) == "" || input.Password == "" || len(input.Username) > 256 || len(input.Password) > 4096 {
		handler.fail(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	identity, err := handler.ldap.authenticate(request.Context(), input.Username, input.Password)
	if err != nil {
		handler.logger.Warn().Str("error_type", fmt.Sprintf("%T", err)).Msg("LDAP authentication rejected")
		handler.fail(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	principal, err := handler.externalPrincipal(request.Context(), "ldap", identity.Subject, identity.Username, identity.Groups,
		handler.ldap.config.TenantID, handler.ldap.config.AutoProvision, handler.ldap.config.GroupRoleMap)
	if err != nil {
		handler.fail(writer, http.StatusUnauthorized, "LDAP identity is not authorized")
		return
	}
	handler.issueSession(writer, request, principal)
}

func (authenticator *ldapAuthenticator) authenticate(ctx context.Context, username, password string) (ldapIdentity, error) {
	searchConnection, err := authenticator.connect(ctx)
	if err != nil {
		return ldapIdentity{}, err
	}
	defer searchConnection.Close()
	if authenticator.config.BindDN != "" {
		if err = searchConnection.Bind(authenticator.config.BindDN, authenticator.bindPassword); err != nil {
			return ldapIdentity{}, errors.New("LDAP service bind failed")
		}
	}
	filter := strings.ReplaceAll(authenticator.config.UserFilter, "{username}", ldap.EscapeFilter(username))
	usernameAttribute := authenticator.config.UsernameAttribute
	if usernameAttribute == "" {
		usernameAttribute = "uid"
	}
	result, err := searchConnection.Search(ldap.NewSearchRequest(authenticator.config.UserBaseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 2, 10, false, filter, []string{usernameAttribute}, nil))
	if err != nil || len(result.Entries) != 1 {
		return ldapIdentity{}, errors.New("LDAP user lookup did not return exactly one entry")
	}
	entry := result.Entries[0]
	canonicalUsername := strings.TrimSpace(entry.GetAttributeValue(usernameAttribute))
	if canonicalUsername == "" {
		canonicalUsername = username
	}
	userConnection, err := authenticator.connect(ctx)
	if err != nil {
		return ldapIdentity{}, err
	}
	defer userConnection.Close()
	if err = userConnection.Bind(entry.DN, password); err != nil {
		return ldapIdentity{}, errors.New("LDAP user bind failed")
	}
	groupFilter := strings.ReplaceAll(authenticator.config.GroupFilter, "{user_dn}", ldap.EscapeFilter(entry.DN))
	groupFilter = strings.ReplaceAll(groupFilter, "{username}", ldap.EscapeFilter(canonicalUsername))
	groupAttribute := authenticator.config.GroupNameAttribute
	if groupAttribute == "" {
		groupAttribute = "cn"
	}
	groupsResult, err := searchConnection.Search(ldap.NewSearchRequest(authenticator.config.GroupBaseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 1000, 10, false, groupFilter, []string{groupAttribute}, nil))
	if err != nil {
		return ldapIdentity{}, errors.New("LDAP group lookup failed")
	}
	groups := make([]string, 0, len(groupsResult.Entries))
	for _, group := range groupsResult.Entries {
		if name := strings.TrimSpace(group.GetAttributeValue(groupAttribute)); name != "" {
			groups = append(groups, name)
		}
	}
	if len(groups) == 0 {
		return ldapIdentity{}, errors.New("LDAP user has no groups")
	}
	return ldapIdentity{Subject: entry.DN, Username: canonicalUsername, Groups: groups}, nil
}

func (authenticator *ldapAuthenticator) connect(ctx context.Context) (*ldap.Conn, error) {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	connection, err := ldap.DialURL(authenticator.config.URL, ldap.DialWithDialer(dialer), ldap.DialWithTLSConfig(authenticator.tlsConfig.Clone()))
	if err != nil {
		return nil, fmt.Errorf("connect LDAP: %w", err)
	}
	connection.SetTimeout(10 * time.Second)
	if authenticator.config.StartTLS {
		if err = connection.StartTLS(authenticator.tlsConfig.Clone()); err != nil {
			connection.Close()
			return nil, fmt.Errorf("start LDAP TLS: %w", err)
		}
	}
	select {
	case <-ctx.Done():
		connection.Close()
		return nil, ctx.Err()
	default:
	}
	return connection, nil
}
