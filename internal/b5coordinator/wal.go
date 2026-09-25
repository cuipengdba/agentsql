package b5coordinator

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"sync"

	"github.com/cuipengdba/agentsql/internal/b5"
	"github.com/cuipengdba/agentsql/internal/b5wal"
)

// WALAuditor adapts the S7b primary/WAL/receipt service to coordinator
// barriers. Pre-effect tx_begin is primary-only and fails closed. Post-effect
// facts use Service so a primary outage is represented by an immutable WAL
// append receipt and the returned durability axis.
type WALAuditor struct {
	Service  *b5wal.Service
	mu       sync.Mutex
	previous map[string][32]byte
}

func NewWALAuditor(service *b5wal.Service) (*WALAuditor, error) {
	if service == nil || service.Primary == nil || service.Receipts == nil || service.WAL == nil {
		return nil, errors.New("b5coordinator: incomplete WAL audit service")
	}
	return &WALAuditor{Service: service, previous: make(map[string][32]byte)}, nil
}

type canonicalCoordinatorEvent struct {
	Schema           string       `json:"schema"`
	Version          uint16       `json:"version"`
	EventUUID        [16]byte     `json:"event_uuid"`
	Kind             AuditKind    `json:"kind"`
	TransactionID    string       `json:"transaction_id"`
	SessionID        string       `json:"session_id"`
	RequestID        string       `json:"request_id"`
	Sequence         uint64       `json:"transaction_seq"`
	PlanDigest       [32]byte     `json:"plan_digest"`
	StatementOrdinal int          `json:"statement_ordinal"`
	Action           b5.DMLAction `json:"action"`
	AffectedRows     int64        `json:"affected_rows"`
	DBOutcome        b5.DBOutcome `json:"db_outcome"`
	ErrorCode        b5.ErrorCode `json:"error_code"`
	EvidenceDigest   [32]byte     `json:"evidence_digest"`
}

func (audit *WALAuditor) Barrier(ctx context.Context, event AuditEvent) (AuditResult, error) {
	if ctx == nil || event.TransactionID == "" || event.Sequence == 0 {
		return AuditResult{}, errors.New("b5coordinator: invalid audit event")
	}
	var eventUUID [16]byte
	if _, err := rand.Read(eventUUID[:]); err != nil {
		return AuditResult{}, err
	}
	canonical, err := json.Marshal(canonicalCoordinatorEvent{Schema: b5.EventSchemaID, Version: b5.EventSchemaVersion, EventUUID: eventUUID, Kind: event.Kind, TransactionID: event.TransactionID, SessionID: event.SessionID, RequestID: event.RequestID, Sequence: event.Sequence, PlanDigest: event.PlanDigest, StatementOrdinal: event.StatementOrdinal, Action: event.Action, AffectedRows: event.AffectedRows, DBOutcome: event.DBOutcome, ErrorCode: event.ErrorCode, EvidenceDigest: event.EvidenceDigest})
	if err != nil {
		return AuditResult{}, err
	}
	digest := sha256.Sum256(canonical)
	audit.mu.Lock()
	previous := audit.previous[event.TransactionID]
	audit.mu.Unlock()
	serviceEvent := b5wal.ServiceEvent{Kind: recordKind(event.Kind), Outcome: walOutcome(event.DBOutcome), TransactionID: event.TransactionID, TransactionSeq: event.Sequence, PreviousTxDigest: previous, EventUUID: eventUUID, SchemaID: b5.EventSchemaID, SchemaVersion: b5.EventSchemaVersion, CanonicalEvent: canonical, SessionID: event.SessionID, RequestID: event.RequestID, AttemptGeneration: event.Sequence}
	if event.Kind == AuditTxBegin {
		_, putErr := audit.Service.Primary.Put(ctx, b5wal.PrimaryEvent{ReplayEvent: b5wal.ReplayEvent{EventUUID: eventUUID, PayloadDigest: digest, TransactionID: event.TransactionID, TransactionSeq: event.Sequence, PreviousTxDigest: previous}, Kind: b5wal.RecordKind(event.Kind), Outcome: walOutcome(event.DBOutcome), SchemaID: b5.EventSchemaID, SchemaVersion: b5.EventSchemaVersion, CanonicalEvent: canonical})
		if putErr != nil {
			return AuditResult{Durability: b5.DurabilityLost, EventDigest: digest}, putErr
		}
		audit.remember(event.TransactionID, digest)
		return AuditResult{Durability: b5.DurabilityDurable, EventDigest: digest}, nil
	}
	state, transition := emissionPath(event)
	var reservationID [16]byte
	if _, err := rand.Read(reservationID[:]); err != nil {
		return AuditResult{}, err
	}
	reservation, err := b5wal.NewEmissionReservation(reservationID, state, transition)
	if err != nil {
		return AuditResult{}, err
	}
	result, ingestErr := audit.Service.IngestAndRespond(ctx, reservation, serviceEvent, func(b5wal.ResultReceipt) error { return nil })
	if ingestErr == nil {
		audit.remember(event.TransactionID, digest)
	}
	return AuditResult{Durability: result.Durability, EventDigest: digest}, ingestErr
}
func (audit *WALAuditor) remember(transaction string, digest [32]byte) {
	audit.mu.Lock()
	audit.previous[transaction] = digest
	audit.mu.Unlock()
}
func recordKind(kind AuditKind) b5wal.RecordKind {
	switch kind {
	case AuditStatement:
		return b5wal.RecordStatement
	case AuditCommitIntent:
		return b5wal.RecordCommitIntent
	case AuditRollback:
		return b5wal.RecordRollback
	case AuditTerminal:
		return b5wal.RecordTerminalOutcome
	default:
		return b5wal.RecordKind(kind)
	}
}
func walOutcome(outcome b5.DBOutcome) b5wal.DBOutcome {
	switch outcome {
	case b5.OutcomeCommitted:
		return b5wal.OutcomeCommitted
	case b5.OutcomeNotCommitted:
		return b5wal.OutcomeNotCommitted
	default:
		return b5wal.OutcomeUnknown
	}
}
func emissionPath(event AuditEvent) (b5wal.LastSuccessState, b5wal.Transition) {
	switch event.Kind {
	case AuditStatement:
		return b5wal.LastAfterStatementEffect, b5wal.TransitionFsyncTimeout
	case AuditCommitIntent:
		return b5wal.LastBeforeCommitIntent, b5wal.TransitionFsyncTimeout
	case AuditRollback:
		return b5wal.LastRollbackOnly, b5wal.TransitionPrimaryFailure
	case AuditTerminal:
		if event.DBOutcome == b5.OutcomeCommitted {
			return b5wal.LastPositiveCommitACK, b5wal.TransitionPrimaryFailure
		}
		return b5wal.LastUnknownCommitOutcome, b5wal.TransitionPrimaryFailure
	default:
		return b5wal.LastUnknownCommitOutcome, b5wal.TransitionPrimaryFailure
	}
}

var _ Auditor = (*WALAuditor)(nil)
