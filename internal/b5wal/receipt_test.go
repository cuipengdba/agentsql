package b5wal

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"testing"
)

func lostReceipt() ResultReceipt {
	var eventUUID [16]byte
	eventUUID[0] = 7
	return ResultReceipt{
		Key:                          ReceiptKey{SessionID: "session-1", RequestID: "request-1", EventUUID: eventUUID, AttemptGeneration: 3},
		BusinessEventDigest:          sha256.Sum256([]byte("business-event")),
		WALAppendReceiptDigest:       sha256.Sum256([]byte("wal-append-receipt")),
		ReportedDurabilityAtResponse: DurabilityLost,
		AppendConfirmation:           AppendTimeout,
		Reconciliation:               ReconciliationNone,
		Delivery:                     ResponsePrepared,
	}
}

func TestReceiptMustPersistBeforeTerminalResponse(t *testing.T) {
	store := NewMemoryReceiptStore()
	store.PutErr = errors.New("durable store unavailable")
	sent := false
	err := PersistThenSend(context.Background(), store, lostReceipt(), func(ResultReceipt) error {
		sent = true
		return nil
	})
	if err == nil || sent {
		t.Fatalf("err=%v sent=%v", err, sent)
	}

	store = NewMemoryReceiptStore()
	err = PersistThenSend(context.Background(), store, lostReceipt(), func(receipt ResultReceipt) error {
		sent = true
		if receipt.Delivery != ResponseSendStarted || receipt.ReportedDurabilityAtResponse != DurabilityLost {
			t.Fatalf("receipt at send = %+v", receipt)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, ok, err := store.Get(context.Background(), lostReceipt().Key)
	if err != nil || !ok || stored.Delivery != ResponseSendCompleted {
		t.Fatalf("stored=%+v ok=%v err=%v", stored, ok, err)
	}
}

func TestTimeoutLostLateSuccessReceiptSurvivesRestart(t *testing.T) {
	store := NewMemoryReceiptStore()
	receipt := lostReceipt()
	if _, _, err := store.PutWriteOnce(context.Background(), receipt); err != nil {
		t.Fatal(err)
	}
	late := AppendLateConfirmed
	advanced, err := store.Advance(context.Background(), receipt.Key, ReceiptAdvance{Append: &late})
	if err != nil {
		t.Fatal(err)
	}
	if advanced.ReportedDurabilityAtResponse != DurabilityLost {
		t.Fatal("late callback upgraded historical response")
	}

	// A logical process restart retains only the ReceiptStore and verified WAL.
	view, err := RestartJoin(context.Background(), store, receipt.Key, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if view.Receipt.ReportedDurabilityAtResponse != DurabilityLost || view.Receipt.AppendConfirmation != AppendRecovered || view.Receipt.Reconciliation != ReconciliationReplayStaged {
		t.Fatalf("restart staging view = %+v", view)
	}
	view, err = RestartJoin(context.Background(), store, receipt.Key, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if view.Receipt.ReportedDurabilityAtResponse != DurabilityLost || view.Receipt.Reconciliation != ReconciliationPrimaryDurable {
		t.Fatalf("restart durable view = %+v", view)
	}
}

func TestValidRecordWithoutReceiptDoesNotInferHistoricalPending(t *testing.T) {
	store := NewMemoryReceiptStore()
	view, err := RestartJoin(context.Background(), store, lostReceipt().Key, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if view.HistoricalResponse != HistoricalResponseUnknown || view.Receipt.ReportedDurabilityAtResponse != DurabilityUnspecified {
		t.Fatalf("scanner inferred a historical response: %+v", view)
	}
}

func TestReceiptCrashWindowsRemainMonotonic(t *testing.T) {
	receipt := lostReceipt()
	states := []struct {
		name     string
		prepare  func(*MemoryReceiptStore)
		expected ResponseDeliveryState
		exists   bool
	}{
		{"metadata-before", func(*MemoryReceiptStore) {}, ResponsePrepared, false},
		{"metadata-after-response-before", func(store *MemoryReceiptStore) { _, _, _ = store.PutWriteOnce(context.Background(), receipt) }, ResponsePrepared, true},
		{"response-started", func(store *MemoryReceiptStore) {
			_, _, _ = store.PutWriteOnce(context.Background(), receipt)
			state := ResponseSendStarted
			_, _ = store.Advance(context.Background(), receipt.Key, ReceiptAdvance{Delivery: &state})
		}, ResponseSendStarted, true},
		{"response-after", func(store *MemoryReceiptStore) {
			_ = PersistThenSend(context.Background(), store, receipt, func(ResultReceipt) error { return nil })
		}, ResponseSendCompleted, true},
	}
	for _, state := range states {
		t.Run(state.name, func(t *testing.T) {
			store := NewMemoryReceiptStore()
			state.prepare(store)
			stored, exists, err := store.Get(context.Background(), receipt.Key)
			if err != nil || exists != state.exists {
				t.Fatalf("exists=%v err=%v", exists, err)
			}
			if exists && (stored.Delivery != state.expected || stored.ReportedDurabilityAtResponse != DurabilityLost) {
				t.Fatalf("stored = %+v", stored)
			}
		})
	}

	store := NewMemoryReceiptStore()
	receipt.AppendConfirmation = AppendUnknown
	if _, _, err := store.PutWriteOnce(context.Background(), receipt); err != nil {
		t.Fatal(err)
	}
	recovered := AppendRecovered
	if _, err := store.Advance(context.Background(), receipt.Key, ReceiptAdvance{Append: &recovered}); !errors.Is(err, ErrReceiptTransition) {
		t.Fatalf("UNKNOWN->RECOVERED bypass accepted: %v", err)
	}
	timeout := AppendTimeout
	if _, err := store.Advance(context.Background(), receipt.Key, ReceiptAdvance{Append: &timeout}); err != nil {
		t.Fatal(err)
	}
	late := AppendLateConfirmed
	if _, err := store.Advance(context.Background(), receipt.Key, ReceiptAdvance{Append: &late}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Advance(context.Background(), receipt.Key, ReceiptAdvance{Append: &timeout}); !errors.Is(err, ErrReceiptTransition) {
		t.Fatalf("late callback regressed: %v", err)
	}
}

func TestReceiptWriteOnceUnderConcurrency(t *testing.T) {
	store := NewMemoryReceiptStore()
	receipt := lostReceipt()
	const goroutines = 32
	var wait sync.WaitGroup
	wait.Add(goroutines)
	errorsFound := make(chan error, goroutines)
	for range goroutines {
		go func() {
			defer wait.Done()
			_, _, err := store.PutWriteOnce(context.Background(), receipt)
			errorsFound <- err
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	conflict := receipt
	conflict.BusinessEventDigest = sha256.Sum256([]byte("different payload"))
	if _, _, err := store.PutWriteOnce(context.Background(), conflict); !errors.Is(err, ErrReceiptConflict) {
		t.Fatalf("digest conflict = %v", err)
	}
}
