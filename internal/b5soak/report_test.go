//go:build agentsql_b5_soak

package b5soak

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/b5terminal"
)

func TestRecoveryRequiresDistinctRuntimeAndQAApprovers(t *testing.T) {
	tests := []struct {
		name       string
		approvals  []RecoveryApproval
		drift      int64
		quarantine int64
		inventory  bool
		want       bool
	}{
		{name: "no request"},
		{name: "missing qa release", approvals: []RecoveryApproval{{Role: "runtime-sre", Subject: "alice"}}, inventory: true},
		{name: "missing runtime sre", approvals: []RecoveryApproval{{Role: "qa-release", Subject: "bob"}}, inventory: true},
		{name: "same person", approvals: []RecoveryApproval{{Role: "runtime-sre", Subject: "alice"}, {Role: "qa-release", Subject: "alice"}}},
		{name: "dirty drift", approvals: []RecoveryApproval{{Role: "runtime-sre", Subject: "alice"}, {Role: "qa-release", Subject: "bob"}}, drift: 1, inventory: true},
		{name: "dirty quarantine", approvals: []RecoveryApproval{{Role: "runtime-sre", Subject: "alice"}, {Role: "qa-release", Subject: "bob"}}, quarantine: 1, inventory: true},
		{name: "inventory unhealthy", approvals: []RecoveryApproval{{Role: "runtime-sre", Subject: "alice"}, {Role: "qa-release", Subject: "bob"}}},
		{name: "two person", approvals: []RecoveryApproval{{Role: "runtime-sre", Subject: "alice"}, {Role: "qa-release", Subject: "bob"}}, inventory: true, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision := EvaluateRecovery(test.approvals, test.drift, test.quarantine, test.inventory)
			if decision.Eligible != test.want {
				t.Fatalf("decision=%+v want eligible=%t", decision, test.want)
			}
		})
	}
}

func TestClaimMonitorDetectsCountAndIdentityDrift(t *testing.T) {
	monitor := newClaimMonitor()
	first := backendKey{PID: 10, Started: time.Unix(100, 0).UTC()}
	monitor.active[first] = struct{}{}
	if drift := monitor.drift(nil); drift != 1 {
		t.Fatalf("missing observed backend drift=%d", drift)
	}
	if drift := monitor.drift(map[backendKey]struct{}{{PID: 10, Started: time.Unix(101, 0).UTC()}: {}}); drift == 0 {
		t.Fatal("identity mismatch was hidden by equal counts")
	}
	if drift := monitor.drift(map[backendKey]struct{}{first: {}}); drift != 0 {
		t.Fatalf("exact inventory drift=%d", drift)
	}
}

func TestSoakWALRejectsTampering(t *testing.T) {
	path := filepath.Join(t.TempDir(), "terminal.wal")
	wal, err := newSoakWAL(path)
	if err != nil {
		t.Fatal(err)
	}
	report := Report{RunID: "tamper", DatasourceID: "test", Quarantine: QuarantineStats{HardBudget: 10}}
	if err := wal.appendRunStart(context.Background(), report); err != nil {
		t.Fatal(err)
	}
	if err := wal.appendRunFinish(context.Background(), report); err != nil {
		t.Fatal(err)
	}
	if err := wal.writer.Seal(); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.WriteAt([]byte{0}, 0); err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wal.verify(); err == nil {
		t.Fatal("tampered WAL verified")
	}
}

func TestConfigRequiresExplicitGateAndNeverExemptsTime(t *testing.T) {
	config := Config{
		DSN: "postgres://ignored", DatasourceID: "test", Duration: time.Second,
		Terminals: 1, Rate: 1, HardBudget: 10, InventoryPeriod: time.Second,
		InventoryStop: 2 * time.Second, ReportPeriod: time.Second,
		OutputPath: filepath.Join(t.TempDir(), "report.json"), WALPath: filepath.Join(t.TempDir(), "terminal.wal"),
	}
	if err := config.Validate(); err == nil {
		t.Fatal("soak gate was optional")
	}
	t.Setenv(GateEnvironment, "1")
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	if OfficialMinDuration != 24*time.Hour || OfficialMinTerminals != 10_000 {
		t.Fatalf("official thresholds changed: %s/%d", OfficialMinDuration, OfficialMinTerminals)
	}
}

func TestSoakWALRoundTripAndChain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "terminal.wal")
	wal, err := newSoakWAL(path)
	if err != nil {
		t.Fatal(err)
	}
	result := b5terminal.PGTerminalResult{Resolution: b5terminal.TerminalResolution{
		Outcome:     b5terminal.OutcomeCommitted,
		Consistency: b5terminal.ConsistencyResult{Verdict: b5terminal.VerdictConsistent},
		Disposition: b5terminal.DispositionReleased,
	}}
	report := Report{RunID: "run", DatasourceID: "test", Thresholds: Thresholds{RequestedDurationSeconds: 1, RequestedTerminals: 3}, Quarantine: QuarantineStats{HardBudget: 100}}
	if err := wal.appendRunStart(context.Background(), report); err != nil {
		t.Fatal(err)
	}
	for ordinal := uint64(1); ordinal <= 3; ordinal++ {
		if err := wal.appendTerminal(context.Background(), "tx", ordinal, result); err != nil {
			t.Fatal(err)
		}
	}
	report.Terminals, report.Committed = 3, 3
	if err := wal.appendRunFinish(context.Background(), report); err != nil {
		t.Fatal(err)
	}
	if err := wal.sealAndVerify(); err != nil {
		t.Fatal(err)
	}
	if wal.verification.Records != 5 || wal.verification.TerminalRecords != 3 {
		t.Fatalf("verification=%+v", wal.verification)
	}
}
