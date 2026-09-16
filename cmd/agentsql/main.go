// Command agentsql is the AgentSQL database security gateway.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/cuipengdba/agentsql/internal/adminapi"
	"github.com/cuipengdba/agentsql/internal/bootstrap"
	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/mcpserver"
	"github.com/cuipengdba/agentsql/internal/version"
	"github.com/cuipengdba/agentsql/internal/webui"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
)

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
		Version:       version.Version,
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	command.SetVersionTemplate("{{.Version}}\n")

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
			secret, _, err := validateStartupSecurity(logger)
			if err != nil {
				return err
			}
			loaded, err := config.Load(configPath)
			if err != nil {
				return fmt.Errorf("load MCP configuration: %w", err)
			}
			// The stdio command has no HTTP console, so it must not allocate or
			// decorate audit paths for an event stream that cannot be consumed.
			loaded = prepareStdioConfig(loaded)
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
				Version: version.Version,
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

func prepareStdioConfig(loaded config.Config) config.Config {
	loaded.Server.ConsoleEnabled = false
	loaded.Server.EventStream = false
	return loaded
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

func newServeCommand(logger zerolog.Logger) *cobra.Command {
	var configPath string

	command := &cobra.Command{
		Use:   "serve",
		Short: "Run the AgentSQL MCP Streamable HTTP server",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			secret, insecure, err := validateStartupSecurity(logger)
			if err != nil {
				return err
			}
			loaded, err := config.Load(configPath)
			if err != nil {
				return fmt.Errorf("load configuration: %w", err)
			}
			var adminUser string
			var adminPassword string
			if loaded.Server.ConsoleEnabled {
				adminUser, adminPassword, err = adminapi.AdminCredentialsFromEnv()
				if err != nil {
					return err
				}
				if err := config.ValidateAdminPassword(adminUser, adminPassword, insecure); err != nil {
					return err
				}
			}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			runtime, err := bootstrap.Assemble(ctx, loaded, []byte(secret))
			if err != nil {
				return fmt.Errorf("assemble HTTP runtime: %w", err)
			}
			var httpOptions []mcpserver.HTTPOption
			if loaded.Server.ConsoleEnabled {
				adminHandler, adminError := adminapi.NewHandler(adminapi.Deps{
					Runtime:       runtime,
					Config:        loaded,
					AdminUsername: adminUser,
					AdminPassword: adminPassword,
					TokenKey:      adminapi.DeriveTokenKey([]byte(secret)),
				}, logger)
				if adminError != nil {
					return errors.Join(adminError, runtime.Close())
				}
				httpOptions = append(httpOptions, mcpserver.WithAdminAPI(adminHandler))
				webHandler, webError := webui.Handler()
				if webError != nil {
					return errors.Join(webError, runtime.Close())
				}
				httpOptions = append(httpOptions, mcpserver.WithWebConsole(webHandler))
			}
			handler, err := mcpserver.NewHTTPHandler(runtime, loaded, logger, httpOptions...)
			if err != nil {
				return errors.Join(err, runtime.Close())
			}
			httpServer := &http.Server{
				Addr:              loaded.Server.HTTPListen,
				Handler:           handler,
				ReadHeaderTimeout: 10 * time.Second,
			}
			logger.Info().
				Str("http_listen", loaded.Server.HTTPListen).
				Str("version", version.Version).
				Msg("MCP Streamable HTTP server starting")
			serveErrors := make(chan error, 1)
			go func() {
				serveErrors <- httpServer.ListenAndServe()
			}()
			select {
			case serveError := <-serveErrors:
				if errors.Is(serveError, http.ErrServerClosed) {
					serveError = nil
				}
				return errors.Join(serveError, runtime.Close())
			case <-ctx.Done():
				shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				shutdownError := httpServer.Shutdown(shutdownContext)
				cancel()
				var forcedCloseError error
				if shutdownError != nil {
					forcedCloseError = httpServer.Close()
				}
				serveError := <-serveErrors
				if errors.Is(serveError, http.ErrServerClosed) {
					serveError = nil
				}
				logger.Info().Msg("MCP Streamable HTTP server stopped")
				return errors.Join(shutdownError, forcedCloseError, serveError, runtime.Close())
			}
		},
	}
	command.Flags().StringVarP(&configPath, "config", "c", "config.yaml", "path to the YAML configuration file")
	return command
}

func validateStartupSecurity(logger zerolog.Logger) (secret string, insecure bool, err error) {
	insecure, err = config.InsecureModeFromEnv()
	if err != nil {
		return "", false, err
	}
	if insecure {
		logger.Warn().Msg("AGENTSQL_INSECURE=1 enabled: publicly known test credentials are permitted; never use in production")
	}
	secret = os.Getenv("AGENTSQL_SECRET")
	if err := config.ValidateStartupSecret(secret, insecure); err != nil {
		return "", insecure, err
	}
	return secret, insecure, nil
}
