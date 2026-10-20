package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/compliance"
	"github.com/stretchr/testify/require"
)

func TestAuditCheckpointCLIRejectsTamperAndWrongPredecessor(t *testing.T) {
	directory := t.TempDir()
	archive, _, err := compliance.BuildArchive(time.Now().UTC().Add(-time.Minute), []compliance.ArchiveFile{{Name: "audit.csv", MediaType: "text/csv", Data: []byte("id\n1\n")}})
	require.NoError(t, err)
	archivePath := filepath.Join(directory, "audit.zip")
	checkpointPath := filepath.Join(directory, "checkpoint.json")
	seedPath := filepath.Join(directory, "seed.hex")
	publicPath := filepath.Join(directory, "public.hex")
	require.NoError(t, os.WriteFile(archivePath, archive, 0o600))
	var output, failures strings.Builder
	require.Zero(t, run([]string{"audit", "checkpoint-keygen", "--private-seed-out", seedPath, "--public-key-out", publicPath}, &output, &failures), failures.String())
	output.Reset()
	failures.Reset()
	require.Zero(t, run([]string{"audit", "checkpoint-create", "--in", archivePath, "--out", checkpointPath,
		"--private-seed-file", seedPath, "--chain-id", "traffic", "--head-seq", "0"}, &output, &failures), failures.String())
	output.Reset()
	failures.Reset()
	require.Zero(t, run([]string{"audit", "checkpoint-verify", "--in", archivePath, "--checkpoint", checkpointPath,
		"--public-key-file", publicPath}, &output, &failures), failures.String())
	output.Reset()
	failures.Reset()
	require.Equal(t, 1, run([]string{"audit", "checkpoint-verify", "--in", archivePath, "--checkpoint", checkpointPath,
		"--public-key-file", publicPath, "--expected-previous-sha256", strings.Repeat("a", 64)}, &output, &failures))
	require.Contains(t, failures.String(), "predecessor")
	archive[0] ^= 1
	require.NoError(t, os.WriteFile(archivePath, archive, 0o600))
	output.Reset()
	failures.Reset()
	require.Equal(t, 1, run([]string{"audit", "checkpoint-verify", "--in", archivePath, "--checkpoint", checkpointPath,
		"--public-key-file", publicPath}, &output, &failures))
}
