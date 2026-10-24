package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/spf13/cobra"
)

type supportBundle struct {
	SchemaVersion int                                `json:"schema_version"`
	GeneratedAt   string                             `json:"generated_at"`
	OS            string                             `json:"os"`
	Architecture  string                             `json:"architecture"`
	ReadOnly      bool                               `json:"read_only_inspection"`
	Diagnostic    doctorReport                       `json:"diagnostic"`
	Capabilities  map[string]model.DatasourceSupport `json:"product_capabilities"`
	Notice        string                             `json:"notice"`
}

func newSupportBundleCommand() *cobra.Command {
	var configPath, output string
	var timeout time.Duration
	command := &cobra.Command{
		Use: "support-bundle", Short: "Export a bounded, allowlisted deployment diagnostic JSON without secrets", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if timeout <= 0 || timeout > time.Minute {
				return errors.New("support-bundle timeout must be positive and at most 1m")
			}
			// Reserve a new output before connecting to stores. O_EXCL rejects
			// existing files and symlinks; Root confines the write to its parent.
			parent, err := os.OpenRoot(filepath.Dir(output))
			if err != nil {
				return errors.New("cannot open support output directory")
			}
			defer parent.Close()
			file, err := parent.OpenFile(filepath.Base(output), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				return errors.New("support output must be a new file; overwrite and symlinks are refused")
			}
			defer file.Close()
			ctx, cancel := context.WithTimeout(command.Context(), timeout)
			defer cancel()
			report := inspectDeployment(ctx, configPath)
			bundle := supportBundle{SchemaVersion: 1, GeneratedAt: time.Now().UTC().Format(time.RFC3339), OS: runtime.GOOS, Architecture: runtime.GOARCH, ReadOnly: true, Diagnostic: report, Capabilities: model.SupportCatalog(), Notice: "Static product evidence is not a scan or certification of configured databases. Contains schema readiness only; no rows, SQL, logs, credentials, connection addresses or environment dump."}
			data, err := json.MarshalIndent(bundle, "", "  ")
			if err != nil || len(data) > 256<<10 {
				return errors.New("support bundle exceeds the bounded output contract")
			}
			if _, err := file.Write(append(data, '\n')); err != nil {
				return errors.New("cannot write support bundle")
			}
			if err := file.Sync(); err != nil {
				return errors.New("cannot persist support bundle")
			}
			if err := file.Close(); err != nil {
				return errors.New("cannot close support bundle")
			}
			if _, err := fmt.Fprintf(command.OutOrStdout(), "support_bundle=written status=%s read_only_inspection=true\n", report.Status); err != nil {
				return errors.New("cannot write support summary")
			}
			switch report.Status {
			case "ready":
				return nil
			case "pending":
				return doctorFailure{code: 2}
			default:
				return doctorFailure{code: 1}
			}
		},
	}
	command.Flags().StringVarP(&configPath, "config", "c", defaultConfigPath, "path to the YAML configuration file (at most 1 MiB)")
	command.Flags().StringVar(&output, "output", "", "new diagnostic JSON file; parent directory must exist")
	command.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "read-only store inspection timeout (maximum 1m)")
	_ = command.MarkFlagRequired("output")
	return command
}
