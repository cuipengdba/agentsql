//go:build agentsql_b5_soak

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/cuipengdba/agentsql/internal/b5soak"
)

type approvalFlags []string

func (values *approvalFlags) String() string { return strings.Join(*values, ",") }
func (values *approvalFlags) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func main() {
	os.Exit(run())
}

func run() int {
	stamp := time.Now().UTC().Format("20060102T150405Z")
	defaultDirectory := filepath.Join("artifacts", "b5-soak", stamp)
	var approvals approvalFlags
	dsn := flag.String("dsn", os.Getenv("AGENTSQL_B5_SOAK_DSN"), "PostgreSQL DSN (or AGENTSQL_B5_SOAK_DSN; never written to the report)")
	datasource := flag.String("datasource", "s10-soak", "non-secret datasource evidence label")
	duration := flag.Duration("duration", b5soak.OfficialMinDuration, "minimum observation duration")
	terminals := flag.Uint64("terminals", b5soak.OfficialMinTerminals, "minimum successful terminal count")
	rate := flag.Float64("rate", 1, "maximum terminal starts per second")
	budget := flag.Int64("budget", 100, "datasource hard connection budget")
	inventoryPeriod := flag.Duration("inventory-period", b5soak.DefaultInventoryPeriod, "independent backend inventory period")
	inventoryStop := flag.Duration("inventory-stop", b5soak.DefaultInventoryStop, "stop line for continuous inventory unavailability")
	reportPeriod := flag.Duration("report-period", 10*time.Second, "JSON checkpoint period")
	output := flag.String("output", filepath.Join(defaultDirectory, "report.json"), "machine-readable JSON report path")
	walPath := flag.String("wal", filepath.Join(defaultDirectory, "terminal.wal"), "append-only terminal evidence WAL path")
	flag.Var(&approvals, "recovery-approval", "record role:subject; repeat for runtime-sre and qa-release (records eligibility only)")
	flag.Parse()

	parsedApprovals, err := parseApprovals(approvals)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	config := b5soak.Config{
		DSN: *dsn, DatasourceID: *datasource, Duration: *duration, Terminals: *terminals,
		Rate: *rate, HardBudget: *budget, InventoryPeriod: *inventoryPeriod,
		InventoryStop: *inventoryStop, ReportPeriod: *reportPeriod,
		OutputPath: *output, WALPath: *walPath, RecoveryApprovers: parsedApprovals,
	}
	runner, err := b5soak.New(config)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	report, runErr := runner.Run(ctx)
	summary, marshalErr := json.Marshal(struct {
		Status               string `json:"status"`
		RunPass              bool   `json:"run_pass"`
		OfficialS10Qualified bool   `json:"official_s10_qualified"`
		Terminals            uint64 `json:"terminals"`
		NormalUnknown        uint64 `json:"normal_unknown"`
		ClaimDrift           int64  `json:"claim_drift"`
		ReportPath           string `json:"report_path"`
	}{report.Status, report.RunPass, report.OfficialS10Qualified, report.Terminals, report.NormalUnknown, report.ClaimDrift, *output})
	if marshalErr == nil {
		fmt.Println(string(summary))
	}
	if runErr != nil {
		fmt.Fprintln(os.Stderr, "agentsql-soak:", runErr)
		return 1
	}
	return 0
}

func parseApprovals(values []string) ([]b5soak.RecoveryApproval, error) {
	result := make([]b5soak.RecoveryApproval, 0, len(values))
	for _, value := range values {
		parts := strings.SplitN(value, ":", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return nil, errors.New("--recovery-approval must be role:subject")
		}
		result = append(result, b5soak.RecoveryApproval{Role: parts[0], Subject: parts[1]})
	}
	return result, nil
}
