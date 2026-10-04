package config

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// AuthConfig controls human control-plane authentication. Every provider is
// opt-in; an omitted auth section preserves local password authentication.
type AuthConfig struct {
	MFA  MFAConfig  `yaml:"mfa"`
	OIDC OIDCConfig `yaml:"oidc"`
	LDAP LDAPConfig `yaml:"ldap"`
}

type MFAConfig struct {
	Enabled bool   `yaml:"enabled"`
	Issuer  string `yaml:"issuer"`
}

type OIDCConfig struct {
	Enabled         bool                `yaml:"enabled"`
	IssuerURL       string              `yaml:"issuer_url"`
	ClientID        string              `yaml:"client_id"`
	ClientSecretEnv string              `yaml:"client_secret_env"`
	RedirectURL     string              `yaml:"redirect_url"`
	PostLogoutURL   string              `yaml:"post_logout_url"`
	TenantID        string              `yaml:"tenant_id"`
	UsernameClaim   string              `yaml:"username_claim"`
	GroupsClaim     string              `yaml:"groups_claim"`
	Scopes          []string            `yaml:"scopes"`
	AutoProvision   bool                `yaml:"auto_provision"`
	GroupRoleMap    map[string][]string `yaml:"group_role_map"`
}

type LDAPConfig struct {
	Enabled            bool                `yaml:"enabled"`
	URL                string              `yaml:"url"`
	StartTLS           bool                `yaml:"start_tls"`
	CAFile             string              `yaml:"ca_file"`
	ServerName         string              `yaml:"server_name"`
	BindDN             string              `yaml:"bind_dn"`
	BindPasswordEnv    string              `yaml:"bind_password_env"`
	UserBaseDN         string              `yaml:"user_base_dn"`
	UserFilter         string              `yaml:"user_filter"`
	UsernameAttribute  string              `yaml:"username_attribute"`
	GroupBaseDN        string              `yaml:"group_base_dn"`
	GroupFilter        string              `yaml:"group_filter"`
	GroupNameAttribute string              `yaml:"group_name_attribute"`
	TenantID           string              `yaml:"tenant_id"`
	AutoProvision      bool                `yaml:"auto_provision"`
	GroupRoleMap       map[string][]string `yaml:"group_role_map"`
}

func (config AuthConfig) Validate() error {
	if config.MFA.Enabled && strings.TrimSpace(config.MFA.Issuer) == "" {
		return errors.New("mfa.issuer is required when MFA is enabled")
	}
	if err := config.OIDC.validate(); err != nil {
		return err
	}
	return config.LDAP.validate()
}

func (config OIDCConfig) validate() error {
	if !config.Enabled {
		return nil
	}
	if strings.TrimSpace(config.IssuerURL) == "" || strings.TrimSpace(config.ClientID) == "" ||
		strings.TrimSpace(config.RedirectURL) == "" || strings.TrimSpace(config.TenantID) == "" {
		return errors.New("oidc issuer_url, client_id, redirect_url, and tenant_id are required")
	}
	for name, raw := range map[string]string{"issuer_url": config.IssuerURL, "redirect_url": config.RedirectURL} {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
			return fmt.Errorf("oidc.%s must be an absolute HTTPS URL", name)
		}
	}
	if config.PostLogoutURL != "" {
		parsed, err := url.Parse(config.PostLogoutURL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
			return errors.New("oidc.post_logout_url must be an absolute HTTPS URL")
		}
	}
	if config.UsernameClaim == "" || config.GroupsClaim == "" {
		return errors.New("oidc username_claim and groups_claim are required")
	}
	return validateRoleMap("oidc.group_role_map", config.GroupRoleMap)
}

func (config LDAPConfig) validate() error {
	if !config.Enabled {
		return nil
	}
	parsed, err := url.Parse(config.URL)
	if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "ldaps" && parsed.Scheme != "ldap") {
		return errors.New("ldap.url must be an absolute ldap:// or ldaps:// URL without credentials")
	}
	if parsed.Scheme == "ldap" && !config.StartTLS {
		return errors.New("ldap:// requires start_tls=true; plaintext LDAP is forbidden")
	}
	if parsed.Scheme == "ldaps" && config.StartTLS {
		return errors.New("ldap.start_tls must be false with ldaps://")
	}
	if strings.TrimSpace(config.UserBaseDN) == "" || strings.TrimSpace(config.UserFilter) == "" ||
		!strings.Contains(config.UserFilter, "{username}") || strings.TrimSpace(config.TenantID) == "" {
		return errors.New("ldap user_base_dn, tenant_id, and a user_filter containing {username} are required")
	}
	if (config.BindDN == "") != (config.BindPasswordEnv == "") {
		return errors.New("ldap bind_dn and bind_password_env must be configured together")
	}
	if strings.TrimSpace(config.GroupBaseDN) == "" || strings.TrimSpace(config.GroupFilter) == "" ||
		!strings.Contains(config.GroupFilter, "{user_dn}") {
		return errors.New("ldap group_base_dn and a group_filter containing {user_dn} are required")
	}
	return validateRoleMap("ldap.group_role_map", config.GroupRoleMap)
}

func validateRoleMap(name string, mapping map[string][]string) error {
	if len(mapping) == 0 {
		return fmt.Errorf("%s must not be empty", name)
	}
	groups := make([]string, 0, len(mapping))
	for group := range mapping {
		groups = append(groups, group)
	}
	sort.Strings(groups)
	for _, group := range groups {
		if strings.TrimSpace(group) == "" || len(mapping[group]) == 0 {
			return fmt.Errorf("%s contains an empty group or role list", name)
		}
		for _, role := range mapping[group] {
			if strings.TrimSpace(role) == "" {
				return fmt.Errorf("%s contains an empty role id", name)
			}
		}
	}
	return nil
}
