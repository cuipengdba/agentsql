// Command agentsqlctl provides AgentSQL operational utilities.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/cuipengdba/agentsql/internal/version"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/spf13/cobra"
	_ "modernc.org/sqlite"
)

const (
	defaultConfigPath = "config.yaml"
	defaultHealthURL  = "http://127.0.0.1:7780/healthz"
	maxHealthBodySize = 1 << 20
)

const defaultConfigTemplate = `server:
  http_listen: "127.0.0.1:7780"
  console_enabled: true
  event_stream: true
  event_stream_max_connections: 100
store:
  sqlite_path: "./data/agentsql.db"
  auto_migrate: true              # false verifies schema versions without DDL
  # The v0.1 shorthand above and metadata below are mutually exclusive.
  # metadata:
  #   driver: sqlite                 # sqlite or postgres; defaults to sqlite
  #   sqlite_path: "./data/agentsql.db"
  #   dsn: ""                       # for postgres; AGENTSQL_STORE_METADATA_DSN overrides this value
  #   max_open_conns: 10
  #   max_idle_conns: 5
  #   conn_max_lifetime: "30m"      # Go duration syntax with a unit, for example 30m or 1h
  # audit:
  #   separate: false
  #   driver: postgres               # independent audit stores support postgres only
  #   dsn: ""                       # AGENTSQL_STORE_AUDIT_DSN overrides this value
  #   max_open_conns: 10
  #   max_idle_conns: 5
defaults:
  statement_timeout_ms: 5000
  row_limit: 1000
  max_conns_per_datasource: 5
  qps_per_agent: 20
theme:
  default: "dark"
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	command := newRootCommand()
	command.SetArgs(args)
	command.SetOut(stdout)
	command.SetErr(stderr)
	if err := command.Execute(); err != nil {
		if _, writeErr := fmt.Fprintln(stderr, err); writeErr != nil {
			return 1
		}
		var codedError interface{ ExitCode() int }
		if errors.As(err, &codedError) {
			return codedError.ExitCode()
		}
		if errors.Is(err, errManagementAuditPending) {
			return 3
		}
		return 1
	}
	return 0
}

func newRootCommand() *cobra.Command {
	command := &cobra.Command{
		Use:           "agentsqlctl",
		Short:         "AgentSQL operational utility",
		Version:       version.Version,
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	command.SetVersionTemplate("{{.Version}}\n")
	command.AddCommand(newVersionCommand())
	command.AddCommand(newInitConfigCommand())
	command.AddCommand(newCheckConfigCommand())
	command.AddCommand(newMigrateCommand())
	command.AddCommand(newSQLiteToPostgresCommand())
	command.AddCommand(newHealthCommand())
	command.AddCommand(newDemoSeedCommand(defaultDemoSeedDependencies()))
	command.AddCommand(newRedactionKeyCommand())
	command.AddCommand(newChainCommand())
	command.AddCommand(newAuditCommand())
	command.AddCommand(newUpgradeCommand())
	return command
}

func newSQLiteToPostgresCommand() *cobra.Command {
	var sourcePath, targetConfigPath string
	var verifyHash bool
	command := &cobra.Command{
		Use:   "migrate-sqlite-to-postgres",
		Short: "Copy a combined SQLite store into PostgreSQL",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if _, err := fmt.Fprintln(command.ErrOrStderr(), "notice=stop-gateway-writes-during-migration"); err != nil {
				return fmt.Errorf("write migration notice: %w", err)
			}
			summary, err := migrateSQLiteToPostgres(command.Context(), sourcePath, targetConfigPath, verifyHash)
			if err != nil {
				return err
			}
			return writeSQLiteToPostgresSummary(command.OutOrStdout(), summary)
		},
	}
	command.Flags().StringVar(&sourcePath, "source", "", "path to the combined SQLite database")
	command.Flags().StringVar(&targetConfigPath, "target-config", "", "path to the PostgreSQL target configuration")
	command.Flags().BoolVar(&verifyHash, "verify-hash", false, "request canonical streaming SHA-256 verification")
	_ = command.MarkFlagRequired("source")
	_ = command.MarkFlagRequired("target-config")
	return command
}

func migrateSQLiteToPostgres(ctx context.Context, sourcePath, targetConfigPath string, verifyHash bool) (summary store.SQLiteToPostgresSummary, resultErr error) {
	resolved, err := resolveConfigFile(targetConfigPath)
	if err != nil {
		return summary, fmt.Errorf("load PostgreSQL migration target: %w", err)
	}
	if resolved.Metadata.Driver != store.DialectPostgres {
		if resolved.Metadata.Driver == store.DialectSQLite && sameFilePath(sourcePath, resolved.Metadata.SQLitePath) {
			return summary, errors.New("migration source and target refer to the same SQLite file")
		}
		return summary, errors.New("migration target metadata driver must be postgres")
	}
	if !resolved.Audit.ReuseMetadata && resolved.Audit.Driver != store.DialectPostgres {
		return summary, errors.New("migration target audit driver must be postgres")
	}

	metadataDB, err := openResolvedDatabase(resolved.Metadata)
	if err != nil {
		return summary, safeStoreError("open metadata migration target", resolved.Metadata, err)
	}
	defer func() {
		if err := metadataDB.Close(); err != nil && resultErr == nil {
			resultErr = safeStoreError("close metadata migration target", resolved.Metadata, err)
		}
	}()
	auditDB := metadataDB
	if !resolved.Audit.ReuseMetadata {
		auditDB, err = openResolvedDatabase(resolved.Audit)
		if err != nil {
			return summary, safeStoreError("open audit migration target", resolved.Audit, err)
		}
		defer func() {
			if err := auditDB.Close(); err != nil && resultErr == nil {
				resultErr = safeStoreError("close audit migration target", resolved.Audit, err)
			}
		}()
	}

	summary, err = store.MigrateSQLiteToPostgres(ctx, store.SQLiteToPostgresOptions{
		SourcePath: sourcePath,
		MetadataDB: metadataDB,
		AuditDB:    auditDB,
		Separate:   !resolved.Audit.ReuseMetadata,
		VerifyHash: verifyHash,
	})
	if err != nil {
		return summary, err
	}
	return summary, nil
}

func sameFilePath(left, right string) bool {
	leftAbsolute, leftErr := filepath.Abs(strings.TrimSpace(left))
	rightAbsolute, rightErr := filepath.Abs(strings.TrimSpace(right))
	if leftErr != nil || rightErr != nil {
		return false
	}
	leftInfo, leftStatErr := os.Stat(leftAbsolute)
	rightInfo, rightStatErr := os.Stat(rightAbsolute)
	if leftStatErr == nil && rightStatErr == nil {
		return os.SameFile(leftInfo, rightInfo)
	}
	return strings.EqualFold(filepath.Clean(leftAbsolute), filepath.Clean(rightAbsolute))
}

func writeSQLiteToPostgresSummary(writer io.Writer, summary store.SQLiteToPostgresSummary) error {
	for _, table := range summary.Tables {
		if _, err := fmt.Fprintf(writer, "table=%s source_rows=%d target_rows=%d hash=%s", table.Table, table.SourceRows, table.TargetRows, table.SHA256); err != nil {
			return fmt.Errorf("write SQLite-to-PostgreSQL summary: %w", err)
		}
		if table.Table == "audit_logs" {
			minID, maxID := "null", "null"
			if table.MinID != nil {
				minID = fmt.Sprint(*table.MinID)
			}
			if table.MaxID != nil {
				maxID = fmt.Sprint(*table.MaxID)
			}
			if _, err := fmt.Fprintf(writer, " min_id=%s max_id=%s", minID, maxID); err != nil {
				return fmt.Errorf("write SQLite-to-PostgreSQL summary: %w", err)
			}
		}
		if _, err := fmt.Fprintln(writer); err != nil {
			return fmt.Errorf("write SQLite-to-PostgreSQL summary: %w", err)
		}
	}
	if _, err := fmt.Fprintf(writer, "sequence=public.audit_logs_id_seq last_value=%d is_called=%t expected_next=%d\n", summary.Sequence.LastValue, summary.Sequence.IsCalled, summary.Sequence.ExpectedNext); err != nil {
		return fmt.Errorf("write SQLite-to-PostgreSQL summary: %w", err)
	}
	if _, err := fmt.Fprintf(writer, "approvals non_null_audit_id_source=%d non_null_audit_id_target=%d source_orphans=%d target_orphans=%d\n", summary.Approvals.SourceNonNullAuditIDs, summary.Approvals.TargetNonNullAuditIDs, summary.Approvals.SourceOrphans, summary.Approvals.TargetOrphans); err != nil {
		return fmt.Errorf("write SQLite-to-PostgreSQL summary: %w", err)
	}
	if _, err := fmt.Fprintf(writer, "verification=%s verify_hash=%t\n", summary.Verification, summary.VerifyHash); err != nil {
		return fmt.Errorf("write SQLite-to-PostgreSQL summary: %w", err)
	}
	steps := []string{
		"stop-old-service-and-back-up-sqlite-and-secret",
		"switch-config-to-postgres-layout",
		"replace-migration-dsns-with-runtime-accounts",
		"set-auto-migrate-false",
		"keep-original-agentsql-secret",
		"run-check-config-and-health",
		"restart-and-confirm-readyz-200",
		"verify-read-deny-approve-and-new-audit-id-above-old-max",
		"retain-sqlite-through-validation-period",
	}
	for index, step := range steps {
		if _, err := fmt.Fprintf(writer, "switch_step=%d action=%s\n", index+1, step); err != nil {
			return fmt.Errorf("write SQLite-to-PostgreSQL switch steps: %w", err)
		}
	}
	return nil
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the AgentSQL version",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if _, err := fmt.Fprintln(command.OutOrStdout(), version.Version); err != nil {
				return fmt.Errorf("write version: %w", err)
			}
			return nil
		},
	}
}

func newInitConfigCommand() *cobra.Command {
	var outputPath string
	var force bool
	command := &cobra.Command{
		Use:   "init-config",
		Short: "Write a default AgentSQL configuration",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if err := writeInitialConfig(outputPath, force); err != nil {
				return err
			}
			if _, err := fmt.Fprintf(command.OutOrStdout(), "config written: %s\n", filepath.Clean(outputPath)); err != nil {
				return fmt.Errorf("write init-config result: %w", err)
			}
			return nil
		},
	}
	command.Flags().StringVarP(&outputPath, "output", "o", defaultConfigPath, "configuration output path")
	command.Flags().BoolVar(&force, "force", false, "overwrite an existing configuration")
	return command
}

func writeInitialConfig(outputPath string, force bool) error {
	if strings.TrimSpace(outputPath) == "" {
		return fmt.Errorf("init-config output path is required")
	}
	cleaned := filepath.Clean(outputPath)
	if err := os.MkdirAll(filepath.Dir(cleaned), 0o750); err != nil {
		return fmt.Errorf("create config directory for %q: %w", cleaned, err)
	}
	flags := os.O_WRONLY | os.O_CREATE
	if force {
		flags |= os.O_TRUNC
	} else {
		flags |= os.O_EXCL
	}
	file, err := os.OpenFile(cleaned, flags, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) && !force {
			return fmt.Errorf("config %q already exists; use --force to overwrite", cleaned)
		}
		return fmt.Errorf("create config %q: %w", cleaned, err)
	}
	_, writeErr := io.WriteString(file, defaultConfigTemplate)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return fmt.Errorf("write config %q: %w", cleaned, errors.Join(writeErr, closeErr))
	}
	return nil
}

func newCheckConfigCommand() *cobra.Command {
	var configPath string
	command := &cobra.Command{
		Use:   "check-config",
		Short: "Validate a configuration without changing the filesystem",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			contents, err := os.ReadFile(configPath)
			if err != nil {
				return fmt.Errorf("read config %q: %w", configPath, err)
			}
			loaded, err := config.Parse(contents)
			if err != nil {
				return fmt.Errorf("check config %q: %w", configPath, err)
			}
			resolvedRedaction, err := config.ResolveRedaction(&loaded, os.LookupEnv)
			if err != nil {
				return fmt.Errorf("check config %q: %w", configPath, err)
			}
			defer resolvedRedaction.Clear()
			redactionAssembly, err := config.BuildRedactionAssembly(resolvedRedaction)
			if err != nil {
				return fmt.Errorf("check config %q: %w", configPath, err)
			}
			resolved, err := config.ResolveStore(&loaded, os.LookupEnv)
			if err != nil {
				return fmt.Errorf("check config %q: %w", configPath, err)
			}
			message := fmt.Sprintf(
				"config ok: metadata_driver=%s audit_driver=%s audit_separate=%t",
				resolved.Metadata.Driver,
				resolved.Audit.Driver,
				!resolved.Audit.ReuseMetadata,
			)
			if resolved.Metadata.Driver == store.DialectSQLite {
				message += " sqlite_path=" + resolved.Metadata.SQLitePath
			}
			redactionStatus := redactionAssembly.Observed.Status
			if redactionStatus == "" {
				redactionStatus = "unavailable"
			}
			message += fmt.Sprintf(" redaction_status=%s redaction_mode=%s active_version=%d key_count=%d revision=%s",
				redactionStatus, redactionAssembly.Observed.Mode, redactionAssembly.Observed.ActiveVersion,
				len(redactionAssembly.Observed.Keys), redactionAssembly.Observed.Revision)
			message += " registry_retired_and_drift_checks=not_run"
			if _, err := fmt.Fprintln(command.OutOrStdout(), message); err != nil {
				return fmt.Errorf("write check-config result: %w", err)
			}
			return nil
		},
	}
	command.Flags().StringVarP(&configPath, "config", "c", defaultConfigPath, "path to the YAML configuration file")
	return command
}

func newMigrateCommand() *cobra.Command {
	var configPath string
	command := &cobra.Command{
		Use:   "migrate",
		Short: "Apply metadata-store migrations",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			results, err := migrate(command.Context(), configPath)
			if err != nil {
				return err
			}
			for _, result := range results {
				format := "migration driver=%s current=%d latest=%d\n"
				arguments := []any{result.driver, result.current, result.latest}
				if len(results) > 1 {
					format = result.role + " migration driver=%s current=%d latest=%d\n"
				}
				if _, err := fmt.Fprintf(command.OutOrStdout(), format, arguments...); err != nil {
					return fmt.Errorf("write migration result: %w", err)
				}
			}
			return nil
		},
	}
	command.Flags().StringVarP(&configPath, "config", "c", defaultConfigPath, "path to the YAML configuration file")
	return command
}

type migrationResult struct {
	role            string
	driver          store.Dialect
	current, latest int
}

func migrate(ctx context.Context, configPath string) ([]migrationResult, error) {
	loaded, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("load migration configuration: %w", err)
	}
	resolved, err := config.ResolveStore(&loaded, os.LookupEnv)
	if err != nil {
		return nil, fmt.Errorf("resolve migration configuration: %w", err)
	}
	separate := !resolved.Audit.ReuseMetadata
	metadataResult, err := migrateResolvedTarget(ctx, "metadata", resolved.Metadata, func(database *sql.DB) error {
		return store.MigrateMetadata(ctx, database, resolved.Metadata.Driver, separate)
	}, func(database *sql.DB) (int, int, error) {
		return store.MetadataMigrationVersions(ctx, database, resolved.Metadata.Driver, separate)
	})
	if err != nil {
		return nil, err
	}
	if !separate {
		return []migrationResult{metadataResult}, nil
	}
	auditResult, err := migrateResolvedTarget(ctx, "audit", resolved.Audit, func(database *sql.DB) error {
		return store.MigrateAudit(ctx, database, resolved.Audit.Driver)
	}, func(database *sql.DB) (int, int, error) {
		return store.AuditMigrationVersions(ctx, database, resolved.Audit.Driver)
	})
	if err != nil {
		return nil, err
	}
	return []migrationResult{metadataResult, auditResult}, nil
}

func migrateResolvedTarget(
	ctx context.Context,
	role string,
	target config.ResolvedStoreTarget,
	apply func(*sql.DB) error,
	versions func(*sql.DB) (int, int, error),
) (result migrationResult, resultErr error) {
	result.role, result.driver = role, target.Driver
	database, err := openResolvedDatabase(target)
	if err != nil {
		return result, safeStoreError("open "+role+" migration connection", target, err)
	}
	defer func() {
		if closeErr := database.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, safeStoreError("close "+role+" migration connection", target, closeErr))
		}
	}()
	if err := database.PingContext(ctx); err != nil {
		return result, safeStoreError("ping "+role+" migration connection", target, err)
	}
	if err := apply(database); err != nil {
		return result, safeStoreError("migrate "+role+" store", target, err)
	}
	result.current, result.latest, err = versions(database)
	if err != nil {
		return result, safeStoreError("read "+role+" migration version", target, err)
	}
	return result, nil
}

func newHealthCommand() *cobra.Command {
	var url string
	var configPath string
	var timeout time.Duration
	command := &cobra.Command{
		Use:   "health",
		Short: "Check an AgentSQL liveness endpoint",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if timeout <= 0 {
				return fmt.Errorf("health timeout must be positive")
			}
			if command.Flags().Changed("url") && command.Flags().Changed("config") {
				return fmt.Errorf("health --url and --config are mutually exclusive")
			}
			if command.Flags().Changed("config") {
				resolved, err := resolveConfigFile(configPath)
				if err != nil {
					return fmt.Errorf("load health configuration: %w", err)
				}
				if err := checkStoreHealth(command.Context(), resolved); err != nil {
					return err
				}
				if _, err := fmt.Fprintf(
					command.OutOrStdout(),
					"health ok: metadata_driver=%s audit_driver=%s audit_separate=%t\n",
					resolved.Metadata.Driver,
					resolved.Audit.Driver,
					!resolved.Audit.ReuseMetadata,
				); err != nil {
					return fmt.Errorf("write health result: %w", err)
				}
				return nil
			}
			body, err := checkHealth(command.Context(), url, timeout)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintln(command.OutOrStdout(), strings.TrimSpace(string(body))); err != nil {
				return fmt.Errorf("write health result: %w", err)
			}
			return nil
		},
	}
	command.Flags().StringVar(&url, "url", defaultHealthURL, "liveness endpoint URL")
	command.Flags().StringVarP(&configPath, "config", "c", "", "path to a metadata-store configuration file")
	command.Flags().DurationVar(&timeout, "timeout", 3*time.Second, "health request timeout")
	return command
}

func resolveConfigFile(configPath string) (*config.ResolvedStore, error) {
	if strings.TrimSpace(configPath) == "" {
		return nil, fmt.Errorf("configuration path is required")
	}
	contents, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", configPath, err)
	}
	loaded, err := config.Parse(contents)
	if err != nil {
		return nil, fmt.Errorf("parse config %q: %w", configPath, err)
	}
	resolved, err := config.ResolveStore(&loaded, os.LookupEnv)
	if err != nil {
		return nil, fmt.Errorf("resolve config %q: %w", configPath, err)
	}
	return resolved, nil
}

func openResolvedDatabase(resolved config.ResolvedStoreTarget) (*sql.DB, error) {
	driverName, target := "sqlite", resolved.SQLitePath
	if resolved.Driver == store.DialectPostgres {
		driverName, target = "pgx", resolved.PostgresDSN
	}
	database, err := sql.Open(driverName, target)
	if err != nil {
		return nil, err
	}
	if resolved.Driver == store.DialectSQLite {
		database.SetMaxOpenConns(1)
		database.SetMaxIdleConns(1)
	} else {
		database.SetMaxOpenConns(resolved.MaxOpenConns)
		database.SetMaxIdleConns(resolved.MaxIdleConns)
		database.SetConnMaxLifetime(time.Duration(resolved.ConnMaxLifetime))
	}
	return database, nil
}

func checkStoreHealth(ctx context.Context, resolved *config.ResolvedStore) (resultErr error) {
	if resolved.Metadata.Driver == store.DialectSQLite {
		info, err := os.Stat(resolved.Metadata.SQLitePath)
		if err != nil {
			return safeStoreError("inspect metadata health target", resolved.Metadata, err)
		}
		if info.IsDir() {
			return fmt.Errorf("inspect health target: driver=sqlite target is not a database file")
		}
	}
	metadataDB, err := openResolvedDatabase(resolved.Metadata)
	if err != nil {
		return safeStoreError("open metadata health connection", resolved.Metadata, err)
	}
	defer func() {
		if closeErr := metadataDB.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, safeStoreError("close metadata health connection", resolved.Metadata, closeErr))
		}
	}()
	if err := metadataDB.PingContext(ctx); err != nil {
		return safeStoreError("ping metadata health connection", resolved.Metadata, err)
	}
	if resolved.Audit.ReuseMetadata {
		return nil
	}
	auditDB, err := openResolvedDatabase(resolved.Audit)
	if err != nil {
		return safeStoreError("open audit health connection", resolved.Audit, err)
	}
	defer func() {
		if closeErr := auditDB.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, safeStoreError("close audit health connection", resolved.Audit, closeErr))
		}
	}()
	if err := auditDB.PingContext(ctx); err != nil {
		return safeStoreError("ping audit health connection", resolved.Audit, err)
	}
	return nil
}

func safeStoreError(action string, resolved config.ResolvedStoreTarget, err error) error {
	if resolved.Driver == store.DialectPostgres {
		// Driver errors are intentionally not wrapped because third-party error
		// text is not contractually guaranteed to omit DSNs or passwords.
		return fmt.Errorf("%s: driver=postgres", action)
	}
	return fmt.Errorf("%s: driver=sqlite sqlite_path=%s: %w", action, resolved.SQLitePath, err)
}

func checkHealth(ctx context.Context, url string, timeout time.Duration) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create health request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	response, err := (&http.Client{Timeout: timeout}).Do(request)
	if err != nil {
		return nil, fmt.Errorf("request health endpoint: %w", err)
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxHealthBodySize+1))
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		return nil, fmt.Errorf("read health response: %w", errors.Join(readErr, closeErr))
	}
	if len(body) > maxHealthBodySize {
		return nil, fmt.Errorf("health response exceeds %d bytes", maxHealthBodySize)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("health endpoint returned HTTP %d", response.StatusCode)
	}
	var payload struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode health response: %w", err)
	}
	if payload.Status != "ok" {
		return nil, fmt.Errorf("health endpoint returned status %q", payload.Status)
	}
	return body, nil
}
