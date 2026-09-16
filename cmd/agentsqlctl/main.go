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
	"github.com/spf13/cobra"
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
	command.AddCommand(newHealthCommand())
	return command
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
			if _, err := fmt.Fprintf(
				command.OutOrStdout(),
				"config ok: %s sqlite=%s\n",
				loaded.Server.HTTPListen,
				loaded.Store.SQLitePath,
			); err != nil {
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
			insecure, err := config.InsecureModeFromEnv()
			if err != nil {
				return err
			}
			if insecure {
				if _, err := fmt.Fprintln(command.ErrOrStderr(), "WARNING: AGENTSQL_INSECURE=1 enabled: publicly known test credentials are permitted; never use in production"); err != nil {
					return fmt.Errorf("write insecure-mode warning: %w", err)
				}
			}
			secret := os.Getenv("AGENTSQL_SECRET")
			if err := config.ValidateStartupSecret(secret, insecure); err != nil {
				return err
			}
			current, latest, err := migrate(command.Context(), configPath, []byte(secret))
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(command.OutOrStdout(), "migration current=%d latest=%d\n", current, latest); err != nil {
				return fmt.Errorf("write migration result: %w", err)
			}
			return nil
		},
	}
	command.Flags().StringVarP(&configPath, "config", "c", defaultConfigPath, "path to the YAML configuration file")
	return command
}

func migrate(ctx context.Context, configPath string, secret []byte) (current int, latest int, resultErr error) {
	loaded, err := config.Load(configPath)
	if err != nil {
		return 0, 0, fmt.Errorf("load migration configuration: %w", err)
	}
	metadataStore, err := store.OpenWithSecret(ctx, loaded.Store.SQLitePath, secret)
	if err != nil {
		return 0, 0, fmt.Errorf("open metadata store for migration: %w", err)
	}
	defer func() {
		resultErr = errors.Join(resultErr, metadataStore.Close())
	}()

	database, err := sql.Open("sqlite", loaded.Store.SQLitePath)
	if err != nil {
		return 0, 0, fmt.Errorf("open SQLite migration connection: %w", err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	defer func() {
		resultErr = errors.Join(resultErr, database.Close())
	}()
	if err := database.PingContext(ctx); err != nil {
		return 0, 0, fmt.Errorf("ping SQLite migration connection: %w", err)
	}
	if err := store.Migrate(ctx, database); err != nil {
		return 0, 0, fmt.Errorf("migrate metadata store: %w", err)
	}
	if err := database.QueryRowContext(
		ctx,
		"SELECT COALESCE(MAX(version), 0) FROM schema_migrations",
	).Scan(&current); err != nil {
		return 0, 0, fmt.Errorf("read current migration version: %w", err)
	}
	// Migrate applies every embedded migration, so current equals latest here.
	return current, current, nil
}

func newHealthCommand() *cobra.Command {
	var url string
	var timeout time.Duration
	command := &cobra.Command{
		Use:   "health",
		Short: "Check an AgentSQL liveness endpoint",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if timeout <= 0 {
				return fmt.Errorf("health timeout must be positive")
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
	command.Flags().DurationVar(&timeout, "timeout", 3*time.Second, "health request timeout")
	return command
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
