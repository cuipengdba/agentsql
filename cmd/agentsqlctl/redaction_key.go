package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/auditrelay"
	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/redaction"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

var errManagementAuditPending = errors.New("management audit pending")

type redactionCLI struct {
	configPath string
}

func newRedactionKeyCommand() *cobra.Command {
	cli := &redactionCLI{}
	command := &cobra.Command{Use: "redaction-key", Short: "Operate the redaction key registry", Args: cobra.NoArgs}
	command.PersistentFlags().StringVarP(&cli.configPath, "config", "c", defaultConfigPath, "path to the YAML configuration file")
	command.AddCommand(cli.newReconcileCommand(), cli.newMarkActiveCommand(), cli.newMarkRetiredCommand(), cli.newVerifyCommand(), cli.newRelayCommand())
	return command
}

func (cli *redactionCLI) newReconcileCommand() *cobra.Command {
	return &cobra.Command{
		Use: "reconcile", Short: "Register missing manifest versions as standby", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			_, resolvedStore, assembly, cleanup, err := loadRedactionCLI(cli.configPath)
			if err != nil {
				return err
			}
			defer cleanup()
			metadata, err := openRegistry(command.Context(), resolvedStore)
			if err != nil {
				return err
			}
			defer metadata.Close()
			registered, err := metadata.RedactionKeys().List(command.Context())
			if err != nil {
				return fmt.Errorf("read redaction key registry: %w", err)
			}
			known := make(map[string]bool, len(registered))
			for _, version := range registered {
				known[version.ID] = true
			}
			registrations := make([]store.RedactionKeyRegistration, 0)
			predicted := append([]model.RedactionKeyVersion(nil), registered...)
			for _, key := range assembly.Observed.Keys {
				id := strconv.Itoa(key.ID)
				if known[id] {
					continue
				}
				registrations = append(registrations, store.RedactionKeyRegistration{ID: id, Commitment: key.Commitment, ConfigRevision: assembly.Observed.Revision})
				predicted = append(predicted, model.RedactionKeyVersion{ID: id, State: model.RedactionKeyStateStandby, Commitment: key.Commitment, ConfigRevision: assembly.Observed.Revision})
			}
			result, reconcileErr := redaction.Reconcile(assembly.Observed, predicted)
			if reconcileErr != nil {
				return reconcileErr
			}
			ids := make([]string, len(registrations))
			for i := range registrations {
				ids[i] = registrations[i].ID
			}
			details := struct {
				RegisteredIDs []string           `json:"registered_ids"`
				Unsatisfied   []int              `json:"unsatisfied"`
				Warnings      []redaction.Detail `json:"warnings"`
				Information   []redaction.Detail `json:"information"`
			}{ids, result.Unsatisfied, result.Warnings, result.Information}
			event, write, err := managementWrite(resolvedStore, "redaction_key_reconcile", details)
			if err != nil {
				return err
			}
			if err := metadata.RedactionKeys().RegisterStandbysWithAudit(command.Context(), registrations, write); err != nil {
				return err
			}
			writeReconciliation(command.OutOrStdout(), ids, result)
			return deliverInline(command.Context(), command.ErrOrStderr(), resolvedStore, metadata, event)
		},
	}
}

func (cli *redactionCLI) newMarkActiveCommand() *cobra.Command {
	var id int
	command := &cobra.Command{
		Use: "registry-mark-active", Short: "Mark a registered version active", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			resolved, err := loadStoreCLI(cli.configPath)
			if err != nil {
				return err
			}
			metadata, err := openRegistry(command.Context(), resolved)
			if err != nil {
				return err
			}
			defer metadata.Close()
			versionID := strconv.Itoa(id)
			var event model.ManagementAuditOutbox
			before, after, outcome, err := metadata.RedactionKeys().MarkActiveCASWithAudit(command.Context(), versionID, func(before, after model.RedactionKeyVersion, outcome string) (store.ManagementAuditWrite, error) {
				details := map[string]any{"version": id, "before_state": before.State, "after_state": after.State, "revision": after.ConfigRevision, "result": outcome}
				var write store.ManagementAuditWrite
				var buildErr error
				event, write, buildErr = managementWrite(resolved, "redaction_key_mark_active", details)
				return write, buildErr
			})
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					return fmt.Errorf("redaction key version %d is not registered; run redaction-key reconcile first", id)
				}
				return err
			}
			fmt.Fprintf(command.OutOrStdout(), "version=%d before=%s after=%s revision=%s result=%s\n", id, before.State, after.State, after.ConfigRevision, outcome)
			return deliverInline(command.Context(), command.ErrOrStderr(), resolved, metadata, event)
		},
	}
	command.Flags().IntVar(&id, "id", 0, "registered key version")
	_ = command.MarkFlagRequired("id")
	return command
}

func (cli *redactionCLI) newMarkRetiredCommand() *cobra.Command {
	var id int
	command := &cobra.Command{
		Use: "registry-mark-retired", Short: "Retire a standby or legacy version", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			resolved, err := loadStoreCLI(cli.configPath)
			if err != nil {
				return err
			}
			metadata, err := openRegistry(command.Context(), resolved)
			if err != nil {
				return err
			}
			defer metadata.Close()
			var event model.ManagementAuditOutbox
			before, after, err := metadata.RedactionKeys().MarkRetiredWithAudit(command.Context(), strconv.Itoa(id), func(before, after model.RedactionKeyVersion) (store.ManagementAuditWrite, error) {
				details := map[string]any{"version": id, "before_state": before.State, "after_state": after.State, "revision": after.ConfigRevision, "result": "retired"}
				var write store.ManagementAuditWrite
				var buildErr error
				event, write, buildErr = managementWrite(resolved, "redaction_key_mark_retired", details)
				return write, buildErr
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(command.OutOrStdout(), "version=%d before=%s after=%s revision=%s result=retired\n", id, before.State, after.State, after.ConfigRevision)
			return deliverInline(command.Context(), command.ErrOrStderr(), resolved, metadata, event)
		},
	}
	command.Flags().IntVar(&id, "id", 0, "registered key version")
	_ = command.MarkFlagRequired("id")
	return command
}

func (cli *redactionCLI) newVerifyCommand() *cobra.Command {
	var id int
	var plaintextFile, fingerprint string
	command := &cobra.Command{
		Use: "verify", Short: "Verify a fingerprint without putting plaintext in argv", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			_, resolvedStore, assembly, cleanup, err := loadRedactionCLI(cli.configPath)
			if err != nil {
				return err
			}
			defer cleanup()
			metadata, err := openRegistry(command.Context(), resolvedStore)
			if err != nil {
				return err
			}
			defer metadata.Close()
			registered, err := metadata.RedactionKeys().List(command.Context())
			if err != nil {
				return fmt.Errorf("read redaction key registry: %w", err)
			}
			verifiable := false
			for _, registeredVersion := range registered {
				if registeredVersion.ID == strconv.Itoa(id) && registeredVersion.State != model.RedactionKeyStateRetired {
					verifiable = true
					break
				}
			}
			var plaintext []byte
			if plaintextFile == "-" {
				plaintext, err = io.ReadAll(io.LimitReader(command.InOrStdin(), 1<<20))
			} else {
				plaintext, err = os.ReadFile(plaintextFile)
			}
			if err != nil {
				return fmt.Errorf("read plaintext input: %w", err)
			}
			defer clearBytes(plaintext)
			match := false
			if verifiable {
				match, err = assembly.Verify(id, string(plaintext), fingerprint)
				if err != nil {
					return err
				}
			}
			fmt.Fprintf(command.OutOrStdout(), "match=%t\n", match)
			if !match {
				return errors.New("fingerprint does not match")
			}
			return nil
		},
	}
	command.Flags().IntVar(&id, "id", 0, "key version")
	command.Flags().StringVar(&plaintextFile, "plaintext-file", "", "plaintext file path, or - for stdin")
	command.Flags().StringVar(&fingerprint, "fingerprint", "", "fingerprint to verify")
	_ = command.MarkFlagRequired("id")
	_ = command.MarkFlagRequired("plaintext-file")
	_ = command.MarkFlagRequired("fingerprint")
	return command
}

func (cli *redactionCLI) newRelayCommand() *cobra.Command {
	var once bool
	command := &cobra.Command{
		Use: "relay", Short: "Deliver pending management audit events", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			resolved, err := loadStoreCLI(cli.configPath)
			if err != nil {
				return err
			}
			if resolved.Audit.ReuseMetadata {
				fmt.Fprintln(command.OutOrStdout(), "relay not required: audit reuses metadata")
				return nil
			}
			metadata, err := openRegistry(command.Context(), resolved)
			if err != nil {
				return err
			}
			defer metadata.Close()
			auditStore, err := store.OpenAuditOnly(command.Context(), resolved.AuditOptions())
			if err != nil {
				return safeCLIStoreError("open audit store", err)
			}
			defer auditStore.Close()
			relay, err := auditrelay.New(metadata.Outbox(), auditStore.AuditLogs(), "agentsqlctl-"+uuid.NewString(), nil)
			if err != nil {
				return err
			}
			if once {
				delivered, runErr := relay.RunOnce(command.Context())
				fmt.Fprintf(command.OutOrStdout(), "delivered=%d\n", delivered)
				return runErr
			}
			return relay.Run(command.Context(), 10*time.Second)
		},
	}
	command.Flags().BoolVar(&once, "once", false, "deliver one batch and exit")
	return command
}

func loadRedactionCLI(path string) (config.Config, *config.ResolvedStore, config.RedactionAssembly, func(), error) {
	loaded, err := config.Load(path)
	if err != nil {
		return config.Config{}, nil, config.RedactionAssembly{}, func() {}, err
	}
	resolvedStore, err := config.ResolveStore(&loaded, os.LookupEnv)
	if err != nil {
		return loaded, nil, config.RedactionAssembly{}, func() {}, err
	}
	resolvedRedaction, err := config.ResolveRedaction(&loaded, os.LookupEnv)
	if err != nil {
		return loaded, resolvedStore, config.RedactionAssembly{}, func() {}, err
	}
	cleanup := func() { resolvedRedaction.Clear() }
	assembly, err := config.BuildRedactionAssembly(resolvedRedaction)
	if err != nil {
		cleanup()
		return loaded, resolvedStore, config.RedactionAssembly{}, func() {}, err
	}
	if assembly.Observed.Status != "available" || len(assembly.Observed.Keys) == 0 || assembly.Verify == nil {
		cleanup()
		return loaded, resolvedStore, config.RedactionAssembly{}, func() {}, errors.New("redaction-key commands require a resolved hash_keys manifest")
	}
	return loaded, resolvedStore, assembly, cleanup, nil
}

func loadStoreCLI(path string) (*config.ResolvedStore, error) {
	loaded, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	return config.ResolveStore(&loaded, os.LookupEnv)
}

func openRegistry(ctx context.Context, resolved *config.ResolvedStore) (store.MetadataRegistryView, error) {
	secret := os.Getenv("AGENTSQL_SECRET")
	if len(secret) != 32 {
		return nil, errors.New("AGENTSQL_SECRET must contain exactly 32 bytes")
	}
	opened, err := store.OpenMetadataOnly(ctx, resolved.MetadataOptions(), []byte(secret))
	if err != nil {
		return nil, safeCLIStoreError("open metadata store", err)
	}
	return opened, nil
}

func managementWrite(resolved *config.ResolvedStore, action string, details any) (model.ManagementAuditOutbox, store.ManagementAuditWrite, error) {
	encoded, err := json.Marshal(details)
	if err != nil {
		return model.ManagementAuditOutbox{}, store.ManagementAuditWrite{}, err
	}
	eventID := uuid.NewString()
	event := model.ManagementAuditOutbox{EventUUID: eventID, Action: action, ActorType: "cli", ActorID: "agentsqlctl", DetailsJSON: string(encoded), CreatedAt: time.Now().UTC()}
	if !resolved.Audit.ReuseMetadata {
		return event, store.ManagementAuditWrite{Outbox: &event}, nil
	}
	actionValue, actorType, actorID, detailText := action, "cli", "agentsqlctl", string(encoded)
	audit := model.AuditLog{Decision: "allow", Action: &actionValue, ActorType: &actorType, ActorID: &actorID, DetailsJSON: &detailText, EventUUID: &eventID}
	return event, store.ManagementAuditWrite{Audit: &audit}, nil
}

func deliverInline(ctx context.Context, _ io.Writer, resolved *config.ResolvedStore, metadata store.MetadataRegistryView, event model.ManagementAuditOutbox) error {
	if resolved.Audit.ReuseMetadata {
		return nil
	}
	auditStore, err := store.OpenAuditOnly(ctx, resolved.AuditOptions())
	if err == nil {
		defer auditStore.Close()
		var relay *auditrelay.Relay
		relay, err = auditrelay.New(metadata.Outbox(), auditStore.AuditLogs(), "agentsqlctl-inline-"+uuid.NewString(), nil)
		if err == nil {
			_, err = relay.RunOnce(ctx)
		}
	}
	delivered, statusErr := metadata.Outbox().Delivered(ctx, event.EventUUID)
	if statusErr == nil && delivered {
		return nil
	}
	if err == nil {
		err = statusErr
	}
	message := fmt.Sprintf("变更已提交，管理审计待投递（event_uuid=%s，可用 redaction-key relay 重试）", event.EventUUID)
	return fmt.Errorf("%w: %s", errManagementAuditPending, message)
}

func writeReconciliation(writer io.Writer, registered []string, result redaction.Result) {
	sort.Strings(registered)
	fmt.Fprintf(writer, "registered_standby=%s ready=%t unsatisfied=%v\n", strings.Join(registered, ","), result.Ready, result.Unsatisfied)
	for _, detail := range append(append([]redaction.Detail{}, result.Warnings...), result.Information...) {
		fmt.Fprintf(writer, "drift=#%d kind=%s version=%d message=%s\n", detail.Number, detail.Kind, detail.Version, detail.Message)
	}
}

func safeCLIStoreError(operation string, _ error) error {
	return fmt.Errorf("%s: operation failed", operation)
}

func clearBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
