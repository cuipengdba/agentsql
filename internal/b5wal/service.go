package b5wal

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"
)

const DefaultFsyncTimeout = 250 * time.Millisecond

type DBOutcome string

const (
	OutcomeNotCommitted DBOutcome = "NOT_COMMITTED"
	OutcomeCommitted    DBOutcome = "COMMITTED"
	OutcomeUnknown      DBOutcome = "UNKNOWN"
)

// PrimaryEvent is the already-canonical fact presented to the durable primary
// audit plane. Put must be idempotent for equal UUID+PayloadDigest and fail
// closed with ErrEventUUIDConflict for an unequal digest.
type PrimaryEvent struct {
	ReplayEvent
	Kind           RecordKind
	Outcome        DBOutcome
	SchemaID       string
	SchemaVersion  uint16
	CanonicalEvent []byte
}

type PrimaryStore interface {
	Put(context.Context, PrimaryEvent) (alreadyPresent bool, err error)
}

type ServiceEvent struct {
	Kind              RecordKind
	Outcome           DBOutcome
	TransactionID     string
	TransactionSeq    uint64
	PreviousTxDigest  [32]byte
	EventUUID         [16]byte
	SchemaID          string
	SchemaVersion     uint16
	CanonicalEvent    []byte
	SessionID         string
	RequestID         string
	AttemptGeneration uint64
}

type IngestResult struct {
	Durability ReportedDurability
	Append     AppendResult
	Primary    bool
}

// EmissionReservation combines the executable emission path with the physical
// 120KiB reservation. Facts must arrive once and in frozen table order.
type EmissionReservation struct {
	mu       sync.Mutex
	id       [16]byte
	path     EmissionPath
	next     int
	capacity *Reservation
	pending  *pendingEmission
}

type pendingEmission struct {
	kind          RecordKind
	eventUUID     [16]byte
	payloadDigest [32]byte
	result        IngestResult
	receipt       ResultReceipt
	late          <-chan error
	lateStarted   bool
}

func NewEmissionReservation(id [16]byte, state LastSuccessState, transition Transition) (*EmissionReservation, error) {
	if id == ([16]byte{}) {
		return nil, errors.New("b5wal: zero reservation id")
	}
	for _, path := range EmissionPaths() {
		if path.State == state && path.Transition == transition {
			capacity, err := NewReservation(MaxPathRecordCount)
			if err != nil {
				return nil, err
			}
			return &EmissionReservation{id: id, path: path, capacity: capacity}, nil
		}
	}
	return nil, errors.New("b5wal: emission path is not frozen")
}

func (reservation *EmissionReservation) begin(kind RecordKind, eventUUID [16]byte, payloadDigest [32]byte) (*pendingEmission, error) {
	reservation.mu.Lock()
	defer reservation.mu.Unlock()
	if reservation.pending != nil {
		if reservation.pending.kind != kind || reservation.pending.eventUUID != eventUUID || reservation.pending.payloadDigest != payloadDigest {
			return nil, errors.New("b5wal: pending emission retry conflict")
		}
		copyOfPending := *reservation.pending
		return &copyOfPending, nil
	}
	if reservation.next >= len(reservation.path.Records) || reservation.path.Records[reservation.next] != kind {
		return nil, fmt.Errorf("b5wal: emission out of order: next=%d kind=%s", reservation.next, kind)
	}
	return nil, nil
}

func (reservation *EmissionReservation) stage(pending pendingEmission) {
	reservation.mu.Lock()
	reservation.pending = &pending
	reservation.mu.Unlock()
}

func (reservation *EmissionReservation) startLate() (<-chan error, bool) {
	reservation.mu.Lock()
	defer reservation.mu.Unlock()
	if reservation.pending == nil || reservation.pending.late == nil || reservation.pending.lateStarted {
		return nil, false
	}
	reservation.pending.lateStarted = true
	return reservation.pending.late, true
}

func (reservation *EmissionReservation) complete() {
	reservation.mu.Lock()
	reservation.next++
	reservation.pending = nil
	reservation.mu.Unlock()
}

func (reservation *EmissionReservation) RemainingBytes() int { return reservation.capacity.Remaining() }
func (reservation *EmissionReservation) Emitted() int {
	reservation.mu.Lock()
	defer reservation.mu.Unlock()
	return reservation.next
}

// Service is the flag-off production boundary intended for S6. It owns
// primary-first ingestion, emergency staging, the response receipt fence, and
// one post-wedge rotation. It has no dependency on MCP or HTTP packages.
type Service struct {
	Primary     PrimaryStore
	Receipts    ReceiptStore
	WAL         *Manager
	SyncTimeout time.Duration
}

func (service *Service) IngestAndRespond(ctx context.Context, reservation *EmissionReservation, event ServiceEvent, send func(ResultReceipt) error) (IngestResult, error) {
	var result IngestResult
	if service == nil || service.Primary == nil || service.Receipts == nil || service.WAL == nil || reservation == nil || send == nil {
		return result, errors.New("b5wal: incomplete WAL service")
	}
	payloadDigest := sha256.Sum256(event.CanonicalEvent)
	pending, err := reservation.begin(event.Kind, event.EventUUID, payloadDigest)
	if err != nil {
		return result, err
	}
	if pending != nil {
		err := PersistThenSend(ctx, service.Receipts, pending.receipt, send)
		if late, start := reservation.startLate(); start {
			go service.observeLate(pending.receipt.Key, late)
		}
		if err != nil {
			return pending.result, err
		}
		reservation.complete()
		return pending.result, nil
	}
	primary := PrimaryEvent{
		ReplayEvent: ReplayEvent{EventUUID: event.EventUUID, PayloadDigest: payloadDigest, TransactionID: event.TransactionID, TransactionSeq: event.TransactionSeq, PreviousTxDigest: event.PreviousTxDigest},
		Kind:        event.Kind, Outcome: event.Outcome, SchemaID: event.SchemaID, SchemaVersion: event.SchemaVersion,
		CanonicalEvent: append([]byte(nil), event.CanonicalEvent...),
	}
	_, primaryErr := service.Primary.Put(ctx, primary)
	if errors.Is(primaryErr, ErrEventUUIDConflict) || errors.Is(primaryErr, ErrTransactionChain) {
		return result, primaryErr
	}
	var appendDigest [32]byte
	var late <-chan error
	appendState := AppendUnknown
	if primaryErr == nil {
		result.Primary = true
		result.Durability = DurabilityDurable
		appendDigest = primaryReceiptDigest(event.EventUUID, payloadDigest)
	} else {
		record := Record{
			ReservationID: reservation.id, EventUUID: event.EventUUID, EventSchemaID: event.SchemaID,
			EventSchemaVersion: event.SchemaVersion, CanonicalEvent: append([]byte(nil), event.CanonicalEvent...),
			Extensions: serviceExtensions(event),
		}
		timeout := service.SyncTimeout
		if timeout <= 0 {
			timeout = DefaultFsyncTimeout
		}
		syncContext, cancel := context.WithTimeout(ctx, timeout)
		result.Append, primaryErr = service.WAL.Current().AppendReserved(syncContext, reservation.capacity, record)
		cancel()
		appendDigest = WALAppendReceiptDigest(result.Append.Identity)
		if primaryErr == nil {
			result.Durability = DurabilityAuditPending
		} else {
			result.Durability = DurabilityLost
			appendState = AppendTimeout
			late = result.Append.Late
			// A write error, disk-full response, timeout, or uncertain device
			// acknowledgement wedges the segment. Rotation is attempted once and
			// never retries the failed fact.
			if service.WAL.Current().State() == WriterWedgedUnconfirmed {
				_ = service.WAL.RotateAfterWedge(ctx, reservation.id)
			}
		}
	}
	if appendDigest == ([32]byte{}) {
		// Encode/key failures happen before an append identity exists. Bind the
		// LOST receipt to a deterministic failure identity, not a zero digest.
		appendDigest = failedAppendReceiptDigest(event.EventUUID, payloadDigest)
	}
	receipt := ResultReceipt{
		Key:                 ReceiptKey{SessionID: event.SessionID, RequestID: event.RequestID, EventUUID: event.EventUUID, AttemptGeneration: event.AttemptGeneration},
		BusinessEventDigest: payloadDigest, WALAppendReceiptDigest: appendDigest,
		ReportedDurabilityAtResponse: result.Durability, AppendConfirmation: appendState,
		Reconciliation: ReconciliationNone, Delivery: ResponsePrepared,
	}
	reservation.stage(pendingEmission{kind: event.Kind, eventUUID: event.EventUUID, payloadDigest: payloadDigest, result: result, receipt: receipt, late: late})
	err = PersistThenSend(ctx, service.Receipts, receipt, send)
	if lateResult, start := reservation.startLate(); start {
		go service.observeLate(receipt.Key, lateResult)
	}
	if err != nil {
		return result, err
	}
	reservation.complete()
	return result, nil
}

func (service *Service) observeLate(key ReceiptKey, late <-chan error) {
	if err := <-late; err != nil {
		return
	}
	confirmed := AppendLateConfirmed
	_, _ = service.Receipts.Advance(context.Background(), key, ReceiptAdvance{Append: &confirmed})
}

func primaryReceiptDigest(uuid [16]byte, payload [32]byte) [32]byte {
	return sha256.Sum256(append(append([]byte("agentsql.b5.primary-receipt.v1"), uuid[:]...), payload[:]...))
}

func failedAppendReceiptDigest(uuid [16]byte, payload [32]byte) [32]byte {
	return sha256.Sum256(append(append([]byte("agentsql.b5.failed-append.v1"), uuid[:]...), payload[:]...))
}

const (
	extensionTransactionID  uint16 = 1
	extensionTransactionSeq uint16 = 2
	extensionPreviousDigest uint16 = 3
	extensionRecordKind     uint16 = 4
	extensionDBOutcome      uint16 = 5
)

func serviceExtensions(event ServiceEvent) []Extension {
	var sequence [8]byte
	binary.BigEndian.PutUint64(sequence[:], event.TransactionSeq)
	return []Extension{
		{Tag: extensionTransactionID, Value: []byte(event.TransactionID)},
		{Tag: extensionTransactionSeq, Value: sequence[:]},
		{Tag: extensionPreviousDigest, Value: event.PreviousTxDigest[:]},
		{Tag: extensionRecordKind, Value: []byte(event.Kind)},
		{Tag: extensionDBOutcome, Value: []byte(event.Outcome)},
	}
}
