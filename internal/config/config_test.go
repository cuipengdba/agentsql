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
	require.False(t, loaded.ColumnAuthorization.Enabled, "factory default must remain off")

	enabled := base + "column_authorization:\n  enabled: true\n  instance_id: runtime-1\n  lease_ms: 15000\n  heartbeat_interval_ms: 5000\n"
	loaded, err = Parse([]byte(enabled))
	require.NoError(t, err)
	require.True(t, loaded.ColumnAuthorization.Enabled)
	require.Equal(t, "runtime-1", loaded.ColumnAuthorization.InstanceID)

	for _, invalid := range []string{
		base + "column_authorization:\n  enabled: true\n",
		base + "column_authorization:\n  enabled: true\n  instance_id: runtime-1\n  lease_ms: 1000\n  heartbeat_interval_ms: 1000\n",
		base + "column_authorization:\n  enabled: true\n  instance_id: runtime-1\n  lease_ms: -1\n",
	} {
		_, err = Parse([]byte(invalid))
		require.Error(t, err)
	}
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
