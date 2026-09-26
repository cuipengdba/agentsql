//go:build agentsql_b5_soak

package b5soak

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
)

const soakSmokeGate = "AGENTSQL_B5_SOAK_SMOKE"

// TestPostgres18SoakSmoke is deliberately gated: the normal test suite never
// starts Docker or turns a short run into S10 qualification evidence.
func TestPostgres18SoakSmoke(t *testing.T) {
	if os.Getenv(soakSmokeGate) != "1" {
		t.Skip(soakSmokeGate + "=1 is required")
	}
	t.Setenv(GateEnvironment, "1")
	duration := smokeDuration("AGENTSQL_B5_SOAK_SMOKE_DURATION", 5*time.Second)
	terminals := smokeUint("AGENTSQL_B5_SOAK_SMOKE_TERMINALS", 20)
	rate := smokeFloat("AGENTSQL_B5_SOAK_SMOKE_RATE", 20)
	ctx, cancel := context.WithTimeout(context.Background(), duration+3*time.Minute)
	defer cancel()
	container, err := postgrescontainer.Run(ctx, "postgres:18",
		postgrescontainer.WithDatabase("agentsql_soak"),
		postgrescontainer.WithUsername("agentsql"),
		postgrescontainer.WithPassword("soak-password"),
		postgrescontainer.BasicWaitStrategies(),
	)
	if err != nil {
		if container != nil {
			testcontainers.CleanupContainer(t, container)
		}
		t.Fatal(err)
	}
	testcontainers.CleanupContainer(t, container)
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := container.MappedPort(ctx, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	if configured := os.Getenv("AGENTSQL_B5_SOAK_SMOKE_OUTPUT_DIR"); configured != "" {
		directory = configured
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	runner, err := New(Config{
		DSN:          fmt.Sprintf("postgres://agentsql:soak-password@%s:%s/agentsql_soak?sslmode=disable", host, port.Port()),
		DatasourceID: "pg18-smoke", Duration: duration, Terminals: terminals, Rate: rate,
		HardBudget: 100, InventoryPeriod: 500 * time.Millisecond, InventoryStop: 30 * time.Second,
		ReportPeriod: time.Second, OutputPath: filepath.Join(directory, "report.json"), WALPath: filepath.Join(directory, "terminal.wal"),
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := runner.Run(ctx)
	if err != nil {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if !report.RunPass || report.OfficialS10Qualified || report.NormalUnknown != 0 || report.ClaimDrift != 0 || !report.WAL.Continuous || !report.WAL.Verified || report.WAL.TerminalRecords != report.Terminals {
		t.Fatalf("unexpected smoke report: %+v", report)
	}
	if report.Status != "PASS" || report.Quarantine.Count != 0 || report.Quarantine.ChargedSlots != 0 || report.Inventory.StopLineBreached || !report.Checks.RequestedDurationMet || !report.Checks.RequestedTerminalsMet {
		t.Fatalf("incomplete smoke S10 checks: %+v", report)
	}

	t.Run("inventory-stop-line", func(t *testing.T) {
		faultDirectory := filepath.Join(directory, "inventory-stop-line")
		faultRunner, newErr := New(Config{
			DSN:          fmt.Sprintf("postgres://agentsql:soak-password@%s:%s/agentsql_soak?sslmode=disable", host, port.Port()),
			DatasourceID: "pg18-inventory-stop", Duration: 5 * time.Second, Terminals: 100, Rate: 50,
			HardBudget: 100, InventoryPeriod: 50 * time.Millisecond, InventoryStop: 250 * time.Millisecond,
			ReportPeriod: 50 * time.Millisecond, OutputPath: filepath.Join(faultDirectory, "report.json"), WALPath: filepath.Join(faultDirectory, "terminal.wal"),
		})
		if newErr != nil {
			t.Fatal(newErr)
		}
		faultRunner.testAfterTerminal = func(runtime *pgRuntime, ordinal uint64) {
			if ordinal == 1 {
				runtime.injectInventoryFailures(100)
			}
		}
		faultReport, runErr := faultRunner.Run(ctx)
		if runErr == nil || faultReport.Status != "FAIL" || faultReport.StopReason == "" || !faultReport.Inventory.StopLineBreached || faultReport.Inventory.MaxUnavailableSeconds < 0.25 || faultReport.RunPass {
			t.Fatalf("inventory stop report=%+v err=%v", faultReport, runErr)
		}
	})
}

func smokeDuration(name string, fallback time.Duration) time.Duration {
	if value, err := time.ParseDuration(os.Getenv(name)); err == nil && value > 0 {
		return value
	}
	return fallback
}

func smokeUint(name string, fallback uint64) uint64 {
	if value, err := strconv.ParseUint(os.Getenv(name), 10, 64); err == nil && value > 0 {
		return value
	}
	return fallback
}

func smokeFloat(name string, fallback float64) float64 {
	if value, err := strconv.ParseFloat(os.Getenv(name), 64); err == nil && value > 0 {
		return value
	}
	return fallback
}
