//go:build agentsql_b5_soak

package b5soak

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const longSoakGate = "AGENTSQL_B5_SOAK_LONG"

// TestLongS10Soak is the release-owner entry point for official evidence. It
// cannot be shortened: qualification requires both 24h and 10,000 terminals.
func TestLongS10Soak(t *testing.T) {
	if os.Getenv(longSoakGate) != "1" {
		t.Skip(longSoakGate + "=1 is required")
	}
	dsn := os.Getenv("AGENTSQL_B5_SOAK_DSN")
	if dsn == "" {
		t.Fatal("AGENTSQL_B5_SOAK_DSN is required")
	}
	t.Setenv(GateEnvironment, "1")
	directory := os.Getenv("AGENTSQL_B5_SOAK_OUTPUT_DIR")
	if directory == "" {
		directory = filepath.Join("artifacts", "b5-soak-long", time.Now().UTC().Format("20060102T150405Z"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), OfficialMinDuration+2*time.Hour)
	defer cancel()
	runner, err := New(Config{
		DSN: dsn, DatasourceID: "s10-long", Duration: OfficialMinDuration, Terminals: OfficialMinTerminals, Rate: 0.2,
		HardBudget: 100, InventoryPeriod: DefaultInventoryPeriod, InventoryStop: DefaultInventoryStop,
		ReportPeriod: 10 * time.Second, OutputPath: filepath.Join(directory, "report.json"), WALPath: filepath.Join(directory, "terminal.wal"),
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := runner.Run(ctx)
	if err != nil {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if !report.OfficialS10Qualified || report.Terminals < OfficialMinTerminals || report.ElapsedSeconds < OfficialMinDuration.Seconds() {
		t.Fatalf("run did not qualify: %+v", report)
	}
}
