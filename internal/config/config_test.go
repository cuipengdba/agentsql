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

	_, err = Parse([]byte(withoutConsole + "unknown_field: true\n"))
	require.Error(t, err)
	_, err = Parse([]byte(withoutConsole + "---\nserver: {}\n"))
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrMultipleYAMLDocuments))
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
