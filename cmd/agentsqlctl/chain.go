package main

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

const (
	chainDomainAuto       = "auto"
	chainDomainManagement = "management"
	chainDomainTraffic    = "traffic"
)

type chainCLI struct {
	configPath string
	domain     string
	json       bool
}

type staticChainManifest struct {
	mode    string
	version int
	key     []byte
}

func (manifest *staticChainManifest) ExpectedMode(context.Context) (string, error) {
	if err := manifest.validate(); err != nil {
		return "", err
	}
	return manifest.mode, nil
}

func (manifest *staticChainManifest) CurrentKeyVersion(context.Context) (int, error) {
	if err := manifest.validate(); err != nil {
		return 0, err
	}
	return manifest.version, nil
}

func (manifest *staticChainManifest) ChainKeyForVersion(_ context.Context, version int) ([]byte, error) {
	if err := manifest.validate(); err != nil {
		return nil, err
	}
	if manifest.mode != "hmac" || version != manifest.version || len(manifest.key) != 32 {
		return nil, fmt.Errorf("chain key version %d is unavailable", version)
	}
	return manifest.key, nil
}

func (manifest *staticChainManifest) validate() error {
	if manifest == nil {
		return errors.New("static chain manifest is required")
	}
	switch manifest.mode {
	case "keyless":
		if manifest.version != 0 || len(manifest.key) != 0 {
			return errors.New("keyless chain manifest requires version 0 and no key")
		}
	case "hmac":
		if manifest.version != 1 {
			return errors.New("hmac chain manifest requires version 1")
		}
	default:
		return fmt.Errorf("chain mode %q is invalid; expected keyless or hmac", manifest.mode)
	}
	return nil
}

type chainDatabase struct {
	db      *sql.DB
	dialect store.Dialect
	domain  string
	role    string
	target  config.ResolvedStoreTarget
}

type chainExitError struct {
	code   int
	result string
}

func (err *chainExitError) Error() string {
	return fmt.Sprintf("chain verification failed: result=%s", err.result)
}

func (err *chainExitError) ExitCode() int {
	return err.code
}

type chainStatusOutput struct {
	ChainID          string     `json:"chain_id"`
	Status           string     `json:"status"`
	Mode             *string    `json:"mode"`
	HeadSeq          int64      `json:"head_seq"`
	HeadID           *int64     `json:"head_id"`
	ProtectedSince   *int64     `json:"protected_since"`
	GenesisAt        *time.Time `json:"genesis_at"`
	ObservedInstance *string    `json:"observed_instance"`
	Result           *string    `json:"result"`
	LastVerifiedAt   *time.Time `json:"last_verified_at"`
	BreakSeq         *int64     `json:"break_seq"`
	BreakReason      *string    `json:"break_reason"`
}

type chainVerificationOutput struct {
	Result    string                  `json:"result"`
	HeadSeq   int64                   `json:"head_seq"`
	Total     int64                   `json:"total"`
	Unchained int64                   `json:"unchained"`
	Break     *chainVerificationBreak `json:"break"`
}

type chainVerificationBreak struct {
	Seq    int64  `json:"seq"`
	ID     int64  `json:"id"`
	Reason string `json:"reason"`
}

type chainProvisionOutput struct {
	Status       string                  `json:"status"`
	Verification chainVerificationOutput `json:"verification"`
}

func newChainCommand() *cobra.Command {
	cli := &chainCLI{}
	command := &cobra.Command{
		Use:   "chain",
		Short: "Operate audit-chain state",
		Args:  cobra.NoArgs,
	}
	command.PersistentFlags().StringVarP(&cli.configPath, "config", "c", defaultConfigPath, "path to the YAML configuration file")
	command.PersistentFlags().StringVar(&cli.domain, "domain", chainDomainAuto, "chain domain: auto, management, or traffic")
	command.PersistentFlags().BoolVar(&cli.json, "json", false, "print structured JSON output")
	command.AddCommand(cli.newStatusCommand(), cli.newVerifyCommand(), cli.newProvisionCommand())
	return command
}

func (cli *chainCLI) newStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Print audit-chain state and its latest verification",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			selected, state, err := cli.openAndReadState(command.Context())
			if err != nil {
				return err
			}
			defer selected.db.Close()
			verification, err := readChainVerification(command.Context(), selected)
			if err != nil {
				return safeChainDatabaseError("read chain verification", selected, err)
			}
			output := chainStatusOutput{
				ChainID:          state.ChainID,
				Status:           state.Status,
				Mode:             state.Mode,
				HeadSeq:          state.HeadSeq,
				HeadID:           state.HeadID,
				ProtectedSince:   state.ProtectedSinceID,
				GenesisAt:        state.GenesisAt,
				ObservedInstance: verification.ObservedInstanceID,
				Result:           verification.Result,
				LastVerifiedAt:   verification.LastVerifiedAt,
				BreakSeq:         verification.BreakSeq,
				BreakReason:      verification.BreakReason,
			}
			return writeChainStatus(command.OutOrStdout(), output, cli.json)
		},
	}
}

func (cli *chainCLI) newVerifyCommand() *cobra.Command {
	var mode, keyHex, keyEnv string
	command := &cobra.Command{
		Use:   "verify",
		Short: "Verify and persist an audit-chain observation",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			manifest, err := buildStaticChainManifest(command, mode, keyHex, keyEnv, true)
			if err != nil {
				return err
			}
			defer clearBytes(manifest.key)
			selected, _, err := cli.openAndReadState(command.Context())
			if err != nil {
				return err
			}
			defer selected.db.Close()
			outcome, verifyErr := store.NewChainVerifier(
				selected.db, selected.dialect, selected.domain, manifest,
			).VerifyAndPersist(command.Context())
			if err := writeChainVerification(command.OutOrStdout(), outcome, cli.json); err != nil {
				return err
			}
			return chainVerificationExit(outcome, verifyErr)
		},
	}
	command.Flags().StringVar(&mode, "mode", "", "trusted chain mode: keyless or hmac")
	command.Flags().StringVar(&keyHex, "key-hex", "", "32-byte HMAC key as exactly 64 hexadecimal characters")
	command.Flags().StringVar(&keyEnv, "key-env", "", "environment variable containing the 64-character hexadecimal HMAC key")
	_ = command.MarkFlagRequired("mode")
	return command
}

func (cli *chainCLI) newProvisionCommand() *cobra.Command {
	var mode, keyHex, keyEnv, owner string
	command := &cobra.Command{
		Use:   "provision",
		Short: "Provision and verify an audit chain",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			manifest, err := buildStaticChainManifest(command, mode, keyHex, keyEnv, false)
			if err != nil {
				return err
			}
			defer clearBytes(manifest.key)
			selected, _, err := cli.openAndReadState(command.Context())
			if err != nil {
				return err
			}
			defer selected.db.Close()
			buildOwner := strings.TrimSpace(owner)
			if buildOwner == "" {
				buildOwner = "agentsqlctl-" + uuid.NewString()[:8]
			}
			provisioner := store.NewChainProvisioner(
				selected.db, selected.dialect, selected.domain, manifest, store.BackfillConfig{},
			)
			if err := provisioner.Provision(command.Context(), buildOwner); err != nil {
				return safeChainDatabaseError("provision chain", selected, err)
			}
			outcome, verifyErr := store.NewChainVerifier(
				selected.db, selected.dialect, selected.domain, manifest,
			).VerifyAndPersist(command.Context())
			if cli.json {
				if err := writeChainJSON(command.OutOrStdout(), chainProvisionOutput{
					Status: "ACTIVE", Verification: verificationOutput(outcome),
				}); err != nil {
					return err
				}
			} else {
				if _, err := fmt.Fprintln(command.OutOrStdout(), "status=ACTIVE"); err != nil {
					return fmt.Errorf("write chain provision status: %w", err)
				}
				if err := writeChainVerification(command.OutOrStdout(), outcome, false); err != nil {
					return err
				}
			}
			return chainVerificationExit(outcome, verifyErr)
		},
	}
	command.Flags().StringVar(&mode, "mode", "", "trusted chain mode: keyless or hmac")
	command.Flags().StringVar(&keyHex, "key-hex", "", "32-byte HMAC key as exactly 64 hexadecimal characters")
	command.Flags().StringVar(&keyEnv, "key-env", "", "environment variable containing the 64-character hexadecimal HMAC key")
	command.Flags().StringVar(&owner, "owner", "", "build lease owner (defaults to agentsqlctl-<short-uuid>)")
	_ = command.MarkFlagRequired("mode")
	return command
}

func buildStaticChainManifest(command *cobra.Command, mode, keyHex, keyEnv string, allowMissingKey bool) (*staticChainManifest, error) {
	if mode != "keyless" && mode != "hmac" {
		return nil, fmt.Errorf("chain --mode must be keyless or hmac")
	}
	hexSet := command.Flags().Changed("key-hex")
	envSet := command.Flags().Changed("key-env")
	if hexSet && envSet {
		return nil, errors.New("chain --key-hex and --key-env are mutually exclusive")
	}
	if mode == "keyless" {
		if hexSet || envSet {
			return nil, errors.New("keyless chain mode does not accept --key-hex or --key-env")
		}
		return &staticChainManifest{mode: mode, version: 0}, nil
	}

	manifest := &staticChainManifest{mode: mode, version: 1}
	if !hexSet && !envSet {
		if allowMissingKey {
			return manifest, nil
		}
		return nil, errors.New("hmac chain provision requires --key-hex or --key-env")
	}
	encoded := keyHex
	if envSet {
		name := strings.TrimSpace(keyEnv)
		if name == "" {
			return nil, errors.New("chain --key-env requires a non-empty environment variable name")
		}
		var found bool
		encoded, found = os.LookupEnv(name)
		if !found || encoded == "" {
			if allowMissingKey {
				return manifest, nil
			}
			return nil, fmt.Errorf("hmac chain key environment variable %q is not set", name)
		}
	}
	if len(encoded) != 64 {
		return nil, errors.New("chain HMAC key must contain exactly 64 hexadecimal characters")
	}
	key, err := hex.DecodeString(encoded)
	if err != nil {
		clearBytes(key)
		return nil, errors.New("chain HMAC key must contain only hexadecimal characters")
	}
	manifest.key = key
	return manifest, nil
}

func (cli *chainCLI) openAndReadState(ctx context.Context) (*chainDatabase, store.ChainState, error) {
	selected, err := cli.openDatabase(ctx)
	if err != nil {
		return nil, store.ChainState{}, err
	}
	state, err := store.NewChainProvisioner(
		selected.db, selected.dialect, selected.domain, nil, store.BackfillConfig{},
	).Status(ctx)
	if err != nil {
		_ = selected.db.Close()
		if errors.Is(err, store.ErrNotFound) {
			return nil, store.ChainState{}, fmt.Errorf(
				"chain_state row for domain %q does not exist in selected %s database",
				selected.domain, selected.role,
			)
		}
		return nil, store.ChainState{}, safeChainDatabaseError("read chain state", selected, err)
	}
	return selected, state, nil
}

func (cli *chainCLI) openDatabase(ctx context.Context) (*chainDatabase, error) {
	resolved, err := resolveConfigFile(cli.configPath)
	if err != nil {
		return nil, fmt.Errorf("load chain configuration: %w", err)
	}
	domain := cli.domain
	if domain != chainDomainAuto && domain != chainDomainManagement && domain != chainDomainTraffic {
		return nil, fmt.Errorf("chain --domain must be auto, management, or traffic")
	}
	if domain == chainDomainAuto {
		domain = chainDomainManagement
		if !resolved.Audit.ReuseMetadata {
			domain = chainDomainTraffic
		}
	}
	role := "metadata"
	target := resolved.Metadata
	if domain == chainDomainTraffic && !resolved.Audit.ReuseMetadata {
		role = "audit"
		target = resolved.Audit
	}
	database, err := openResolvedDatabase(target)
	if err != nil {
		selected := &chainDatabase{dialect: target.Driver, domain: domain, role: role, target: target}
		return nil, safeChainDatabaseError("open chain database", selected, err)
	}
	selected := &chainDatabase{db: database, dialect: target.Driver, domain: domain, role: role, target: target}
	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		return nil, safeChainDatabaseError("ping chain database", selected, err)
	}
	return selected, nil
}

func readChainVerification(ctx context.Context, selected *chainDatabase) (store.ChainVerification, error) {
	query := `
SELECT chain_id, observed_instance_id, observed_head_hash, result,
       last_verified_head_seq, last_verified_at, break_seq, break_id, break_reason
FROM chain_verification
WHERE chain_id = ?`
	if selected.dialect == store.DialectPostgres {
		query = strings.Replace(query, "?", "$1", 1)
	}
	var verification store.ChainVerification
	var observedInstance, observedHash, result, breakReason sql.NullString
	var lastHeadSeq, breakSeq, breakID sql.NullInt64
	var lastVerifiedAt sql.NullTime
	err := selected.db.QueryRowContext(ctx, query, selected.domain).Scan(
		&verification.ChainID,
		&observedInstance,
		&observedHash,
		&result,
		&lastHeadSeq,
		&lastVerifiedAt,
		&breakSeq,
		&breakID,
		&breakReason,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ChainVerification{ChainID: selected.domain}, nil
	}
	if err != nil {
		return store.ChainVerification{}, err
	}
	verification.ObservedInstanceID = nullableStringPointer(observedInstance)
	verification.ObservedHeadHash = nullableStringPointer(observedHash)
	verification.Result = nullableStringPointer(result)
	verification.LastVerifiedHeadSeq = nullableInt64Pointer(lastHeadSeq)
	if lastVerifiedAt.Valid {
		verification.LastVerifiedAt = &lastVerifiedAt.Time
	}
	verification.BreakSeq = nullableInt64Pointer(breakSeq)
	verification.BreakID = nullableInt64Pointer(breakID)
	verification.BreakReason = nullableStringPointer(breakReason)
	return verification, nil
}

func nullableStringPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func nullableInt64Pointer(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	return &value.Int64
}

func safeChainDatabaseError(operation string, selected *chainDatabase, err error) error {
	if selected.target.Driver == store.DialectPostgres {
		return fmt.Errorf("%s for domain %q in selected %s database: operation failed", operation, selected.domain, selected.role)
	}
	return fmt.Errorf("%s for domain %q in selected %s database: %w", operation, selected.domain, selected.role, err)
}

func writeChainStatus(writer io.Writer, output chainStatusOutput, structured bool) error {
	if structured {
		return writeChainJSON(writer, output)
	}
	_, err := fmt.Fprintf(
		writer,
		"chain_id=%s status=%s mode=%s head_seq=%d head_id=%s protected_since=%s genesis_at=%s observed_instance=%s result=%s last_verified_at=%s break_seq=%s break_reason=%s\n",
		output.ChainID,
		output.Status,
		formatStringPointer(output.Mode),
		output.HeadSeq,
		formatInt64Pointer(output.HeadID),
		formatInt64Pointer(output.ProtectedSince),
		formatTimePointer(output.GenesisAt),
		formatStringPointer(output.ObservedInstance),
		formatStringPointer(output.Result),
		formatTimePointer(output.LastVerifiedAt),
		formatInt64Pointer(output.BreakSeq),
		formatStringPointer(output.BreakReason),
	)
	if err != nil {
		return fmt.Errorf("write chain status: %w", err)
	}
	return nil
}

func writeChainVerification(writer io.Writer, outcome store.ChainVerificationOutcome, structured bool) error {
	output := verificationOutput(outcome)
	if structured {
		return writeChainJSON(writer, output)
	}
	breakSeq, breakID, breakReason := "null", "null", "null"
	if output.Break != nil {
		breakSeq = fmt.Sprint(output.Break.Seq)
		breakID = fmt.Sprint(output.Break.ID)
		breakReason = output.Break.Reason
	}
	if _, err := fmt.Fprintf(
		writer,
		"result=%s head_seq=%d total=%d unchained=%d break_seq=%s break_id=%s break_reason=%s\n",
		output.Result, output.HeadSeq, output.Total, output.Unchained, breakSeq, breakID, breakReason,
	); err != nil {
		return fmt.Errorf("write chain verification: %w", err)
	}
	return nil
}

func verificationOutput(outcome store.ChainVerificationOutcome) chainVerificationOutput {
	output := chainVerificationOutput{
		Result: outcome.Result, HeadSeq: outcome.HeadSeq, Total: outcome.TotalRows, Unchained: outcome.Unchained,
	}
	if outcome.BreakReason != "" {
		output.Break = &chainVerificationBreak{
			Seq: outcome.BreakSeq, ID: outcome.BreakID, Reason: outcome.BreakReason,
		}
	}
	return output
}

func writeChainJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("write chain JSON: %w", err)
	}
	return nil
}

func chainVerificationExit(outcome store.ChainVerificationOutcome, verifyErr error) error {
	code := store.ExitCodeForVerification(outcome, verifyErr)
	if code == 0 {
		return nil
	}
	result := outcome.Result
	if result == "" {
		result = "unknown"
	}
	return &chainExitError{code: code, result: result}
}

func formatStringPointer(value *string) string {
	if value == nil {
		return "null"
	}
	return *value
}

func formatInt64Pointer(value *int64) string {
	if value == nil {
		return "null"
	}
	return fmt.Sprint(*value)
}

func formatTimePointer(value *time.Time) string {
	if value == nil {
		return "null"
	}
	return value.UTC().Format(time.RFC3339Nano)
}
