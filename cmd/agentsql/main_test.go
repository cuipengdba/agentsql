package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/version"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

const commandConfig = `server:
  http_listen: %q
  console_enabled: true
store:
  sqlite_path: %q
defaults:
  statement_timeout_ms: %d
  row_limit: 1000
  max_conns_per_datasource: 5
  qps_per_agent: 20
theme:
  default: dark
`

func TestVersionCommand(t *testing.T) {
	t.Setenv("LOG_LEVEL", "")
	t.Setenv("LOG_FORMAT", "")

	command := newRootCommand(zerologForTest(t))
	_, _, err := command.Find([]string{"version"})
	require.NoError(t, err)

	output := new(strings.Builder)
	exitCode := run([]string{"version"}, output, io.Discard)
	require.Equal(t, 0, exitCode)
	require.Equal(t, version.Version+"\n", output.String())
	command = newRootCommand(zerologForTest(t))
	_, _, err = command.Find([]string{"mcp"})
	require.NoError(t, err)
	serve, _, err := command.Find([]string{"serve"})
	require.NoError(t, err)
	require.Nil(t, serve.Flags().Lookup("api-key"))
}

func TestMCPCommandKeepsStdoutCleanOnStartupFailure(t *testing.T) {
	t.Setenv("LOG_LEVEL", "")
	t.Setenv("LOG_FORMAT", "")
	t.Setenv("AGENTSQL_SECRET", "0123456789abcdef0123456789abcdef")
	t.Setenv("AGENTSQL_API_KEY", "")
	path := filepath.Join(t.TempDir(), "config.yaml")
	contents := fmt.Sprintf(
		commandConfig,
		"127.0.0.1:7780",
		filepath.ToSlash(filepath.Join(t.TempDir(), "agentsql.db")),
		5000,
	)
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	var stdout strings.Builder
	var stderr strings.Builder
	exitCode := run([]string{"mcp", "--config", path}, &stdout, &stderr)
	require.Equal(t, 1, exitCode)
	require.Empty(t, stdout.String())
	require.NotEmpty(t, stderr.String())
	require.NotContains(t, stderr.String(), "0123456789abcdef0123456789abcdef")
}

func TestServeExitCodes(t *testing.T) {
	t.Setenv("LOG_LEVEL", "")
	t.Setenv("LOG_FORMAT", "")
	tests := []struct {
		name         string
		httpListen   string
		sqlitePath   string
		timeoutMS    int
		secret       string
		expectedExit int
	}{
		{
			name:         "valid configuration requires secret",
			httpListen:   "127.0.0.1:7780",
			sqlitePath:   filepath.ToSlash(filepath.Join(t.TempDir(), "data", "agentsql.db")),
			timeoutMS:    5000,
			secret:       "",
			expectedExit: 1,
		},
		{
			name:         "missing sqlite path",
			httpListen:   "127.0.0.1:7780",
			sqlitePath:   "",
			timeoutMS:    5000,
			secret:       "0123456789abcdef0123456789abcdef",
			expectedExit: 1,
		},
		{
			name:         "invalid port",
			httpListen:   "127.0.0.1:70000",
			sqlitePath:   filepath.ToSlash(filepath.Join(t.TempDir(), "agentsql.db")),
			timeoutMS:    5000,
			secret:       "0123456789abcdef0123456789abcdef",
			expectedExit: 1,
		},
		{
			name:         "negative timeout",
			httpListen:   "127.0.0.1:7780",
			sqlitePath:   filepath.ToSlash(filepath.Join(t.TempDir(), "agentsql.db")),
			timeoutMS:    -1,
			secret:       "0123456789abcdef0123456789abcdef",
			expectedExit: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("AGENTSQL_SECRET", test.secret)
			path := filepath.Join(t.TempDir(), "config.yaml")
			contents := fmt.Sprintf(commandConfig, test.httpListen, test.sqlitePath, test.timeoutMS)
			require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))

			exitCode := run([]string{"serve", "--config", path}, io.Discard, io.Discard)
			require.Equal(t, test.expectedExit, exitCode)
		})
	}
}

func TestServeConsoleRequiresAdminPassword(t *testing.T) {
	t.Setenv("LOG_LEVEL", "")
	t.Setenv("LOG_FORMAT", "")
	t.Setenv("AGENTSQL_SECRET", "0123456789abcdef0123456789abcdef")
	t.Setenv("AGENTSQL_ADMIN_PASSWORD", "")
	path := filepath.Join(t.TempDir(), "config.yaml")
	contents := fmt.Sprintf(commandConfig, "127.0.0.1:7780", filepath.ToSlash(filepath.Join(t.TempDir(), "agentsql.db")), 5000)
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	require.Equal(t, 1, run([]string{"serve", "--config", path}, io.Discard, io.Discard))
}

func zerologForTest(t *testing.T) zerolog.Logger {
	t.Helper()
	logger, err := newLogger(io.Discard)
	require.NoError(t, err)
	return logger
}
