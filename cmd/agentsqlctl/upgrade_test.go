package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpgradeCheckAndDryRun(t *testing.T) {
	artifact := []byte("test binary")
	digest := sha256.Sum256(artifact)
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.json":
			fmt.Fprintf(w, `{"version":"v99.0.0","url":%q,"sha256":%q}`, server.URL+"/artifact", hex.EncodeToString(digest[:]))
		case "/artifact":
			_, _ = w.Write(artifact)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	manifest, status, err := checkUpgrade(context.Background(), server.Client(), server.URL+"/manifest.json")
	require.NoError(t, err)
	require.Equal(t, "update-available", status)
	dir := t.TempDir()
	executable := filepath.Join(dir, "agentsqlctl")
	config := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(executable, []byte("old binary"), 0o600))
	require.NoError(t, os.WriteFile(config, []byte("secret config"), 0o600))
	backup := filepath.Join(dir, "backups")
	var output strings.Builder
	require.NoError(t, dryRunUpgrade(context.Background(), server.Client(), manifest, backup, config, executable, &output))
	require.Contains(t, output.String(), "changes=none")
	require.Contains(t, output.String(), "artifact_sha256="+hex.EncodeToString(digest[:]))
	require.NotContains(t, output.String(), "secret config")
	_, err = os.Stat(backup)
	require.ErrorIs(t, err, os.ErrNotExist)
	contents, err := os.ReadFile(executable)
	require.NoError(t, err)
	require.Equal(t, []byte("old binary"), contents)
	manifest.SHA256 = strings.Repeat("0", 64)
	require.ErrorContains(t, dryRunUpgrade(context.Background(), server.Client(), manifest, backup, config, executable, &output), "SHA-256 mismatch")
}

func TestUpgradeRejectsUntrustedInputAndLiveApply(t *testing.T) {
	for _, address := range []string{"http://example.com/manifest", "https://user:pass@example.com/manifest", "https://example.com/manifest?token=x"} {
		_, _, err := checkUpgrade(context.Background(), http.DefaultClient, address)
		require.ErrorContains(t, err, "HTTPS URL")
	}
	command := newUpgradeCommand()
	command.SetArgs([]string{"apply", "--backup-dir", t.TempDir(), "--yes"})
	require.ErrorContains(t, command.Execute(), "live upgrade is unavailable")
	for _, version := range []string{"v0.5", "v0.5.0-rc1", "v01.5.0", "v0.5.x"} {
		_, err := parseUpgradeVersion(version)
		require.Error(t, err)
	}
}
