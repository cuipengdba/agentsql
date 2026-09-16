package config

import (
	"fmt"
	"os"
	"strings"
	"unicode/utf8"
)

const (
	publicExampleSecret = "0123456789abcdef0123456789abcdef"
)

var publiclyKnownSecrets = [...]string{
	publicExampleSecret,
}

var weakAdminPasswords = [...]string{
	"admin",
	"password",
	"password123",
	"admin123",
	"12345678",
	"qwerty123",
	"changeme",
	"agentsql",
	"change_me_agentsql_admin_2026!",
}

// InsecureModeFromEnv reports whether explicitly allowing public test
// credentials has been requested. This switch is permanently limited to that
// purpose; it must never disable authentication, security rules, or fail-closed
// behavior.
func InsecureModeFromEnv() (bool, error) {
	switch os.Getenv("AGENTSQL_INSECURE") {
	case "":
		return false, nil
	case "1":
		return true, nil
	default:
		return false, fmt.Errorf(`AGENTSQL_INSECURE must be unset or exactly "1"`)
	}
}

// ValidateStartupSecret enforces the production startup policy for the
// encryption key without changing the lower-level cipher contract.
func ValidateStartupSecret(secret string, insecure bool) error {
	const guidance = `generate one with "openssl rand -base64 24" and back it up with agentsql.db`
	if secret == "" {
		return fmt.Errorf("AGENTSQL_SECRET is required; %s", guidance)
	}
	if len([]byte(secret)) != 32 {
		return fmt.Errorf("AGENTSQL_SECRET must be exactly 32 bytes; %s", guidance)
	}
	if !insecure && containsExact(publiclyKnownSecrets[:], secret) {
		return fmt.Errorf("AGENTSQL_SECRET uses a publicly known example value and is refused; generate a unique 32-byte value; for local development/testing set AGENTSQL_INSECURE=1")
	}
	return nil
}

// ValidateAdminPassword enforces the production startup policy for the web
// console administrator password.
func ValidateAdminPassword(username, password string, insecure bool) error {
	if password == "" {
		return fmt.Errorf("AGENTSQL_ADMIN_PASSWORD is required")
	}
	if insecure {
		return nil
	}

	trimmed := strings.TrimSpace(password)
	weak := utf8.RuneCountInString(trimmed) < 12 ||
		strings.EqualFold(trimmed, username) ||
		containsExact(weakAdminPasswords[:], strings.ToLower(trimmed))
	if weak {
		return fmt.Errorf("AGENTSQL_ADMIN_PASSWORD is too weak; use a unique passphrase of at least 12 characters; set AGENTSQL_INSECURE=1 for local dev")
	}
	return nil
}

func containsExact(values []string, candidate string) bool {
	for _, value := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
