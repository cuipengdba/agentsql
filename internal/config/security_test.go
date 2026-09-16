package config

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInsecureModeFromEnv(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		unset   bool
		enabled bool
		wantErr string
	}{
		{name: "unset", unset: true},
		{name: "empty"},
		{name: "exact one", value: "1", enabled: true},
		{name: "true rejected", value: "true", wantErr: `AGENTSQL_INSECURE must be unset or exactly "1"`},
		{name: "whitespace rejected", value: " 1 ", wantErr: `AGENTSQL_INSECURE must be unset or exactly "1"`},
		{name: "zero rejected", value: "0", wantErr: `AGENTSQL_INSECURE must be unset or exactly "1"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("AGENTSQL_INSECURE", test.value)
			if test.unset {
				require.NoError(t, os.Unsetenv("AGENTSQL_INSECURE"))
			}
			enabled, err := InsecureModeFromEnv()
			if test.wantErr != "" {
				require.EqualError(t, err, test.wantErr)
				require.False(t, enabled)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.enabled, enabled)
		})
	}
}

func TestValidateStartupSecret(t *testing.T) {
	tests := []struct {
		name     string
		secret   string
		insecure bool
		wantErr  string
	}{
		{name: "missing", wantErr: "AGENTSQL_SECRET is required"},
		{name: "wrong byte length", secret: "short", wantErr: "must be exactly 32 bytes"},
		{name: "runes are measured as bytes", secret: strings.Repeat("界", 32), wantErr: "must be exactly 32 bytes"},
		{name: "public value refused", secret: publicExampleSecret, wantErr: "publicly known example value"},
		{name: "strong value", secret: "a7f3c91e5b2d4806af15ce9034d77b21"},
		{name: "similar case is exact only", secret: "0123456789ABCDEF0123456789ABCDEF"},
		{name: "insecure allows public value", secret: publicExampleSecret, insecure: true},
		{name: "insecure still rejects missing", insecure: true, wantErr: "AGENTSQL_SECRET is required"},
		{name: "insecure still rejects wrong length", secret: "short", insecure: true, wantErr: "must be exactly 32 bytes"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateStartupSecret(test.secret, test.insecure)
			if test.wantErr != "" {
				require.ErrorContains(t, err, test.wantErr)
				if test.name == "missing" || test.name == "wrong byte length" {
					require.ErrorContains(t, err, "openssl rand -base64 24")
					require.ErrorContains(t, err, "agentsql.db")
				}
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidateAdminPassword(t *testing.T) {
	tests := []struct {
		name     string
		username string
		password string
		insecure bool
		wantErr  string
	}{
		{name: "missing", username: "admin", wantErr: "AGENTSQL_ADMIN_PASSWORD is required"},
		{name: "short", username: "admin", password: "short", wantErr: "too weak"},
		{name: "trimmed short", username: "admin", password: "  short  ", wantErr: "too weak"},
		{name: "same as username", username: "LongAdministrator", password: " longadministrator ", wantErr: "too weak"},
		{name: "strong passphrase", username: "admin", password: "Str0ng!Passphrase_2026"},
		{name: "insecure allows short", username: "admin", password: "admin", insecure: true},
		{name: "insecure still rejects missing", username: "admin", insecure: true, wantErr: "AGENTSQL_ADMIN_PASSWORD is required"},
	}
	for _, password := range weakAdminPasswords {
		tests = append(tests, struct {
			name     string
			username string
			password string
			insecure bool
			wantErr  string
		}{name: "blacklist " + password, username: "operator", password: "  " + strings.ToUpper(password) + "  ", wantErr: "too weak"})
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateAdminPassword(test.username, test.password, test.insecure)
			if test.wantErr != "" {
				require.ErrorContains(t, err, test.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}
