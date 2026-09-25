package b5wal

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestTimeoutLostLateSuccessRestartReplayWithAndWithoutReceipt(t *testing.T) {
	record, key := maximalRecord()
	extent, err := EncodeRecord(key, record)
	if err != nil {
		t.Fatal(err)
	}
	// This verified extent models timeout -> late fsync success -> kill/restart.
	decoded, err := DecodeRecord(key, extent)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.EventPayloadDigest != recordDigest(record.CanonicalEvent) {
		t.Fatal("restart scanner payload mismatch")
	}

	t.Run("receipt persisted", func(t *testing.T) {
		store := NewMemoryReceiptStore()
		receipt := lostReceipt()
		if _, _, err := store.PutWriteOnce(context.Background(), receipt); err != nil {
			t.Fatal(err)
		}
		late := AppendLateConfirmed
		if _, err := store.Advance(context.Background(), receipt.Key, ReceiptAdvance{Append: &late}); err != nil {
			t.Fatal(err)
		}
		view, err := RestartJoin(context.Background(), store, receipt.Key, true, true)
		if err != nil {
			t.Fatal(err)
		}
		if view.Receipt.ReportedDurabilityAtResponse != DurabilityLost || view.Receipt.AppendConfirmation != AppendRecovered || view.Receipt.Reconciliation != ReconciliationPrimaryDurable {
			t.Fatalf("persisted receipt recovery = %+v", view)
		}
	})

	t.Run("receipt missing", func(t *testing.T) {
		view, err := RestartJoin(context.Background(), NewMemoryReceiptStore(), lostReceipt().Key, true, true)
		if err != nil {
			t.Fatal(err)
		}
		if view.HistoricalResponse != HistoricalResponseUnknown || view.Receipt.ReportedDurabilityAtResponse != DurabilityUnspecified {
			t.Fatalf("missing receipt recovery = %+v", view)
		}
	})
}

func TestKillCrashPointsAroundRecordAndRecovery(t *testing.T) {
	record, key := maximalRecord()
	extent, err := EncodeRecord(key, record)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		bytes []byte
		valid bool
	}{
		{"kill-before-record-write", make([]byte, len(extent)), false},
		{"kill-during-record-write", append([]byte(nil), extent[:len(extent)/2]...), false},
		{"kill-after-record-write-before-callback", extent, true},
		{"kill-after-late-callback", extent, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeRecord(key, test.bytes)
			if test.valid && err != nil {
				t.Fatal(err)
			}
			if !test.valid && err == nil {
				t.Fatal("crash-corrupt extent accepted")
			}
		})
	}

	store := NewMemoryReceiptStore()
	receipt := lostReceipt()
	if _, _, err := store.PutWriteOnce(context.Background(), receipt); err != nil {
		t.Fatal(err)
	}
	// Recovery-event-before/after is represented by staged vs primary durable;
	// both preserve the response-time LOST value.
	before, err := RestartJoin(context.Background(), store, receipt.Key, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if before.Receipt.Reconciliation != ReconciliationReplayStaged {
		t.Fatalf("before recovery event = %+v", before)
	}
	after, err := RestartJoin(context.Background(), store, receipt.Key, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if after.Receipt.Reconciliation != ReconciliationPrimaryDurable || after.Receipt.ReportedDurabilityAtResponse != DurabilityLost {
		t.Fatalf("after recovery event = %+v", after)
	}

	regress := ReconciliationNone
	if _, err := store.Advance(context.Background(), receipt.Key, ReceiptAdvance{Reconciliation: &regress}); !errors.Is(err, ErrReceiptTransition) {
		t.Fatalf("recovery regression = %v", err)
	}
}

func TestAbruptProcessKillAfterRecordWrite(t *testing.T) {
	if os.Getenv("AGENTSQL_B5WAL_KILL_HELPER") == "1" {
		record, key := maximalRecord()
		extent, err := EncodeRecord(key, record)
		if err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(os.Getenv("AGENTSQL_B5WAL_KILL_DATA"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(extent); err != nil {
			t.Fatal(err)
		}
		// Deliberately do not Sync or Close the record file. The marker tells
		// the parent that the write returned, after which the process is killed.
		if err := os.WriteFile(os.Getenv("AGENTSQL_B5WAL_KILL_READY"), []byte("ready"), 0o600); err != nil {
			t.Fatal(err)
		}
		for {
			time.Sleep(time.Hour)
		}
	}

	directory := t.TempDir()
	dataPath := filepath.Join(directory, "record.extent")
	readyPath := filepath.Join(directory, "ready")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestAbruptProcessKillAfterRecordWrite$")
	command.Env = append(os.Environ(),
		"AGENTSQL_B5WAL_KILL_HELPER=1",
		"AGENTSQL_B5WAL_KILL_DATA="+dataPath,
		"AGENTSQL_B5WAL_KILL_READY="+readyPath,
	)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = command.Process.Kill()
			_, _ = command.Process.Wait()
			t.Fatal("kill helper did not reach post-write crash point")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_, _ = command.Process.Wait()
	extent, err := os.ReadFile(dataPath)
	if err != nil {
		t.Fatal(err)
	}
	_, key := maximalRecord()
	if _, err := DecodeRecord(key, extent); err != nil {
		t.Fatalf("scanner rejected complete post-kill extent: %v", err)
	}
}

func recordDigest(payload []byte) [32]byte { return sha256.Sum256(payload) }
