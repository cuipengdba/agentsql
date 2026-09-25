package b5wal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type unavailablePrimary struct{ err error }

func (primary unavailablePrimary) Put(context.Context, PrimaryEvent) (bool, error) {
	return false, primary.err
}

func TestServiceReservationWedgeRotationAndSixRecordBound(t *testing.T) {
	factory, _ := newTestFactory()
	var sinks []*scriptedSink
	manager, err := NewManager(context.Background(), factory, 1, func(Segment) (ExtentSink, error) {
		sink := newScriptedSink()
		sinks = append(sinks, sink)
		return sink, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	receipts := NewMemoryReceiptStore()
	service := Service{Primary: unavailablePrimary{err: errors.New("primary down")}, Receipts: receipts, WAL: manager, SyncTimeout: 5 * time.Millisecond}
	var reservationID [16]byte
	reservationID[0] = 1
	reservation, err := NewEmissionReservation(reservationID, LastAfterStatementEffect, TransitionFsyncTimeout)
	if err != nil {
		t.Fatal(err)
	}
	kinds := []RecordKind{RecordStatement, RecordRollback, RecordTerminalOutcome, RecordSessionTerminal, RecordDurabilityDiagnostic, RecordRecovery}
	for index, kind := range kinds {
		var uuid [16]byte
		uuid[0] = byte(index + 1)
		event := ServiceEvent{
			Kind: kind, Outcome: OutcomeCommitted, TransactionID: "tx", TransactionSeq: uint64(index + 1),
			EventUUID: uuid, SchemaID: "agentsql.audit.event.v4", SchemaVersion: 4, CanonicalEvent: []byte{byte(index + 1)},
			SessionID: "session", RequestID: string(rune('a' + index)), AttemptGeneration: 1,
		}
		result, ingestErr := service.IngestAndRespond(context.Background(), reservation, event, func(receipt ResultReceipt) error {
			if receipt.ReportedDurabilityAtResponse != DurabilityLost || receipt.Delivery != ResponseSendStarted {
				t.Fatalf("terminal receipt = %+v", receipt)
			}
			return nil
		})
		if ingestErr != nil {
			t.Fatalf("event %d: %v", index, ingestErr)
		}
		if result.Durability != DurabilityLost {
			t.Fatalf("event %d durability = %s", index, result.Durability)
		}
		if index < 2 {
			sinks[index].sync <- nil // release late callback after response LOST
		}
	}
	if reservation.Emitted() != MaxPathRecordCount || reservation.RemainingBytes() != ReservationCharge()-2*EncodedRecordCharge() {
		t.Fatalf("emitted=%d remaining=%d", reservation.Emitted(), reservation.RemainingBytes())
	}
	if len(sinks) != 2 || len(sinks[0].extents) != 1 || len(sinks[1].extents) != 1 {
		t.Fatalf("rotation/sink writes: sinks=%d writes=%d/%d", len(sinks), len(sinks[0].extents), len(sinks[1].extents))
	}
	var extraUUID [16]byte
	extraUUID[0] = 99
	_, err = service.IngestAndRespond(context.Background(), reservation, ServiceEvent{Kind: RecordRecovery, EventUUID: extraUUID}, func(ResultReceipt) error { return nil })
	if err == nil {
		t.Fatal("seventh emission accepted")
	}
}

func TestServiceResponseRetryDoesNotAppendFactTwice(t *testing.T) {
	factory, _ := newTestFactory()
	sink := newScriptedSink(nil)
	manager, err := NewManager(context.Background(), factory, 1, func(Segment) (ExtentSink, error) { return sink, nil })
	if err != nil {
		t.Fatal(err)
	}
	service := Service{Primary: unavailablePrimary{err: errors.New("primary down")}, Receipts: NewMemoryReceiptStore(), WAL: manager, SyncTimeout: time.Second}
	var reservationID, uuid [16]byte
	reservationID[0], uuid[0] = 8, 9
	reservation, err := NewEmissionReservation(reservationID, LastAfterStatementEffect, TransitionFsyncTimeout)
	if err != nil {
		t.Fatal(err)
	}
	event := ServiceEvent{Kind: RecordStatement, Outcome: OutcomeUnknown, TransactionID: "tx", TransactionSeq: 1, EventUUID: uuid, SchemaID: "agentsql.audit.event.v4", SchemaVersion: 4, CanonicalEvent: []byte("same"), SessionID: "session", RequestID: "request", AttemptGeneration: 1}
	sendErr := errors.New("network crash")
	if _, err := service.IngestAndRespond(context.Background(), reservation, event, func(ResultReceipt) error { return sendErr }); !errors.Is(err, sendErr) {
		t.Fatalf("first send = %v", err)
	}
	if _, err := service.IngestAndRespond(context.Background(), reservation, event, func(ResultReceipt) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(sink.extents) != 1 || reservation.Emitted() != 1 {
		t.Fatalf("writes=%d emitted=%d", len(sink.extents), reservation.Emitted())
	}
}

type fakeKMS struct {
	mu          sync.Mutex
	unavailable bool
	seenAAD     [][]byte
}

func (kms *fakeKMS) Encrypt(_ context.Context, revision string, plaintext, aad []byte) ([]byte, error) {
	kms.mu.Lock()
	defer kms.mu.Unlock()
	if kms.unavailable {
		return nil, errors.New("kms unavailable")
	}
	kms.seenAAD = append(kms.seenAAD, append([]byte(nil), aad...))
	result := append([]byte(revision+":"), plaintext...)
	return result, nil
}

func (kms *fakeKMS) Decrypt(_ context.Context, revision string, ciphertext, aad []byte) ([]byte, error) {
	kms.mu.Lock()
	defer kms.mu.Unlock()
	if kms.unavailable || !bytes.HasPrefix(ciphertext, []byte(revision+":")) {
		return nil, errors.New("kms unavailable")
	}
	kms.seenAAD = append(kms.seenAAD, append([]byte(nil), aad...))
	return append([]byte(nil), ciphertext[len(revision)+1:]...), nil
}

func TestKMSPerSegmentKeyRotationRegistryCASAndUnavailable(t *testing.T) {
	kms := &fakeKMS{}
	wrapper := KMSKeyWrapper{KMS: kms, MasterRevision: "master-r7"}
	registry := NewMemoryKeyRegistry()
	manifests := &MemoryManifestStore{}
	factory := SegmentFactory{MasterRevision: "master-r7", Registry: registry, Wrapper: wrapper, Manifests: manifests}
	first, err := factory.Create(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := factory.Create(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if first.Key == second.Key || first.Manifest.KeyID == second.Manifest.KeyID || bytes.Equal(first.Manifest.WrappedKey, second.Manifest.WrappedKey) {
		t.Fatal("segment rotation reused key identity")
	}
	unwrapped, err := wrapper.UnwrapSegmentKey(context.Background(), first.Manifest)
	if err != nil || unwrapped != first.Key {
		t.Fatalf("unwrap=%x err=%v", unwrapped, err)
	}
	reserved, err := registry.Reserve(context.Background(), first.Manifest.KeyID, first.Manifest.SegmentID, first.Manifest.WrappedKey)
	if err != nil || reserved {
		t.Fatalf("duplicate registry CAS reserved=%v err=%v", reserved, err)
	}
	kms.unavailable = true
	if _, err := factory.Create(context.Background(), 2); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("KMS outage = %v", err)
	}
}

type memoryPrimary struct {
	verifier *ReplayVerifier
	events   map[[16]byte]PrimaryEvent
}

func newMemoryPrimary() *memoryPrimary {
	return &memoryPrimary{verifier: NewReplayVerifier(), events: make(map[[16]byte]PrimaryEvent)}
}

func (primary *memoryPrimary) Put(_ context.Context, event PrimaryEvent) (bool, error) {
	_, duplicate, err := primary.verifier.Ingest(event.ReplayEvent)
	if err != nil {
		return false, err
	}
	if !duplicate {
		primary.events[event.EventUUID] = event
	}
	return duplicate, nil
}

func TestExecutableReconciliationPreservesLostAndDistinguishesOutcome(t *testing.T) {
	factory, _ := newTestFactory()
	segment, err := factory.Create(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	var reservationID, uuid [16]byte
	reservationID[0], uuid[0] = 1, 2
	event := ServiceEvent{Kind: RecordTerminalOutcome, Outcome: OutcomeCommitted, TransactionID: "tx", TransactionSeq: 1, EventUUID: uuid, SchemaID: "agentsql.audit.event.v4", SchemaVersion: 4, CanonicalEvent: []byte(`{"outcome":"committed"}`)}
	record := Record{SegmentID: segment.Manifest.SegmentID, ReservationID: reservationID, EventUUID: uuid, EventSchemaID: event.SchemaID, EventSchemaVersion: event.SchemaVersion, KeyID: segment.Manifest.KeyID, CanonicalEvent: event.CanonicalEvent, Extensions: serviceExtensions(event)}
	copy(record.Nonce[:4], segment.Manifest.NonceDomain[:])
	extent, err := EncodeRecord(segment.Key[:], record)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeRecord(segment.Key[:], extent)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := recoveredRecord(decoded, sha256.Sum256(extent), "test.wal")
	if err != nil {
		t.Fatal(err)
	}
	receipts := NewMemoryReceiptStore()
	receipt := ResultReceipt{
		Key:                 ReceiptKey{SessionID: "session", RequestID: "request", EventUUID: uuid, AttemptGeneration: 1},
		BusinessEventDigest: decoded.EventPayloadDigest, WALAppendReceiptDigest: WALAppendReceiptDigest(recovered.Identity),
		ReportedDurabilityAtResponse: DurabilityLost, AppendConfirmation: AppendTimeout, Reconciliation: ReconciliationNone, Delivery: ResponsePrepared,
	}
	if _, _, err := receipts.PutWriteOnce(context.Background(), receipt); err != nil {
		t.Fatal(err)
	}
	primary := newMemoryPrimary()
	ledger, err := (Reconciler{Primary: primary, Receipts: receipts, Finder: receipts}).Reconcile(context.Background(), []RecoveredRecord{recovered})
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger) != 1 || ledger[0].Classification != RecoveryCommittedAuditPending || ledger[0].ReportedDurability != DurabilityLost || ledger[0].Reconciliation != ReconciliationPrimaryDurable {
		t.Fatalf("ledger = %+v", ledger)
	}
	stored, _, _ := receipts.Get(context.Background(), receipt.Key)
	if stored.ReportedDurabilityAtResponse != DurabilityLost || stored.AppendConfirmation != AppendRecovered {
		t.Fatalf("recovered receipt = %+v", stored)
	}
}

type fixedUnwrapper struct {
	key [32]byte
	err error
}

func (unwrapper fixedUnwrapper) UnwrapSegmentKey(context.Context, SegmentManifest) ([32]byte, error) {
	return unwrapper.key, unwrapper.err
}

type captureQuarantine struct{ paths []string }

func (quarantine *captureQuarantine) Quarantine(_ context.Context, path string, _ error) error {
	quarantine.paths = append(quarantine.paths, path)
	return nil
}

func TestFileScannerValidTornWrongKeyAndQuarantine(t *testing.T) {
	factory, _ := newTestFactory()
	segment, err := factory.Create(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	base := hex.EncodeToString(segment.Manifest.SegmentID[:])
	manifestData, _ := json.Marshal(segment.Manifest)
	if err := os.WriteFile(filepath.Join(directory, base+".manifest"), manifestData, 0o600); err != nil {
		t.Fatal(err)
	}
	var reservationID, uuid [16]byte
	reservationID[0], uuid[0] = 1, 1
	event := ServiceEvent{Kind: RecordStatement, Outcome: OutcomeUnknown, TransactionID: "tx", TransactionSeq: 1, EventUUID: uuid, SchemaID: "agentsql.audit.event.v4", SchemaVersion: 4, CanonicalEvent: []byte("event")}
	record := Record{SegmentID: segment.Manifest.SegmentID, ReservationID: reservationID, EventUUID: uuid, EventSchemaID: event.SchemaID, EventSchemaVersion: event.SchemaVersion, KeyID: segment.Manifest.KeyID, CanonicalEvent: event.CanonicalEvent, Extensions: serviceExtensions(event)}
	copy(record.Nonce[:4], segment.Manifest.NonceDomain[:])
	extent, err := EncodeRecord(segment.Key[:], record)
	if err != nil {
		t.Fatal(err)
	}
	walPath := filepath.Join(directory, base+".wal")
	if err := os.WriteFile(walPath, extent, 0o600); err != nil {
		t.Fatal(err)
	}
	quarantine := &captureQuarantine{}
	scanner := FileScanner{Directory: directory, Unwrapper: fixedUnwrapper{key: segment.Key}, Quarantine: quarantine}
	result, err := scanner.Scan(context.Background())
	if err != nil || len(result.Records) != 1 || len(result.Quarantined) != 0 {
		t.Fatalf("valid scan = %+v err=%v", result, err)
	}

	if err := os.WriteFile(walPath, append(extent, 1), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err = scanner.Scan(context.Background())
	if err != nil || len(result.Quarantined) != 1 || len(quarantine.paths) != 1 {
		t.Fatalf("torn scan = %+v paths=%v err=%v", result, quarantine.paths, err)
	}

	if err := os.WriteFile(walPath, extent, 0o600); err != nil {
		t.Fatal(err)
	}
	wrong := segment.Key
	wrong[0] ^= 1
	quarantine.paths = nil
	scanner.Unwrapper = fixedUnwrapper{key: wrong}
	result, err = scanner.Scan(context.Background())
	if err != nil || len(result.Quarantined) != 1 || len(quarantine.paths) != 1 {
		t.Fatalf("wrong-key scan = %+v paths=%v err=%v", result, quarantine.paths, err)
	}

	scanner.Unwrapper = fixedUnwrapper{err: errors.New("KMS offline")}
	if _, err := scanner.Scan(context.Background()); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("unavailable key = %v", err)
	}
}
