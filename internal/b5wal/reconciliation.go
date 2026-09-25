package b5wal

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
)

type ReceiptFinder interface {
	FindByEventUUID(context.Context, [16]byte) ([]ResultReceipt, error)
}

type PrimarySnapshot interface {
	Snapshot(context.Context, int) ([]PrimaryEvent, error)
}

type RecoveredRecord struct {
	Decoded  DecodedRecord
	Identity AppendIdentity
	Event    PrimaryEvent
	Source   string
}

type RecoveryClassification string

const (
	RecoveryHistoricalUnknown        RecoveryClassification = "HISTORICAL_RESPONSE_UNKNOWN"
	RecoveryCommittedAuditPending    RecoveryClassification = "COMMITTED_AUDIT_PENDING"
	RecoveryNotCommittedAuditPending RecoveryClassification = "NOT_COMMITTED_AUDIT_PENDING"
	RecoveryDBOutcomeUnknown         RecoveryClassification = "UNKNOWN"
	RecoveryPrimaryDurable           RecoveryClassification = "PRIMARY_DURABLE"
)

type RecoveryLedgerEntry struct {
	EventUUID          [16]byte
	TransactionID      string
	TransactionSeq     uint64
	Classification     RecoveryClassification
	HistoricalResponse HistoricalResponseState
	ReportedDurability ReportedDurability
	AppendConfirmation AppendConfirmationState
	Reconciliation     ReconciliationState
	Replayed           bool
}

// Reconciler replays verified records in double-chain order and durably joins
// them to the result-receipt plane. The returned ledger is an operational view;
// receipt updates and primary rows are the durable recovery ledger.
type Reconciler struct {
	Primary  PrimaryStore
	Receipts ReceiptStore
	Finder   ReceiptFinder
}

func (reconciler Reconciler) Reconcile(ctx context.Context, records []RecoveredRecord) ([]RecoveryLedgerEntry, error) {
	if reconciler.Primary == nil || reconciler.Receipts == nil || reconciler.Finder == nil {
		return nil, errors.New("b5wal: incomplete reconciler")
	}
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].Event.TransactionID == records[j].Event.TransactionID {
			return records[i].Event.TransactionSeq < records[j].Event.TransactionSeq
		}
		return records[i].Event.TransactionID < records[j].Event.TransactionID
	})
	verifier := NewReplayVerifier()
	if snapshot, ok := reconciler.Primary.(PrimarySnapshot); ok {
		primaryEvents, err := snapshot.Snapshot(ctx, 100000)
		if err != nil {
			return nil, err
		}
		for _, event := range primaryEvents {
			if _, _, err := verifier.Ingest(event.ReplayEvent); err != nil {
				return nil, fmt.Errorf("b5wal: primary double-chain verification: %w", err)
			}
		}
	}
	ledger := make([]RecoveryLedgerEntry, 0, len(records))
	for _, recovered := range records {
		if _, _, err := verifier.Ingest(recovered.Event.ReplayEvent); err != nil {
			return ledger, err
		}
		receipts, err := reconciler.Finder.FindByEventUUID(ctx, recovered.Event.EventUUID)
		if err != nil {
			return ledger, err
		}
		entry := RecoveryLedgerEntry{EventUUID: recovered.Event.EventUUID, TransactionID: recovered.Event.TransactionID, TransactionSeq: recovered.Event.TransactionSeq}
		if len(receipts) == 0 {
			entry.HistoricalResponse = HistoricalResponseUnknown
			entry.Classification = RecoveryHistoricalUnknown
		}
		expectedAppendDigest := WALAppendReceiptDigest(recovered.Identity)
		for _, receipt := range receipts {
			if receipt.BusinessEventDigest != recovered.Event.PayloadDigest || receipt.WALAppendReceiptDigest != expectedAppendDigest {
				return ledger, ErrReceiptConflict
			}
		}
		_, err = reconciler.Primary.Put(ctx, recovered.Event)
		if err != nil {
			return ledger, err
		}
		entry.Replayed = true
		for _, receipt := range receipts {
			view, joinErr := RestartJoin(ctx, reconciler.Receipts, receipt.Key, true, true)
			if joinErr != nil {
				return ledger, joinErr
			}
			entry.HistoricalResponse = view.HistoricalResponse
			entry.ReportedDurability = view.Receipt.ReportedDurabilityAtResponse
			entry.AppendConfirmation = view.Receipt.AppendConfirmation
			entry.Reconciliation = view.Receipt.Reconciliation
		}
		if len(receipts) > 0 {
			switch recovered.Event.Outcome {
			case OutcomeCommitted:
				if entry.ReportedDurability == DurabilityDurable {
					entry.Classification = RecoveryPrimaryDurable
				} else {
					entry.Classification = RecoveryCommittedAuditPending
				}
			case OutcomeNotCommitted:
				entry.Classification = RecoveryNotCommittedAuditPending
			case OutcomeUnknown:
				entry.Classification = RecoveryDBOutcomeUnknown
			default:
				return ledger, fmt.Errorf("b5wal: unknown DB outcome %q", recovered.Event.Outcome)
			}
		}
		ledger = append(ledger, entry)
	}
	if err := VerifyGlobalChain(verifier.GlobalChain()); err != nil {
		return ledger, err
	}
	return ledger, nil
}

func recoveredRecord(decoded DecodedRecord, extentDigest [32]byte, source string) (RecoveredRecord, error) {
	values := make(map[uint16][]byte, len(decoded.Extensions))
	for _, extension := range decoded.Extensions {
		values[extension.Tag] = extension.Value
	}
	transactionID := string(values[extensionTransactionID])
	sequenceBytes := values[extensionTransactionSeq]
	previous := values[extensionPreviousDigest]
	if transactionID == "" || len(sequenceBytes) != 8 || len(previous) != 32 {
		return RecoveredRecord{}, errors.New("b5wal: recovery metadata missing")
	}
	var previousDigest [32]byte
	copy(previousDigest[:], previous)
	outcome := DBOutcome(values[extensionDBOutcome])
	kind := RecordKind(values[extensionRecordKind])
	event := PrimaryEvent{
		ReplayEvent: ReplayEvent{EventUUID: decoded.EventUUID, PayloadDigest: decoded.EventPayloadDigest, TransactionID: transactionID, TransactionSeq: binary.BigEndian.Uint64(sequenceBytes), PreviousTxDigest: previousDigest},
		Kind:        kind, Outcome: outcome, SchemaID: decoded.EventSchemaID, SchemaVersion: decoded.EventSchemaVersion,
		CanonicalEvent: append([]byte(nil), decoded.CanonicalEvent...),
	}
	return RecoveredRecord{
		Decoded: decoded, Event: event, Source: source,
		Identity: AppendIdentity{SegmentID: decoded.SegmentID, RecordOrdinal: decoded.RecordOrdinal, Nonce: decoded.Nonce, EventUUID: decoded.EventUUID, EventDigest: decoded.EventPayloadDigest, ExtentDigest: extentDigest},
	}, nil
}
