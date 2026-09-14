package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/version"
	"github.com/stretchr/testify/require"
)

func TestVersionCommand(t *testing.T) {
	var output strings.Builder
	require.Equal(t, 0, run([]string{"version"}, &output, io.Discard))
	require.Equal(t, version.Version+"\n", output.String())

	root := newRootCommand()
	require.True(t, root.SilenceErrors)
	require.True(t, root.SilenceUsage)
}

func TestInitConfigCreatesValidConfigAndProtectsExistingFile(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "nested", "config.yaml")
	require.Equal(t, 0, run([]string{"init-config", "--output", outputPath}, io.Discard, io.Discard))
	contents, err := os.ReadFile(outputPath)
	require.NoError(t, err)
	loaded, err := config.Parse(contents)
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1:7780", loaded.Server.HTTPListen)
	require.True(t, loaded.Server.ConsoleEnabled)
	require.Equal(t, "./data/agentsql.db", loaded.Store.SQLitePath)
	require.Equal(t, 5_000, loaded.Defaults.StatementTimeoutMS)
	require.Equal(t, 1_000, loaded.Defaults.RowLimit)
	require.Equal(t, 5, loaded.Defaults.MaxConnsPerDatasource)
	require.Equal(t, 20, loaded.Defaults.QPSPerAgent)
	require.Equal(t, "dark", loaded.Theme.Default)

	require.NoError(t, os.WriteFile(outputPath, []byte("preserve me"), 0o600))
	require.Equal(t, 1, run([]string{"init-config", "--output", outputPath}, io.Discard, io.Discard))
	preserved, err := os.ReadFile(outputPath)
	require.NoError(t, err)
	require.Equal(t, "preserve me", string(preserved))

	require.Equal(t, 0, run([]string{"init-config", "--output", outputPath, "--force"}, io.Discard, io.Discard))
	forced, err := os.ReadFile(outputPath)
	require.NoError(t, err)
	_, err = config.Parse(forced)
	require.NoError(t, err)
}

func TestCheckConfigOnlyValidates(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "absent", "agentsql.db")
	configPath := writeControlConfig(t, strings.Replace(
		defaultConfigTemplate,
		"./data/agentsql.db",
		filepath.ToSlash(databasePath),
		1,
	))
	var output strings.Builder
	require.Equal(t, 0, run([]string{"check-config", "-c", configPath}, &output, io.Discard))
	require.Contains(t, output.String(), "config ok: 127.0.0.1:7780")
	require.Contains(t, output.String(), "sqlite="+filepath.ToSlash(databasePath))
	_, err := os.Stat(filepath.Dir(databasePath))
	require.True(t, os.IsNotExist(err))

	invalid := []string{
		"not: [valid",
		strings.Replace(defaultConfigTemplate, "127.0.0.1:7780", "127.0.0.1:70000", 1),
		strings.Replace(defaultConfigTemplate, "row_limit: 1000", "row_limit: 0", 1),
	}
	for _, contents := range invalid {
		path := writeControlConfig(t, contents)
		require.Equal(t, 1, run([]string{"check-config", "-c", path}, io.Discard, io.Discard))
	}
}

func TestMigrateRequiresSecretAndPrintsVersions(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "metadata", "agentsql.db")
	configPath := writeControlConfig(t, strings.Replace(
		defaultConfigTemplate,
		"./data/agentsql.db",
		filepath.ToSlash(databasePath),
		1,
	))
	t.Setenv("AGENTSQL_SECRET", "")
	require.Equal(t, 1, run([]string{"migrate", "-c", configPath}, io.Discard, io.Discard))

	t.Setenv("AGENTSQL_SECRET", "0123456789abcdef0123456789abcdef")
	var output strings.Builder
	require.Equal(t, 0, run([]string{"migrate", "-c", configPath}, &output, io.Discard))
	require.Contains(t, output.String(), "migration current=1 latest=1")
	_, err := os.Stat(databasePath)
	require.NoError(t, err)
}

func TestHealthCommand(t *testing.T) {
	t.Run("healthy", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"status":"ok","version":"test"}`))
		}))
		defer server.Close()
		var output strings.Builder
		require.Equal(t, 0, run([]string{"health", "--url", server.URL}, &output, io.Discard))
		require.JSONEq(t, `{"status":"ok","version":"test"}`, strings.TrimSpace(output.String()))
	})

	t.Run("unavailable", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = writer.Write([]byte(`{"status":"not ready"}`))
		}))
		defer server.Close()
		require.Equal(t, 1, run([]string{"health", "--url", server.URL}, io.Discard, io.Discard))
	})

	t.Run("timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			time.Sleep(50 * time.Millisecond)
			_, _ = writer.Write([]byte(`{"status":"ok"}`))
		}))
		defer server.Close()
		require.Equal(t, 1, run(
			[]string{"health", "--url", server.URL, "--timeout", "1ms"},
			io.Discard,
			io.Discard,
		))
	})
}

func writeControlConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	return path
}
