package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/demoseed"
	"github.com/cuipengdba/agentsql/internal/executor"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/spf13/cobra"
)

type realDemoDatasourcePinger struct{}

func (realDemoDatasourcePinger) Ping(ctx context.Context, datasource model.Datasource, password string) error {
	var opened executor.Executor
	var err error
	switch datasource.DBType {
	case "postgres":
		opened, err = executor.NewPostgresExecutor(ctx, datasource, password, true)
	case "mysql":
		opened, err = executor.NewMySQLExecutor(ctx, datasource, password, true)
	default:
		return fmt.Errorf("unsupported datasource type")
	}
	if err != nil {
		return fmt.Errorf("datasource is unreachable")
	}
	if err := opened.Close(); err != nil {
		return fmt.Errorf("close datasource probe")
	}
	return nil
}

type demoSeedDependencies struct {
	lookupEnv func(string) (string, bool)
	pinger    demoseed.DatasourcePinger
}

func defaultDemoSeedDependencies() demoSeedDependencies {
	return demoSeedDependencies{lookupEnv: os.LookupEnv, pinger: realDemoDatasourcePinger{}}
}

func newDemoSeedCommand(dependencies demoSeedDependencies) *cobra.Command {
	var configPath, manifestPath, anchorText string
	var verifyOnly bool
	command := &cobra.Command{
		Use:   "demo-seed",
		Short: "Create or verify the deterministic Live Demo seed",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			result, manifest, err := executeDemoSeed(
				command.Context(), configPath, manifestPath, anchorText, verifyOnly, dependencies,
			)
			if err != nil {
				return err
			}
			datasourceIDs, agentIDs := demoseed.SortedFixedIDs(manifest)
			status := "DEMO_SEED_OK"
			if verifyOnly {
				status = "DEMO_SEED_VERIFY_OK"
			}
			_, err = fmt.Fprintf(command.OutOrStdout(),
				"%s datasource_ids=%s agent_ids=%s datasources=%d agents=%d policies=%d mask_rules=%d rules=%d audits=%d approvals=%d\n",
				status, strings.Join(datasourceIDs, ","), strings.Join(agentIDs, ","),
				result.Datasources, result.Agents, result.Policies, result.MaskRules,
				result.Rules, result.Audits, result.Approvals,
			)
			if err != nil {
				return fmt.Errorf("write demo seed result: %w", err)
			}
			return nil
		},
	}
	command.Flags().StringVarP(&configPath, "config", "c", "", "path to the demo YAML configuration")
	command.Flags().StringVar(&manifestPath, "manifest", "", "path to the demo seed YAML manifest")
	command.Flags().StringVar(&anchorText, "anchor-date", "", "most recent demo day in YYYY-MM-DD format")
	command.Flags().BoolVar(&verifyOnly, "verify-only", false, "verify the complete seed without writing data")
	_ = command.MarkFlagRequired("config")
	_ = command.MarkFlagRequired("manifest")
	_ = command.MarkFlagRequired("anchor-date")
	return command
}

func executeDemoSeed(
	ctx context.Context,
	configPath, manifestPath, anchorText string,
	verifyOnly bool,
	dependencies demoSeedDependencies,
) (summary demoseed.Summary, manifest demoseed.Manifest, resultErr error) {
	anchor, err := time.Parse("2006-01-02", anchorText)
	if err != nil || anchor.Format("2006-01-02") != anchorText {
		return summary, manifest, fmt.Errorf("demo-seed --anchor-date must be a real date in YYYY-MM-DD format")
	}
	if dependencies.lookupEnv == nil || dependencies.pinger == nil {
		return summary, manifest, fmt.Errorf("demo-seed dependencies are unavailable")
	}

	contents, err := os.ReadFile(configPath)
	if err != nil {
		return summary, manifest, fmt.Errorf("read demo configuration %q: %w", configPath, err)
	}
	loaded, err := config.Parse(contents)
	if err != nil {
		return summary, manifest, fmt.Errorf("parse demo configuration %q: %w", configPath, err)
	}
	if !loaded.DemoEnabled() {
		return summary, manifest, fmt.Errorf("demo-seed refused: demo.enabled must be true")
	}
	resolved, err := config.ResolveStore(&loaded, dependencies.lookupEnv)
	if err != nil {
		return summary, manifest, fmt.Errorf("resolve demo control store: configuration is invalid")
	}

	manifestContents, err := os.ReadFile(manifestPath)
	if err != nil {
		return summary, manifest, fmt.Errorf("read demo seed manifest %q: %w", manifestPath, err)
	}
	manifest, err = demoseed.ParseManifest(manifestContents)
	if err != nil {
		return summary, manifest, err
	}
	if err := manifest.Validate(loaded); err != nil {
		return summary, manifest, fmt.Errorf("validate demo seed manifest: %w", err)
	}
	secrets, err := demoseed.LoadSecrets(manifest, dependencies.lookupEnv)
	if err != nil {
		return summary, manifest, err
	}
	secret, ok := dependencies.lookupEnv("AGENTSQL_SECRET")
	if !ok || secret == "" {
		return summary, manifest, fmt.Errorf("required environment variable AGENTSQL_SECRET is missing or empty")
	}

	options := resolved.MetadataOptions()
	options.AutoMigrate = !verifyOnly
	options.Audit.AutoMigrate = !verifyOnly
	if verifyOnly && options.Driver == store.DialectSQLite {
		info, statErr := os.Stat(options.SQLitePath)
		if statErr != nil || info.IsDir() {
			return summary, manifest, fmt.Errorf("verify demo control store: existing SQLite database is required")
		}
	}
	if !verifyOnly && options.Driver == store.DialectSQLite {
		if err := os.MkdirAll(filepath.Dir(filepath.Clean(options.SQLitePath)), 0o750); err != nil {
			return summary, manifest, fmt.Errorf("prepare demo control store directory: operation failed")
		}
	}
	opened, err := store.OpenMetadata(ctx, options, []byte(secret))
	if err != nil {
		return summary, manifest, fmt.Errorf("open demo control store: operation failed")
	}
	defer func() {
		if closeErr := opened.Close(); closeErr != nil && resultErr == nil {
			resultErr = fmt.Errorf("close demo control store: operation failed")
		}
	}()

	summary, err = demoseed.Run(ctx, opened, manifest, secrets, anchor, verifyOnly, dependencies.pinger)
	if err != nil {
		return demoseed.Summary{}, manifest, err
	}
	return summary, manifest, nil
}
