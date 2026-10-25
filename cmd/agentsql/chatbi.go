package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/cuipengdba/agentsql/internal/chatbi"
	"github.com/cuipengdba/agentsql/internal/version"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
)

func newChatBICommand(logger zerolog.Logger) *cobra.Command {
	root := &cobra.Command{Use: "chatbi", Short: "Run native data Q&A and SQLBot compatibility services"}
	var configuration, snapshot string
	root.PersistentFlags().StringVar(&configuration, "config", "chatbi.yaml", "ChatBI YAML configuration")
	preflight := &cobra.Command{Use: "preflight", Short: "Validate a private SQLBot identity snapshot without changing either system", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		_, report, err := chatbi.ReadSnapshot(snapshot)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
	}}
	preflight.Flags().StringVar(&snapshot, "from", "", "private migration snapshot")
	_ = preflight.MarkFlagRequired("from")
	importCommand := &cobra.Command{Use: "import", Short: "Import a private identity snapshot into a new encrypted ChatBI store", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		input, report, err := chatbi.ReadSnapshot(snapshot)
		if err != nil {
			return err
		}
		config, err := chatbi.LoadConfig(configuration)
		if err != nil {
			return err
		}
		if err := publishChatBIStore(cmd.Context(), config, input); err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
	}}
	importCommand.Flags().StringVar(&snapshot, "from", "", "private migration snapshot")
	_ = importCommand.MarkFlagRequired("from")
	serve := &cobra.Command{Use: "serve", Short: "Serve the Go Q&A API on its dedicated listener", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		config, err := chatbi.LoadConfig(configuration)
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		service, err := chatbi.Assemble(ctx, config)
		if err != nil {
			return err
		}
		defer service.Store.Close()
		handler, err := chatbi.NewHandlerWithLimit(service, version.Version, config.MaxDataRequests)
		if err != nil {
			return err
		}
		server := &http.Server{Addr: config.Listen, Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
		result := make(chan error, 1)
		go func() { result <- server.ListenAndServe() }()
		logger.Info().Str("listen", config.Listen).Str("version", version.Version).Msg("ChatBI server starting")
		select {
		case err := <-result:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return fmt.Errorf("serve ChatBI: %w", err)
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err := server.Shutdown(shutdownCtx)
			if err != nil {
				_ = server.Close()
			}
			return err
		}
	}}

	var adminUser, passwordEnv, connections string
	var standardUser bool
	initCommand := &cobra.Command{Use: "init", Short: "Initialize a new native Q&A installation", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		config, err := chatbi.LoadConfig(configuration)
		if err != nil {
			return err
		}
		snapshot, err := chatbi.NewInstallation(adminUser, os.Getenv(passwordEnv))
		if err != nil {
			return err
		}
		if standardUser {
			snapshot.Users[0].Admin = false
			snapshot.Users[0].Memberships[0].Weight = 0
		}
		if err := publishChatBIStore(cmd.Context(), config, snapshot); err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"initialized": true, "workspace": "1", "user": "1", "connections_configured": false})
	}}
	initCommand.Flags().StringVar(&adminUser, "admin-user", "admin", "initial native administrator account")
	initCommand.Flags().BoolVar(&standardUser, "standard-user", false, "initialize a non-administrator account for a dedicated demonstration installation")
	initCommand.Flags().StringVar(&passwordEnv, "password-env", "AGENTSQL_CHATBI_ADMIN_PASSWORD", "environment variable containing the initial password")
	configure := &cobra.Command{Use: "configure", Short: "Apply private model and per-user gateway mappings atomically", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		config, err := chatbi.LoadConfig(configuration)
		if err != nil {
			return err
		}
		input, err := chatbi.ReadConnections(connections)
		if err != nil {
			return err
		}
		service, err := chatbi.Assemble(cmd.Context(), config)
		if err != nil {
			return err
		}
		defer service.Store.Close()
		if err := service.Store.ConfigureConnections(cmd.Context(), input); err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"configured": true, "models": len(input.Models), "bindings": len(input.Bindings), "live_query_verified": false})
	}}
	configure.Flags().StringVar(&connections, "from", "", "private connection configuration JSON")
	_ = configure.MarkFlagRequired("from")
	root.AddCommand(preflight, importCommand, initCommand, configure, serve)
	return root
}

func publishChatBIStore(ctx context.Context, config chatbi.Config, input chatbi.Snapshot) error {
	secret := os.Getenv(config.SecretEnv)
	if len(secret) != 32 {
		return errors.New("ChatBI encryption secret must contain exactly 32 bytes")
	}
	target, err := filepath.Abs(config.StorePath)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		return errors.New("ChatBI import target already exists or is unavailable")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return errors.New("cannot prepare ChatBI import directory")
	}
	staging, err := os.CreateTemp(filepath.Dir(target), ".agentsql-chatbi-import-*")
	if err != nil {
		return errors.New("cannot prepare ChatBI import file")
	}
	path := staging.Name()
	if err := staging.Close(); err != nil {
		return err
	}
	defer os.Remove(path)
	store, err := chatbi.OpenStore(ctx, path, []byte(secret))
	if err != nil {
		return err
	}
	importErr := store.ImportSnapshot(ctx, input)
	closeErr := store.Close()
	if err := errors.Join(importErr, closeErr); err != nil {
		return err
	}
	// Link is create-only even if another process creates target after the
	// initial check. Both paths are on the explicitly configured filesystem.
	if err := os.Link(path, target); err != nil {
		return errors.New("cannot publish imported store; target was not replaced")
	}
	return nil
}
