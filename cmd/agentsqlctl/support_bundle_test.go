package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSupportBundleReadOnlyAndCreateOnly(t *testing.T) {
	database := filepath.Join(t.TempDir(), "metadata.db")
	config := sqliteMigrationCheckConfig(t, database)
	require.Equal(t, 0, run([]string{"migrate", "--config", config}, io.Discard, io.Discard))
	before, err := os.ReadFile(database)
	require.NoError(t, err)
	output := filepath.Join(t.TempDir(), "support.json")
	var stdout, stderr strings.Builder
	require.Equal(t, 0, run([]string{"support-bundle", "--config", config, "--output", output}, &stdout, &stderr), stderr.String())
	data, err := os.ReadFile(output)
	require.NoError(t, err)
	var bundle supportBundle
	require.NoError(t, json.Unmarshal(data, &bundle))
	require.True(t, bundle.ReadOnly)
	require.Equal(t, "ready", bundle.Diagnostic.Status)
	require.NotEmpty(t, bundle.Capabilities["oracle"].EvidenceDoc)
	require.NotContains(t, string(data), database)
	require.NotContains(t, string(data), config)
	after, err := os.ReadFile(database)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Equal(t, 1, run([]string{"support-bundle", "--config", config, "--output", output}, io.Discard, io.Discard))
	after, err = os.ReadFile(output)
	require.NoError(t, err)
	require.Equal(t, data, after)
}

func TestSupportBundleDoesNotLeakSecretsOrCreateStore(t *testing.T) {
	canary := "support-secret-hostname-canary"
	for _, invalid := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing store", true: "invalid config"}[invalid], func(t *testing.T) {
			database := filepath.Join(t.TempDir(), canary+".db")
			config := sqliteMigrationCheckConfig(t, database)
			if invalid {
				require.NoError(t, os.WriteFile(config, []byte("unknown: "+canary+"\n"), 0600))
			}
			output := filepath.Join(t.TempDir(), "support.json")
			var stdout, stderr strings.Builder
			require.Equal(t, 1, run([]string{"support-bundle", "--config", config, "--output", output}, &stdout, &stderr))
			data, err := os.ReadFile(output)
			require.NoError(t, err)
			var bundle supportBundle
			require.NoError(t, json.Unmarshal(data, &bundle))
			require.Equal(t, "blocked", bundle.Diagnostic.Status)
			require.NotContains(t, string(data)+stdout.String()+stderr.String(), canary)
			require.NoFileExists(t, database)
			require.NoFileExists(t, database+".instance-id")
			require.NoFileExists(t, config+".instance-id")
		})
	}
}

func TestSupportBundleRejectsSymlinkAndUnboundedConfig(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	require.NoError(t, os.WriteFile(target, []byte("preserve"), 0600))
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err == nil {
		require.Equal(t, 1, run([]string{"support-bundle", "--output", link}, io.Discard, io.Discard))
		data, err := os.ReadFile(target)
		require.NoError(t, err)
		require.Equal(t, "preserve", string(data))
	}
	config := filepath.Join(dir, "large.yaml")
	require.NoError(t, os.WriteFile(config, []byte(strings.Repeat("x", (1<<20)+1)), 0600))
	_, err := readDiagnosticConfig(config)
	require.Error(t, err)
	require.Equal(t, 1, run([]string{"support-bundle", "--config", config, "--output", filepath.Join(dir, "unused"), "--timeout", "2m"}, io.Discard, io.Discard))
	require.NoFileExists(t, filepath.Join(dir, "unused"))
}
