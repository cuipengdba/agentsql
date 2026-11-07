package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/cuipengdba/agentsql/internal/chatbi"
	"github.com/cuipengdba/agentsql/internal/version"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
)

func newChatBICommand(logger zerolog.Logger) *cobra.Command {
	root := &cobra.Command{Use: "chatbi", Short: "Run native data Q&A and SQLBot compatibility services"}
	var configuration, snapshot, historyPath string
	root.PersistentFlags().StringVar(&configuration, "config", "chatbi.yaml", "ChatBI YAML configuration")
	var modelPath string
	verifyModel := &cobra.Command{Use: "verify-embedding-model", Short: "Verify the pinned local ONNX model without loading it", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := chatbi.VerifyPinnedEmbeddingModel(modelPath); err != nil {
			return err
		}
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "CHATBI_MODEL=VERIFIED")
		return err
	}}
	verifyModel.Flags().StringVar(&modelPath, "path", "", "absolute directory containing the pinned ONNX model")
	_ = verifyModel.MarkFlagRequired("path")
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
	var assistantOnly bool
	var assistantListen string
	serve := &cobra.Command{Use: "serve", Short: "Serve the Go Q&A API on its dedicated listener", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		config, err := chatbi.LoadConfig(configuration)
		if err != nil {
			return err
		}
		if assistantListen != "" {
			if !assistantOnly {
				return errors.New("--listen requires --assistant-only")
			}
			host, port, splitErr := net.SplitHostPort(assistantListen)
			portNumber, portErr := strconv.Atoi(port)
			if splitErr != nil || portErr != nil || portNumber < 1 || portNumber > 65535 || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() || assistantListen == config.Listen || assistantListen == config.LegacyMCPListen {
				return errors.New("assistant-only listen must be a distinct loopback host:port")
			}
			config.Listen = assistantListen
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		service, err := chatbi.Assemble(ctx, config)
		if err != nil {
			return err
		}
		defer service.Store.Close()
		if config.EmbeddingModelPath != "" {
			embedding, err := chatbi.NewLocalEmbeddingModel(config.EmbeddingModelPath)
			if err != nil {
				return err
			}
			service.Embedding = embedding
			defer embedding.Close()
		}
		stopIndexPrewarm := service.EnableTableIndexPrewarm(ctx)
		defer stopIndexPrewarm()
		handler, err := chatbi.NewHandlerWithLimit(service, version.Version, config.MaxDataRequests)
		if err != nil {
			return err
		}
		if assistantOnly {
			handler = chatbi.AssistantOnlyHandler(handler)
		}
		if !assistantOnly && service.ManagedFileDatasource != "" {
			reaperCtx, stopReaper := context.WithCancel(ctx)
			reaperDone := make(chan struct{})
			defer func() {
				stopReaper()
				<-reaperDone
			}()
			go func() {
				defer close(reaperDone)
				ticker := time.NewTicker(10 * time.Minute)
				defer ticker.Stop()
				for {
					cleanupCtx, cancel := context.WithTimeout(reaperCtx, 5*time.Minute)
					count, reapErr := service.ReapAbandonedManagedFiles(cleanupCtx, time.Now().UTC())
					cancel()
					if reapErr != nil {
						logger.Error().Err(reapErr).Msg("ChatBI abandoned file import cleanup failed")
					}
					if count > 0 {
						logger.Info().Int("count", count).Msg("ChatBI abandoned file imports cleaned")
					}
					select {
					case <-reaperCtx.Done():
						return
					case <-ticker.C:
					}
				}
			}()
		}
		server := &http.Server{Addr: config.Listen, Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
		servers := []*http.Server{server}
		if !assistantOnly && config.LegacyMCPListen != "" {
			// The original SQLBot client connects to /mcp on a separate port.
			// Do not expose the API, login or administration routes there.
			mcpOnly := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/mcp" {
					http.NotFound(w, r)
					return
				}
				handler.ServeHTTP(w, r)
			})
			servers = append(servers, &http.Server{Addr: config.LegacyMCPListen, Handler: mcpOnly,
				ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 20 * time.Second,
				IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10})
		}
		result := make(chan error, len(servers))
		for _, active := range servers {
			go func() { result <- active.ListenAndServe() }()
		}
		logger.Info().Str("listen", config.Listen).Str("version", version.Version).Msg("ChatBI server starting")
		if len(servers) > 1 {
			logger.Info().Str("listen", config.LegacyMCPListen).Msg("Legacy SQLBot MCP listener starting")
		}
		select {
		case err := <-result:
			for _, active := range servers {
				_ = active.Close()
			}
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return fmt.Errorf("serve ChatBI: %w", err)
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var shutdownErr error
			for _, active := range servers {
				if err := active.Shutdown(shutdownCtx); err != nil {
					_ = active.Close()
					shutdownErr = errors.Join(shutdownErr, err)
				}
			}
			return shutdownErr
		}
	}}
	serve.Flags().BoolVar(&assistantOnly, "assistant-only", false, "serve only the public assistant iframe and scoped API on an isolated origin")
	serve.Flags().StringVar(&assistantListen, "listen", "", "loopback listener override for the isolated assistant service")

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
	configure := &cobra.Command{Use: "configure", Short: "Upsert private model and per-user gateway mappings atomically", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
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
	var rotationPath string
	rotateBinding := &cobra.Command{Use: "rotate-binding", Short: "Preflight and atomically rotate one private gateway binding", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		config, err := chatbi.LoadConfig(configuration)
		if err != nil {
			return err
		}
		rotation, err := chatbi.ReadBindingRotation(rotationPath)
		if err != nil {
			return err
		}
		service, err := chatbi.Assemble(cmd.Context(), config)
		if err != nil {
			return err
		}
		defer service.Store.Close()
		if err := service.RotateBinding(cmd.Context(), rotation); err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
			"rotated": true, "workspace": rotation.Workspace, "user": rotation.User,
			"datasource": rotation.Datasource, "gateway_mapping_rotated": false})
	}}
	rotateBinding.Flags().StringVar(&rotationPath, "from", "", "private binding rotation JSON")
	_ = rotateBinding.MarkFlagRequired("from")
	var revokeWorkspace, revokeUser, revokeDatasource, revokeBindingID string
	revokeBinding := &cobra.Command{Use: "revoke-binding", Short: "Revoke one current local ChatBI datasource mapping with an audited compare-and-swap", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		config, err := chatbi.LoadConfig(configuration)
		if err != nil {
			return err
		}
		service, err := chatbi.Assemble(cmd.Context(), config)
		if err != nil {
			return err
		}
		defer service.Store.Close()
		if err := service.Store.RevokeBinding(cmd.Context(), chatbi.ID(revokeWorkspace), chatbi.ID(revokeUser),
			chatbi.ID(revokeDatasource), revokeBindingID); err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
			"revoked": true, "workspace": revokeWorkspace, "user": revokeUser,
			"datasource": revokeDatasource, "gateway_mapping_revoked": false})
	}}
	revokeBinding.Flags().StringVar(&revokeWorkspace, "workspace", "", "current ChatBI workspace ID")
	revokeBinding.Flags().StringVar(&revokeUser, "user", "", "current ChatBI user ID")
	revokeBinding.Flags().StringVar(&revokeDatasource, "datasource", "", "current ChatBI datasource ID")
	revokeBinding.Flags().StringVar(&revokeBindingID, "expected-binding-id", "", "current private gateway binding ID; rejects a rotated mapping")
	for _, name := range []string{"workspace", "user", "datasource", "expected-binding-id"} {
		_ = revokeBinding.MarkFlagRequired(name)
	}
	historyPreflight := &cobra.Command{Use: "history-preflight", Short: "Validate a metadata-only SQLBot history export without writing", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		input, err := chatbi.ReadHistory(historyPath)
		if err != nil {
			return err
		}
		questions := 0
		for _, item := range input.Chats {
			questions += len(item.Records)
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(chatbi.HistoryReport{ImportedChats: len(input.Chats), ImportedQuestions: questions, CompleteMigration: false, HistoricalRowsUsed: false})
	}}
	historyPreflight.Flags().StringVar(&historyPath, "from", "", "private metadata-only SQLBot history export")
	_ = historyPreflight.MarkFlagRequired("from")
	var importHistoryPath string
	historyImport := &cobra.Command{Use: "import-history", Short: "Atomically import metadata-only SQLBot chat history into an initialized ChatBI store", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		input, err := chatbi.ReadHistory(importHistoryPath)
		if err != nil {
			return err
		}
		config, err := chatbi.LoadConfig(configuration)
		if err != nil {
			return err
		}
		service, err := chatbi.Assemble(cmd.Context(), config)
		if err != nil {
			return err
		}
		defer service.Store.Close()
		report, err := service.Store.ImportHistory(cmd.Context(), input)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
	}}
	historyImport.Flags().StringVar(&importHistoryPath, "from", "", "private metadata-only SQLBot history export")
	_ = historyImport.MarkFlagRequired("from")
	root.AddCommand(preflight, importCommand, initCommand, configure, rotateBinding, revokeBinding, historyPreflight, historyImport, verifyModel, serve)
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
