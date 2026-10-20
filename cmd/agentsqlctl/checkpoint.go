package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/compliance"
	"github.com/spf13/cobra"
)

func newAuditCheckpointKeygenCommand() *cobra.Command {
	var seedPath, publicPath string
	command := &cobra.Command{Use: "checkpoint-keygen", Short: "Create a separate Ed25519 key pair for external audit checkpoints", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if seedPath == "" || publicPath == "" || seedPath == publicPath {
				return errors.New("two distinct --private-seed-out and --public-key-out paths are required")
			}
			for _, path := range []string{seedPath, publicPath} {
				if _, err := os.Stat(path); err == nil {
					return fmt.Errorf("checkpoint key output already exists: %s", path)
				} else if !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
			public, private, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				return err
			}
			if err := writeNewAuditFile(seedPath, []byte(hex.EncodeToString(private.Seed())+"\n")); err != nil {
				return err
			}
			if err := writeNewAuditFile(publicPath, []byte(hex.EncodeToString(public)+"\n")); err != nil {
				return err
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "checkpoint keys written: private=%s public=%s\n", seedPath, publicPath)
			return err
		}}
	command.Flags().StringVar(&seedPath, "private-seed-out", "", "new private seed path (keep outside AgentSQL data volumes)")
	command.Flags().StringVar(&publicPath, "public-key-out", "", "new public key path")
	return command
}

func readCheckpointFile(path string, limit int64) ([]byte, error) {
	if path == "" {
		return nil, errors.New("file path is required")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > limit {
		return nil, errors.New("file type or size is invalid")
	}
	return os.ReadFile(path)
}

func readCheckpointKey(path string, size int) ([]byte, error) {
	data, err := readCheckpointFile(path, 256)
	if err != nil {
		return nil, err
	}
	decoded, err := hex.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(decoded) != size {
		return nil, errors.New("checkpoint key must be lowercase or uppercase hex of the required length")
	}
	return decoded, nil
}

func newAuditCreateCheckpointCommand() *cobra.Command {
	var archivePath, outputPath, keyPath, chainID, headHash, previous string
	var headSeq int64
	command := &cobra.Command{Use: "checkpoint-create", Short: "Sign a verified audit archive with an externally held Ed25519 key", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if outputPath == "" {
				return errors.New("audit checkpoint-create --out is required")
			}
			archive, err := readCheckpointFile(archivePath, compliance.MaximumArchiveBytes)
			if err != nil {
				return fmt.Errorf("read audit archive: %w", err)
			}
			seed, err := readCheckpointKey(keyPath, ed25519.SeedSize)
			if err != nil {
				return fmt.Errorf("read checkpoint private seed: %w", err)
			}
			checkpoint, err := compliance.SignArchiveCheckpoint(archive, chainID, headSeq, headHash, previous, time.Now().UTC(), ed25519.NewKeyFromSeed(seed))
			if err != nil {
				return err
			}
			contents, err := json.MarshalIndent(checkpoint, "", "  ")
			if err != nil {
				return err
			}
			if err := writeNewAuditFile(outputPath, append(contents, '\n')); err != nil {
				return err
			}
			digest, err := compliance.ArchiveCheckpointDigest(checkpoint)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "checkpoint written: path=%s sha256=%s chain=%s head_seq=%d\n", outputPath, digest, chainID, headSeq)
			return err
		}}
	command.Flags().StringVar(&archivePath, "in", "", "verified audit archive ZIP")
	command.Flags().StringVar(&outputPath, "out", "", "new checkpoint JSON file")
	command.Flags().StringVar(&keyPath, "private-seed-file", "", "external Ed25519 32-byte seed as hex")
	command.Flags().StringVar(&chainID, "chain-id", "", "verified audit chain ID")
	command.Flags().Int64Var(&headSeq, "head-seq", -1, "verified audit chain head sequence")
	command.Flags().StringVar(&headHash, "head-hash", "", "verified audit chain head hash (64 lowercase hex, blank for sequence zero)")
	command.Flags().StringVar(&previous, "previous-sha256", "", "trusted digest of the preceding checkpoint, if any")
	return command
}

func newAuditVerifyCheckpointCommand() *cobra.Command {
	var archivePath, checkpointPath, keyPath, previous string
	var maxAge time.Duration
	command := &cobra.Command{Use: "checkpoint-verify", Short: "Verify archive bytes, checkpoint signature, age, and trusted predecessor", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			archive, err := readCheckpointFile(archivePath, compliance.MaximumArchiveBytes)
			if err != nil {
				return fmt.Errorf("read audit archive: %w", err)
			}
			data, err := readCheckpointFile(checkpointPath, 4096)
			if err != nil {
				return fmt.Errorf("read checkpoint: %w", err)
			}
			var checkpoint compliance.ArchiveCheckpoint
			decoder := json.NewDecoder(strings.NewReader(string(data)))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&checkpoint); err != nil {
				return fmt.Errorf("decode checkpoint: %w", err)
			}
			var extra any
			if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
				return errors.New("checkpoint JSON has trailing data")
			}
			public, err := readCheckpointKey(keyPath, ed25519.PublicKeySize)
			if err != nil {
				return fmt.Errorf("read checkpoint public key: %w", err)
			}
			if err := compliance.VerifyArchiveCheckpoint(archive, checkpoint, ed25519.PublicKey(public), previous, time.Now().UTC(), maxAge); err != nil {
				return err
			}
			digest, err := compliance.ArchiveCheckpointDigest(checkpoint)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "checkpoint verified: sha256=%s chain=%s head_seq=%d\n", digest, checkpoint.ChainID, checkpoint.HeadSeq)
			return err
		}}
	command.Flags().StringVar(&archivePath, "in", "", "audit archive ZIP")
	command.Flags().StringVar(&checkpointPath, "checkpoint", "", "signed checkpoint JSON")
	command.Flags().StringVar(&keyPath, "public-key-file", "", "trusted Ed25519 32-byte public key as hex")
	command.Flags().StringVar(&previous, "expected-previous-sha256", "", "trusted predecessor digest (blank only for first checkpoint)")
	command.Flags().DurationVar(&maxAge, "max-age", 24*time.Hour, "maximum accepted checkpoint age")
	return command
}
