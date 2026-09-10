// Command agentsql is the AgentSQL database security gateway.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/cuipengdba/agentsql/internal/bootstrap"
	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/mcpserver"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
)

var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	logger, err := newLogger(stderr)
	if err != nil {
		fallback := zerolog.New(stderr)
		fallback.Error().Err(err).Msg("configure logger")
		return 1
	}

	command := newRootCommand(logger)
	command.SetArgs(args)
	command.SetOut(stdout)
	command.SetErr(stderr)
	if err := command.Execute(); err != nil {
		logger.Error().Err(err).Msg("command failed")
		return 1
	}

	return 0
}

func newRootCommand(logger zerolog.Logger) *cobra.Command {
	command := &cobra.Command{
		Use:           "agentsql",
		Short:         "AI-native database security gateway",
		Version:       version,
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	command.AddCommand(newVersionCommand())
	command.AddCommand(newServeCommand(logger))
	command.AddCommand(newMCPCommand(logger))
	return command
}

func newMCPCommand(logger zerolog.Logger) *cobra.Command {
	var configPath string
	var apiKey string
	command := &cobra.Command{
		Use:   "mcp",
		Short: "Run the authenticated AgentSQL MCP stdio server",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			loaded, err := config.Load(configPath)
			if err != nil {
				return fmt.Errorf("load MCP configuration: %w", err)
			}
			secret := os.Getenv("AGENTSQL_SECRET")
			if len(secret) != 32 {
				return fmt.Errorf("AGENTSQL_SECRET must contain exactly 32 bytes")
			}
			boundKey := strings.TrimSpace(apiKey)
			if boundKey == "" {
				boundKey = strings.TrimSpace(os.Getenv("AGENTSQL_API_KEY"))
			}
			if boundKey == "" {
				return fmt.Errorf("MCP API key is required via --api-key or AGENTSQL_API_KEY")
			}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			runtime, err := bootstrap.Assemble(ctx, loaded, []byte(secret))
			if err != nil {
				return fmt.Errorf("assemble MCP runtime: %w", err)
			}
			runError := mcpserver.RunStdio(ctx, mcpserver.Options{
				APIKey:  boundKey,
				Runtime: runtime,
				Logger:  logger,
				Version: version,
			})
			if errors.Is(runError, context.Canceled) {
				runError = nil
			}
			closeError := runtime.Close()
			if runError != nil || closeError != nil {
				return errors.Join(runError, closeError)
			}
			return nil
		},
	}
	command.Flags().StringVarP(&configPath, "config", "c", "config.yaml", "path to the YAML configuration file")
	command.Flags().StringVar(&apiKey, "api-key", "", "bind this stdio server to one Agent API key")
	return command
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the AgentSQL version",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if _, err := fmt.Fprintln(command.OutOrStdout(), version); err != nil {
				return fmt.Errorf("write version: %w", err)
			}
			return nil
		},
	}
}

func newServeCommand(logger zerolog.Logger) *cobra.Command {
	var configPath string

	command := &cobra.Command{
		Use:   "serve",
		Short: "Validate configuration and initialize AgentSQL",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			loaded, err := config.Load(configPath)
			if err != nil {
				return fmt.Errorf("load configuration: %w", err)
			}
			metadataStore, err := store.Open(context.Background(), loaded.Store.SQLitePath)
			if err != nil {
				return fmt.Errorf("open metadata store: %w", err)
			}
			if err := metadataStore.Close(); err != nil {
				return fmt.Errorf("close metadata store: %w", err)
			}

			logger.Info().
				Str("http_listen", loaded.Server.HTTPListen).
				Bool("console_enabled", loaded.Server.ConsoleEnabled).
				Msg("configuration validated")
			return nil
		},
	}
	command.Flags().StringVarP(&configPath, "config", "c", "config.yaml", "path to the YAML configuration file")
	return command
}
