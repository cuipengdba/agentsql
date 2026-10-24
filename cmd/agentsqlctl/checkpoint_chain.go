package main

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/compliance"
	"github.com/spf13/cobra"
)

const auditChainCatalogSchema = "agentsql.audit-chain-catalog/v1"
const maxAuditChainEntries = 1000

type auditChainCatalog struct {
	SchemaVersion string `json:"schema_version"`
	Entries       []struct {
		Archive    string `json:"archive"`
		Checkpoint string `json:"checkpoint"`
	} `json:"entries"`
}

func decodeAuditChainJSON(path string, limit int64, target any) error {
	contents, err := readCheckpointFile(path, limit)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("audit chain JSON has trailing data")
	}
	return nil
}

func auditChainMemberPath(directory, member string) (string, error) {
	if member == "" || !filepath.IsLocal(member) || filepath.Clean(member) == "." {
		return "", errors.New("audit chain catalog path must be local and relative")
	}
	return filepath.Join(directory, member), nil
}

func newAuditVerifyCheckpointChainCommand() *cobra.Command {
	var catalogPath, publicPath, chainID, trustedPrevious, expectedHead string
	var maxAge time.Duration
	command := &cobra.Command{Use: "checkpoint-chain-verify", Short: "Verify ordered audit archives against an externally pinned head", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if catalogPath == "" || chainID == "" || expectedHead == "" || maxAge <= 0 {
				return errors.New("--catalog, --chain-id, --expected-head and a positive --max-age are required")
			}
			var catalog auditChainCatalog
			if err := decodeAuditChainJSON(catalogPath, 256<<10, &catalog); err != nil {
				return fmt.Errorf("read audit chain catalog: %w", err)
			}
			if catalog.SchemaVersion != auditChainCatalogSchema || len(catalog.Entries) == 0 || len(catalog.Entries) > maxAuditChainEntries {
				return errors.New("audit chain catalog version or entry count is invalid")
			}
			public, err := readCheckpointKey(publicPath, ed25519.PublicKeySize)
			if err != nil {
				return fmt.Errorf("read trusted audit checkpoint public key: %w", err)
			}
			verifier, err := compliance.NewArchiveCheckpointChainVerifier(ed25519.PublicKey(public), chainID, trustedPrevious, expectedHead, time.Now().UTC(), maxAge)
			if err != nil {
				return err
			}
			directory := filepath.Dir(catalogPath)
			seen := make(map[string]bool, len(catalog.Entries)*2)
			for index, entry := range catalog.Entries {
				archivePath, err := auditChainMemberPath(directory, entry.Archive)
				if err != nil {
					return fmt.Errorf("archive entry %d: %w", index+1, err)
				}
				checkpointPath, err := auditChainMemberPath(directory, entry.Checkpoint)
				if err != nil {
					return fmt.Errorf("checkpoint entry %d: %w", index+1, err)
				}
				if archivePath == checkpointPath || seen[archivePath] || seen[checkpointPath] {
					return fmt.Errorf("audit chain entry %d repeats a file", index+1)
				}
				seen[archivePath], seen[checkpointPath] = true, true
				archive, err := readCheckpointFile(archivePath, compliance.MaximumArchiveBytes)
				if err != nil {
					return fmt.Errorf("read archive entry %d: %w", index+1, err)
				}
				var checkpoint compliance.ArchiveCheckpoint
				if err := decodeAuditChainJSON(checkpointPath, 4096, &checkpoint); err != nil {
					return fmt.Errorf("read checkpoint entry %d: %w", index+1, err)
				}
				if err := verifier.VerifyNext(archive, checkpoint); err != nil {
					return err
				}
			}
			head, count, err := verifier.Finish()
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "audit checkpoint chain verified: chain=%s entries=%d head_sha256=%s\n", chainID, count, head)
			return err
		}}
	command.Flags().StringVar(&catalogPath, "catalog", "", "catalog JSON with ordered relative archive/checkpoint paths")
	command.Flags().StringVar(&publicPath, "public-key-file", "", "externally trusted Ed25519 public key as hex")
	command.Flags().StringVar(&chainID, "chain-id", "", "externally trusted audit chain ID")
	command.Flags().StringVar(&trustedPrevious, "trusted-predecessor", "", "externally trusted digest preceding the first catalog entry")
	command.Flags().StringVar(&expectedHead, "expected-head", "", "externally pinned digest of the final checkpoint")
	command.Flags().DurationVar(&maxAge, "max-age", 0, "maximum age of each signed checkpoint and archive; specify explicitly")
	return command
}
