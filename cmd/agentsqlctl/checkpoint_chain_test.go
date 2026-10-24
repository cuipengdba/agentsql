package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/compliance"
)

func TestCheckpointChainCLIRejectsMissingEntryAndPathEscape(t *testing.T) {
	directory := t.TempDir()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	archive, _, err := compliance.BuildArchive(time.Now().UTC().Add(-time.Hour), []compliance.ArchiveFile{{Name: "audit.csv", MediaType: "text/csv", Data: []byte("id\n1\n")}})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := compliance.SignArchiveCheckpoint(archive, "test-chain", 1, strings.Repeat("a", 64), "", time.Now().UTC(), private)
	if err != nil {
		t.Fatal(err)
	}
	head, err := compliance.ArchiveCheckpointDigest(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(directory, "archive.zip")
	checkpointPath := filepath.Join(directory, "checkpoint.json")
	publicPath := filepath.Join(directory, "public.hex")
	catalogPath := filepath.Join(directory, "catalog.json")
	checkpointJSON, _ := json.Marshal(checkpoint)
	for path, contents := range map[string][]byte{
		archivePath: archive, checkpointPath: checkpointJSON,
		publicPath: []byte(hex.EncodeToString(public) + "\n"),
	} {
		if err := os.WriteFile(path, contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeCatalog := func(archiveName string) {
		t.Helper()
		contents, _ := json.Marshal(map[string]any{"schema_version": auditChainCatalogSchema,
			"entries": []map[string]string{{"archive": archiveName, "checkpoint": "checkpoint.json"}}})
		if err := os.WriteFile(catalogPath, contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeCatalog("archive.zip")
	args := []string{"audit", "checkpoint-chain-verify", "--catalog", catalogPath, "--public-key-file", publicPath,
		"--chain-id", "test-chain", "--expected-head", head, "--max-age", "3h"}
	var output, failures bytes.Buffer
	if code := run(args, &output, &failures); code != 0 || !strings.Contains(output.String(), "entries=1") {
		t.Fatalf("valid chain failed: code=%d out=%s err=%s", code, output.String(), failures.String())
	}
	writeCatalog("../outside.zip")
	output.Reset()
	failures.Reset()
	if code := run(args, &output, &failures); code == 0 || !strings.Contains(failures.String(), "local and relative") {
		t.Fatalf("path escape was accepted: code=%d err=%s", code, failures.String())
	}
	writeCatalog("missing.zip")
	output.Reset()
	failures.Reset()
	if code := run(args, &output, &failures); code == 0 {
		t.Fatal("missing archive was accepted")
	}
}
