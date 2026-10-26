package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestManagedFileConfigRequiresExplicitIsolatedTarget(t *testing.T) {
	base := Config{Server: ServerConfig{ConsoleEnabled: true}, ColumnAuthorization: ColumnAuthorizationConfig{Enabled: true}}
	base.ManagedFiles = ManagedFileConfig{Enabled: true, TenantID: "tenant", ReadDatasourceID: "reader",
		ProvisionerUsername: "writer", ProvisionerPasswordEnv: "AGENTSQL_FILE_WRITER_PASSWORD"}
	require.NoError(t, base.ManagedFiles.Validate(base))
	for _, mutate := range []func(*Config){
		func(c *Config) { c.Server.ConsoleEnabled = false },
		func(c *Config) { c.ColumnAuthorization.Enabled = false },
		func(c *Config) { c.ManagedFiles.TenantID = "" },
		func(c *Config) { c.ManagedFiles.ReadDatasourceID = "" },
		func(c *Config) { c.ManagedFiles.ProvisionerUsername = "" },
		func(c *Config) { c.ManagedFiles.ProvisionerPasswordEnv = "bad-env" },
	} {
		candidate := base
		mutate(&candidate)
		require.Error(t, candidate.ManagedFiles.Validate(candidate))
	}
	base.ManagedFiles.Enabled = false
	require.Error(t, base.ManagedFiles.Validate(base), "disabled config cannot silently retain provisioner material")
}
