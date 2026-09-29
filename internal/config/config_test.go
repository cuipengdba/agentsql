package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const validConfig = `server:
  http_listen: "127.0.0.1:7780"
  console_enabled: true
store:
  sqlite_path: %q
defaults:
  statement_timeout_ms: 5000
  row_limit: 1000
  max_conns_per_datasource: 5
  qps_per_agent: 20
theme:
  default: dark
`

func TestLoadValidConfigCreatesSQLiteDirectory(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "nested", "data", "agentsql.db")
	path := writeConfig(t, fmt.Sprintf(validConfig, filepath.ToSlash(databasePath)))

	loaded, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, filepath.Clean(databasePath), loaded.Store.SQLitePath)

	info, err := os.Stat(filepath.Dir(databasePath))
	require.NoError(t, err)
	require.True(t, info.IsDir())
}

func TestParseValidConfigDoesNotCreateSQLiteDirectory(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "must-not-exist", "agentsql.db")
	loaded, err := Parse([]byte(fmt.Sprintf(validConfig, filepath.ToSlash(databasePath))))

	require.NoError(t, err)
	require.Equal(t, filepath.ToSlash(databasePath), loaded.Store.SQLitePath)
	_, err = os.Stat(filepath.Dir(databasePath))
	require.True(t, errors.Is(err, os.ErrNotExist))
}

func TestParseDefaultsConsoleAndRejectsUnknownOrTrailingDocuments(t *testing.T) {
	databasePath := filepath.ToSlash(filepath.Join(t.TempDir(), "agentsql.db"))
	withoutConsole := fmt.Sprintf(`server:
  http_listen: "127.0.0.1:7780"
store:
  sqlite_path: %q
defaults:
  statement_timeout_ms: 5000
  row_limit: 1000
  max_conns_per_datasource: 5
  qps_per_agent: 20
theme:
  default: dark
`, databasePath)
	loaded, err := Parse([]byte(withoutConsole))
	require.NoError(t, err)
	require.True(t, loaded.Server.ConsoleEnabled)
	require.True(t, loaded.Server.EventStream)
	require.Equal(t, 100, loaded.Server.EventStreamMaxConnections)

	_, err = Parse([]byte(withoutConsole + "unknown_field: true\n"))
	require.Error(t, err)
	_, err = Parse([]byte(withoutConsole + "---\nserver: {}\n"))
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrMultipleYAMLDocuments))
}

func TestColumnAuthorizationActivationConfigIsExplicitAndBounded(t *testing.T) {
	databasePath := filepath.ToSlash(filepath.Join(t.TempDir(), "agentsql.db"))
	base := fmt.Sprintf(validConfig, databasePath)
	loaded, err := Parse([]byte(base))
	require.NoError(t, err)
	require.True(t, loaded.ColumnAuthorization.Enabled, "PostgreSQL B2 factory default must be on")
	require.False(t, loaded.ColumnAuthorization.DryRun, "factory dry-run must remain off")
	require.Empty(t, loaded.ColumnAuthorization.InstanceID, "Parse must not create persistent state")

	dryRun := base + "column_authorization:\n  enabled: false\n  dry_run: true\n  instance_id: runtime-observer\n"
	loaded, err = Parse([]byte(dryRun))
	require.NoError(t, err)
	require.True(t, loaded.ColumnAuthorization.DryRun)
	require.False(t, loaded.ColumnAuthorization.Enabled)

	enabled := base + "column_authorization:\n  enabled: true\n  instance_id: runtime-1\n  lease_ms: 15000\n  heartbeat_interval_ms: 5000\n"
	loaded, err = Parse([]byte(enabled))
	require.NoError(t, err)
	require.True(t, loaded.ColumnAuthorization.Enabled)
	require.Equal(t, "runtime-1", loaded.ColumnAuthorization.InstanceID)

	for _, invalid := range []string{
		base + "column_authorization:\n  enabled: true\n  instance_id: \"\"\n",
		base + "column_authorization:\n  dry_run: true\n",
		base + "column_authorization:\n  enabled: true\n  dry_run: true\n  instance_id: runtime-1\n",
		base + "column_authorization:\n  enabled: true\n  instance_id: runtime-1\n  instance_id_file: runtime.id\n",
		base + "column_authorization:\n  enabled: true\n  instance_id: runtime-1\n  lease_ms: 1000\n  heartbeat_interval_ms: 1000\n",
		base + "column_authorization:\n  enabled: true\n  instance_id: runtime-1\n  lease_ms: -1\n",
	} {
		_, err = Parse([]byte(invalid))
		require.Error(t, err)
	}
}

func TestColumnAuthorizationExplicitOffOverridesDefaultOn(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "agentsql.db")
	contents := fmt.Sprintf(validConfig, filepath.ToSlash(databasePath)) + "column_authorization:\n  enabled: false\n"
	loaded, err := Load(writeConfig(t, contents))
	require.NoError(t, err)
	require.False(t, loaded.ColumnAuthorization.Enabled)
	require.False(t, loaded.ColumnAuthorization.DryRun)
	require.Empty(t, loaded.ColumnAuthorization.InstanceID)
	require.NoFileExists(t, databasePath+".instance-id")
}

func TestMCPB5DefaultsExplicitOffAndValidation(t *testing.T) {
	base := fmt.Sprintf(validConfig, filepath.ToSlash(filepath.Join(t.TempDir(), "agentsql.db")))
	loaded, err := Parse([]byte(base))
	require.NoError(t, err)
	require.True(t, loaded.MCP.HTTP.Stateful)
	require.Equal(t, 600_000, loaded.MCP.HTTP.SessionTimeoutMS)
	require.True(t, loaded.MCP.Sessions.Enabled)
	require.True(t, loaded.MCP.Transactions.Postgres)
	require.False(t, loaded.MCP.Transactions.MySQL)
	require.Equal(t, 15_000, loaded.MCP.Transactions.IdleTimeoutMS)
	require.Equal(t, 60_000, loaded.MCP.Transactions.WallTimeoutMS)

	off, err := Parse([]byte(base + "mcp:\n  sessions:\n    enabled: false\n"))
	require.NoError(t, err)
	require.False(t, off.MCP.Sessions.Enabled)
	require.False(t, off.MCP.Transactions.Postgres, "session rollback switch must disable the dependent default")
	require.True(t, off.MCP.HTTP.Stateful, "B5 rollback must not disable transport sessions")

	stateless, err := Parse([]byte(base + "mcp:\n  http:\n    stateful: false\n    session_timeout_ms: 300000\n"))
	require.NoError(t, err)
	require.False(t, stateless.MCP.HTTP.Stateful, "explicit false must override the stateful default")
	require.Equal(t, 300_000, stateless.MCP.HTTP.SessionTimeoutMS)
	require.True(t, stateless.MCP.Sessions.Enabled, "transport rollback must not disable B5 sessions")

	postgresOff, err := Parse([]byte(base + "mcp:\n  transactions:\n    postgres: false\n"))
	require.NoError(t, err)
	require.True(t, postgresOff.MCP.Sessions.Enabled)
	require.False(t, postgresOff.MCP.Transactions.Postgres)

	_, err = Parse([]byte(base + "mcp:\n  transactions:\n    mysql: true\n"))
	require.ErrorIs(t, err, ErrB5MySQLUnsupported)
	require.Contains(t, err.Error(), "不受支持")

	for _, fragment := range []string{
		"mcp:\n  http:\n    session_timeout_ms: 1800001\n",
		"mcp:\n  sessions:\n    idle_ttl_ms: 1800001\n",
		"mcp:\n  transactions:\n    wall_timeout_ms: 60001\n",
		"mcp:\n  sessions:\n    enabled: false\n  transactions:\n    postgres: true\n",
	} {
		_, err = Parse([]byte(base + fragment))
		require.Error(t, err)
	}
}

func TestLoadPersistsRandomColumnAuthorizationInstanceIDPerDeployment(t *testing.T) {
	root := t.TempDir()
	firstDatabase := filepath.Join(root, "replica-a", "agentsql.db")
	secondDatabase := filepath.Join(root, "replica-b", "agentsql.db")
	firstPath := writeConfig(t, fmt.Sprintf(validConfig, filepath.ToSlash(firstDatabase)))
	secondPath := filepath.Join(root, "replica-b.yaml")
	require.NoError(t, os.WriteFile(secondPath, []byte(fmt.Sprintf(validConfig, filepath.ToSlash(secondDatabase))), 0o600))

	first, err := Load(firstPath)
	require.NoError(t, err)
	reloaded, err := Load(firstPath)
	require.NoError(t, err)
	second, err := Load(secondPath)
	require.NoError(t, err)

	require.NotEmpty(t, first.ColumnAuthorization.InstanceID)
	require.Equal(t, first.ColumnAuthorization.InstanceID, reloaded.ColumnAuthorization.InstanceID)
	require.NotEqual(t, first.ColumnAuthorization.InstanceID, second.ColumnAuthorization.InstanceID)
	require.FileExists(t, firstDatabase+".instance-id")
}

func TestColumnAuthorizationExplicitInstanceIDWinsAndCorruptDefaultFails(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "agentsql.db")
	explicit := fmt.Sprintf(validConfig, filepath.ToSlash(databasePath)) +
		"column_authorization:\n  enabled: true\n  instance_id: replica-explicit\n"
	loaded, err := Load(writeConfig(t, explicit))
	require.NoError(t, err)
	require.Equal(t, "replica-explicit", loaded.ColumnAuthorization.InstanceID)
	require.NoFileExists(t, databasePath+".instance-id")

	defaultPath := writeConfig(t, fmt.Sprintf(validConfig, filepath.ToSlash(databasePath)))
	require.NoError(t, os.WriteFile(databasePath+".instance-id", []byte("shared-public-value\n"), 0o600))
	_, err = Load(defaultPath)
	require.ErrorIs(t, err, ErrInvalidColumnAuthorizationInstanceID)
}

func TestColumnAuthorizationInstanceIDFileSupportsReadOnlyConfigLayouts(t *testing.T) {
	root := t.TempDir()
	databasePath := filepath.Join(root, "agentsql.db")
	identityPath := filepath.Join(root, "state", "b2-instance-id")
	contents := fmt.Sprintf(validConfig, filepath.ToSlash(databasePath)) + fmt.Sprintf(
		"column_authorization:\n  instance_id_file: %q\n", filepath.ToSlash(identityPath),
	)
	configPath := writeConfig(t, contents)

	loaded, err := Load(configPath)
	require.NoError(t, err)
	require.NotEmpty(t, loaded.ColumnAuthorization.InstanceID)
	require.Equal(t, filepath.ToSlash(identityPath), loaded.ColumnAuthorization.InstanceIDFile)
	require.FileExists(t, identityPath)
	require.NoError(t, loaded.Validate())
}

func TestParseRegistersStrictRedactionFields(t *testing.T) {
	databasePath := filepath.ToSlash(filepath.Join(t.TempDir(), "agentsql.db"))
	contents := fmt.Sprintf(validConfig, databasePath) + "redaction:\n  hash_key: 12345678901234567890123456789012\n"
	loaded, err := Parse([]byte(contents))
	require.NoError(t, err)
	require.Equal(t, "12345678901234567890123456789012", loaded.Redaction.HashKey)

	_, err = Parse([]byte(contents + "  unknown: true\n"))
	require.Error(t, err)
}

func TestParsePreservesEventStreamSettingsAndRejectsInvalidConnectionLimits(t *testing.T) {
	databasePath := filepath.ToSlash(filepath.Join(t.TempDir(), "agentsql.db"))
	contents := fmt.Sprintf(validConfig, databasePath)
	contents = strings.Replace(contents, "console_enabled: true", "console_enabled: true\n  event_stream: false\n  event_stream_max_connections: 1", 1)

	loaded, err := Parse([]byte(contents))
	require.NoError(t, err)
	require.False(t, loaded.Server.EventStream)
	require.Equal(t, 1, loaded.Server.EventStreamMaxConnections)

	for _, limit := range []int{0, 1001} {
		invalid := strings.Replace(contents, "event_stream_max_connections: 1", fmt.Sprintf("event_stream_max_connections: %d", limit), 1)
		_, err = Parse([]byte(invalid))
		require.ErrorIs(t, err, ErrInvalidEventStreamMaxConnections)
	}
}

func TestParsePreservesExplicitConsoleDisabled(t *testing.T) {
	databasePath := filepath.ToSlash(filepath.Join(t.TempDir(), "agentsql.db"))
	contents := fmt.Sprintf(validConfig, databasePath)
	contents = strings.Replace(contents, "console_enabled: true", "console_enabled: false", 1)

	loaded, err := Parse([]byte(contents))
	require.NoError(t, err)
	require.False(t, loaded.Server.ConsoleEnabled)
}

func TestLoadRejectsRequiredInvalidConfigurations(t *testing.T) {
	databasePath := filepath.ToSlash(filepath.Join(t.TempDir(), "agentsql.db"))
	tests := []struct {
		name        string
		contents    string
		targetError error
	}{
		{
			name: "missing sqlite path",
			contents: `server: {http_listen: "127.0.0.1:7780", console_enabled: true}
store: {sqlite_path: ""}
defaults: {statement_timeout_ms: 5000, row_limit: 1000, max_conns_per_datasource: 5, qps_per_agent: 20}
theme: {default: dark}
`,
			targetError: ErrMissingSQLitePath,
		},
		{
			name: "invalid port",
			contents: fmt.Sprintf(`server: {http_listen: "127.0.0.1:70000", console_enabled: true}
store: {sqlite_path: %q}
defaults: {statement_timeout_ms: 5000, row_limit: 1000, max_conns_per_datasource: 5, qps_per_agent: 20}
theme: {default: dark}
`, databasePath),
			targetError: ErrInvalidHTTPListen,
		},
		{
			name: "negative timeout",
			contents: fmt.Sprintf(`server: {http_listen: "127.0.0.1:7780", console_enabled: true}
store: {sqlite_path: %q}
defaults: {statement_timeout_ms: -1, row_limit: 1000, max_conns_per_datasource: 5, qps_per_agent: 20}
theme: {default: dark}
`, databasePath),
			targetError: ErrNegativeStatementTimeout,
		},
		{
			name: "too many event stream connections",
			contents: fmt.Sprintf(`server: {http_listen: "127.0.0.1:7780", console_enabled: true, event_stream: true, event_stream_max_connections: 1001}
store: {sqlite_path: %q}
defaults: {statement_timeout_ms: 5000, row_limit: 1000, max_conns_per_datasource: 5, qps_per_agent: 20}
theme: {default: dark}
`, databasePath),
			targetError: ErrInvalidEventStreamMaxConnections,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, test.contents))
			require.Error(t, err)
			require.True(t, errors.Is(err, test.targetError))
		})
	}
}

func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	return path
}
