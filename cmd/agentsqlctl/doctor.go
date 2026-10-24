package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/version"
	"github.com/spf13/cobra"
)

type doctorCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Driver  string `json:"driver,omitempty"`
	Current *int   `json:"current,omitempty"`
	Latest  *int   `json:"latest,omitempty"`
	Action  string `json:"action,omitempty"`
}

type doctorReport struct {
	Version  string        `json:"version"`
	ReadOnly bool          `json:"read_only"`
	Status   string        `json:"status"`
	Checks   []doctorCheck `json:"checks"`
}

type doctorFailure struct{ code int }

func (failure doctorFailure) Error() string {
	return "doctor reported " + map[int]string{1: "blocked", 2: "pending"}[failure.code]
}
func (failure doctorFailure) ExitCode() int { return failure.code }

func newDoctorCommand() *cobra.Command {
	var configPath, format string
	var timeout time.Duration
	command := &cobra.Command{
		Use:   "doctor",
		Short: "Read-only deployment and migration readiness report",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if timeout <= 0 {
				return errors.New("doctor timeout must be positive")
			}
			if format != "json" && format != "text" {
				return errors.New("doctor format must be json or text")
			}
			ctx, cancel := context.WithTimeout(command.Context(), timeout)
			defer cancel()
			report := inspectDeployment(ctx, configPath)
			if err := writeDoctorReport(command.OutOrStdout(), report, format); err != nil {
				return err
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
	command.Flags().StringVarP(&configPath, "config", "c", defaultConfigPath, "path to the YAML configuration file")
	command.Flags().StringVar(&format, "format", "json", "report format: json or text")
	command.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "read-only preflight timeout")
	return command
}

// inspectDeployment uses Parse, not Load: Load can create a persistent B2
// instance ID. The migration checker receives the resolved targets directly
// and opens SQLite in mode=ro, so this command never performs DDL or creates
// an absent database file.
func inspectDeployment(ctx context.Context, path string) doctorReport {
	report := doctorReport{Version: version.Version, ReadOnly: true, Status: "blocked", Checks: []doctorCheck{}}
	contents, err := readDiagnosticConfig(path)
	if err != nil {
		report.Checks = append(report.Checks, doctorCheck{Name: "config", Status: "blocked", Action: "read the selected configuration file"})
		return report
	}
	loaded, err := config.Parse(contents)
	if err != nil {
		report.Checks = append(report.Checks, doctorCheck{Name: "config", Status: "blocked", Action: "correct the configuration syntax and values"})
		return report
	}
	redaction, err := config.ResolveRedaction(&loaded, os.LookupEnv)
	if err != nil {
		report.Checks = append(report.Checks, doctorCheck{Name: "config", Status: "blocked", Action: "correct the redaction key configuration"})
		return report
	}
	defer redaction.Clear()
	if _, err := config.BuildRedactionAssembly(redaction); err != nil {
		report.Checks = append(report.Checks, doctorCheck{Name: "config", Status: "blocked", Action: "correct the redaction key configuration"})
		return report
	}
	resolved, err := config.ResolveStore(&loaded, os.LookupEnv)
	if err != nil {
		report.Checks = append(report.Checks, doctorCheck{Name: "config", Status: "blocked", Action: "correct the metadata and audit store configuration"})
		return report
	}
	report.Checks = append(report.Checks, doctorCheck{Name: "config", Status: "ready"})
	results, checkErr := checkMigrationsResolved(ctx, resolved)
	for _, result := range results {
		current, latest := result.current, result.latest
		check := doctorCheck{Name: result.role + "_schema", Driver: string(result.driver), Current: &current, Latest: &latest}
		switch {
		case result.current < result.latest:
			check.Status = "pending"
			check.Action = "stop writes, back up metadata and audit together, then run agentsqlctl migrate"
		case result.current > result.latest:
			check.Status = "blocked"
			check.Action = "use a binary compatible with this newer schema; do not downgrade in place"
		default:
			check.Status = "ready"
		}
		report.Checks = append(report.Checks, check)
	}
	if checkErr == nil {
		report.Status = "ready"
		return report
	}
	var pending migrationsPendingError
	if errors.As(checkErr, &pending) {
		report.Status = "pending"
		return report
	}
	role := "metadata_schema"
	if len(results) == 1 && !resolved.Audit.ReuseMetadata && strings.Contains(checkErr.Error(), "audit") {
		role = "audit_schema"
	}
	if len(report.Checks) > 1 && report.Checks[len(report.Checks)-1].Name == role {
		report.Checks[len(report.Checks)-1].Status = "blocked"
		report.Checks[len(report.Checks)-1].Action = "inspect store availability and contiguous migration history"
	} else {
		report.Checks = append(report.Checks, doctorCheck{Name: role, Status: "blocked", Action: "inspect store availability and contiguous migration history"})
	}
	return report
}

func readDiagnosticConfig(path string) ([]byte, error) {
	before, err := os.Stat(path)
	if err != nil || !before.Mode().IsRegular() || before.Size() > 1<<20 {
		return nil, errors.New("invalid diagnostic configuration file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot read diagnostic configuration")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(before, info) || info.Size() > 1<<20 {
		return nil, errors.New("invalid diagnostic configuration file")
	}
	contents, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(contents) > 1<<20 {
		return nil, errors.New("cannot read bounded diagnostic configuration")
	}
	return contents, nil
}

func writeDoctorReport(writer io.Writer, report doctorReport, format string) error {
	if format == "json" {
		return json.NewEncoder(writer).Encode(report)
	}
	if _, err := fmt.Fprintf(writer, "doctor version=%s status=%s read_only=true\n", report.Version, report.Status); err != nil {
		return err
	}
	for _, check := range report.Checks {
		if _, err := fmt.Fprintf(writer, "check name=%s status=%s driver=%s", check.Name, check.Status, check.Driver); err != nil {
			return err
		}
		if check.Current != nil && check.Latest != nil {
			if _, err := fmt.Fprintf(writer, " current=%d latest=%d", *check.Current, *check.Latest); err != nil {
				return err
			}
		}
		if check.Action != "" {
			if _, err := fmt.Fprintf(writer, " action=%q", check.Action); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(writer); err != nil {
			return err
		}
	}
	return nil
}
