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

	"github.com/cuipengdba/agentsql/internal/policybundle"
	"github.com/spf13/cobra"
)

const maxPolicyTransferInput = 512 << 10

type policyTransferSnapshot struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		Bundle policybundle.Bundle `json:"bundle"`
		Digest string              `json:"digest"`
	} `json:"data"`
}

func readPolicyTransferJSON(path string, target any) error {
	contents, err := readCheckpointFile(path, maxPolicyTransferInput)
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
		return errors.New("policy transfer JSON has trailing data")
	}
	return nil
}

func readPolicyTransferSnapshot(path string) (policybundle.Bundle, string, error) {
	var snapshot policyTransferSnapshot
	if err := readPolicyTransferJSON(path, &snapshot); err != nil {
		return policybundle.Bundle{}, "", err
	}
	if snapshot.Code != 0 || snapshot.Data.Digest == "" {
		return policybundle.Bundle{}, "", errors.New("policy snapshot response is invalid")
	}
	digest, err := policybundle.Digest(snapshot.Data.Bundle)
	if err != nil || digest != snapshot.Data.Digest {
		return policybundle.Bundle{}, "", errors.New("policy snapshot digest differs from bundle")
	}
	return snapshot.Data.Bundle, digest, nil
}

func newPolicyTransferCommand() *cobra.Command {
	command := &cobra.Command{Use: "policy-transfer", Short: "Sign and plan an offline, tenant-scoped policy transfer"}
	command.AddCommand(newPolicyTransferKeygenCommand())
	command.AddCommand(newPolicyTransferSignCommand())
	command.AddCommand(newPolicyTransferPlanCommand())
	return command
}

func newPolicyTransferKeygenCommand() *cobra.Command {
	var seedPath, publicPath string
	command := &cobra.Command{Use: "keygen", Short: "Create an external Ed25519 policy transfer key pair", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if seedPath == "" || publicPath == "" || seedPath == publicPath {
				return errors.New("two distinct --private-seed-out and --public-key-out paths are required")
			}
			for _, path := range []string{seedPath, publicPath} {
				if _, err := os.Stat(path); err == nil {
					return fmt.Errorf("policy transfer key output already exists: %s", path)
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
			_, err = fmt.Fprintf(command.OutOrStdout(), "policy transfer keys written: private=%s public=%s\n", seedPath, publicPath)
			return err
		}}
	command.Flags().StringVar(&seedPath, "private-seed-out", "", "new external private seed path")
	command.Flags().StringVar(&publicPath, "public-key-out", "", "new public key path")
	return command
}

func newPolicyTransferSignCommand() *cobra.Command {
	var snapshotPath, sourceEnvironment, seedPath, outputPath string
	command := &cobra.Command{Use: "sign", Short: "Sign a simple-table policy snapshot outside the gateway", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if outputPath == "" || sourceEnvironment == "" {
				return errors.New("--out and --source-environment are required")
			}
			bundle, _, err := readPolicyTransferSnapshot(snapshotPath)
			if err != nil {
				return fmt.Errorf("read source policy snapshot: %w", err)
			}
			seed, err := readCheckpointKey(seedPath, ed25519.SeedSize)
			if err != nil {
				return fmt.Errorf("read external policy transfer seed: %w", err)
			}
			transfer, err := policybundle.SignTransfer(bundle, sourceEnvironment, time.Now().UTC(), ed25519.NewKeyFromSeed(seed))
			if err != nil {
				return err
			}
			contents, err := json.MarshalIndent(transfer, "", "  ")
			if err != nil {
				return err
			}
			if err := writeNewAuditFile(outputPath, append(contents, '\n')); err != nil {
				return err
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "policy transfer signed: path=%s source=%s digest=%s\n", outputPath, sourceEnvironment, transfer.Payload.BundleDigest)
			return err
		}}
	command.Flags().StringVar(&snapshotPath, "snapshot", "", "source GET /api/v1/policy-bundles/current JSON response")
	command.Flags().StringVar(&sourceEnvironment, "source-environment", "", "operator-chosen source environment identity")
	command.Flags().StringVar(&seedPath, "private-seed-file", "", "external Ed25519 32-byte seed as hex")
	command.Flags().StringVar(&outputPath, "out", "", "new signed policy transfer JSON")
	return command
}

func newPolicyTransferPlanCommand() *cobra.Command {
	var transferPath, snapshotPath, mappingPath, keyPath, sourceEnvironment, outputPath string
	command := &cobra.Command{Use: "plan", Short: "Verify a signed transfer and prepare an existing policy proposal", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if sourceEnvironment == "" || outputPath == "" {
				return errors.New("--source-environment and --out are required")
			}
			var transfer policybundle.SignedTransfer
			if err := readPolicyTransferJSON(transferPath, &transfer); err != nil {
				return fmt.Errorf("read signed policy transfer: %w", err)
			}
			if transfer.Payload.SourceEnvironment != sourceEnvironment {
				return errors.New("policy transfer source environment differs from trusted selection")
			}
			public, err := readCheckpointKey(keyPath, ed25519.PublicKeySize)
			if err != nil {
				return fmt.Errorf("read trusted policy transfer public key: %w", err)
			}
			current, digest, err := readPolicyTransferSnapshot(snapshotPath)
			if err != nil {
				return fmt.Errorf("read target policy snapshot: %w", err)
			}
			var mapping policybundle.TransferMapping
			if err := readPolicyTransferJSON(mappingPath, &mapping); err != nil {
				return fmt.Errorf("read explicit policy mapping: %w", err)
			}
			desired, changes, err := policybundle.PlanTransfer(transfer, ed25519.PublicKey(public), time.Now().UTC(), current, mapping)
			if err != nil {
				return err
			}
			output := struct {
				BaseDigest string              `json:"base_digest"`
				Desired    policybundle.Bundle `json:"desired"`
			}{BaseDigest: digest, Desired: desired}
			contents, err := json.MarshalIndent(output, "", "  ")
			if err != nil {
				return err
			}
			if err := writeNewAuditFile(outputPath, append(contents, '\n')); err != nil {
				return err
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "policy transfer planned: path=%s changes=%d base=%s; submit for live plan and separate-person review\n", outputPath, len(changes), digest)
			return err
		}}
	command.Flags().StringVar(&transferPath, "transfer", "", "signed source policy transfer JSON")
	command.Flags().StringVar(&snapshotPath, "target-snapshot", "", "target GET /api/v1/policy-bundles/current JSON response")
	command.Flags().StringVar(&mappingPath, "mapping", "", "explicit target tenant and identity mapping JSON")
	command.Flags().StringVar(&keyPath, "public-key-file", "", "trusted Ed25519 32-byte public key as hex")
	command.Flags().StringVar(&sourceEnvironment, "source-environment", "", "expected signed source environment identity")
	command.Flags().StringVar(&outputPath, "out", "", "new JSON for existing plan/proposal endpoints")
	return command
}
