package b5wal

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"

	"github.com/cuipengdba/agentsql/internal/store"
)

// PGPrimaryStore writes canonical v4 facts into the frozen b5_tx_events table.
// It is named for production use but is dialect-neutral for SQLite tests.
type PGPrimaryStore struct{ repository store.B5TxEventStore }

var _ PrimaryStore = (*PGPrimaryStore)(nil)

func NewPGPrimaryStore(repository store.B5TxEventStore) (*PGPrimaryStore, error) {
	if repository == nil {
		return nil, errors.New("b5wal: nil primary event repository")
	}
	return &PGPrimaryStore{repository: repository}, nil
}

// Snapshot supplies the durable primary side of restart reconstruction before
// WAL-only records are joined. Canonical payloads are returned for digest and
// UUID verification; DBOutcome is intentionally not inferred from bytes.
func (primary *PGPrimaryStore) Snapshot(ctx context.Context, limit int) ([]PrimaryEvent, error) {
	rows, err := primary.repository.List(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("b5wal: list primary events: %w", err)
	}
	values := make([]PrimaryEvent, len(rows))
	for index, row := range rows {
		var uuid [16]byte
		var payloadDigest, previous [32]byte
		copy(uuid[:], row.EventUUID)
		copy(payloadDigest[:], row.EventDigest)
		copy(previous[:], row.PreviousTxEventDigest)
		values[index] = PrimaryEvent{
			ReplayEvent: ReplayEvent{EventUUID: uuid, PayloadDigest: payloadDigest, TransactionID: row.TransactionID, TransactionSeq: row.TransactionSeq, PreviousTxDigest: previous},
			Kind:        RecordKind(row.EventType), SchemaID: row.EventSchemaID, SchemaVersion: row.EventSchemaVersion,
			CanonicalEvent: append([]byte(nil), row.CanonicalEvent...),
		}
	}
	return values, nil
}

func (primary *PGPrimaryStore) Put(ctx context.Context, event PrimaryEvent) (bool, error) {
	if event.EventUUID == ([16]byte{}) || event.PayloadDigest == ([32]byte{}) || event.TransactionID == "" || event.TransactionSeq == 0 {
		return false, errors.New("b5wal: invalid primary event")
	}
	if event.TransactionSeq == 1 {
		if event.PreviousTxDigest != ([32]byte{}) {
			return false, ErrTransactionChain
		}
	} else {
		previous, err := primary.repository.Get(ctx, event.TransactionID, event.TransactionSeq-1)
		if err != nil {
			return false, fmt.Errorf("%w: predecessor: %v", ErrTransactionChain, err)
		}
		if len(previous.EventDigest) != 32 || subtle.ConstantTimeCompare(previous.EventDigest, event.PreviousTxDigest[:]) != 1 {
			return false, ErrTransactionChain
		}
	}
	value := store.B5TxEvent{
		TransactionID: event.TransactionID, TransactionSeq: event.TransactionSeq, EventUUID: event.EventUUID[:],
		EventType: string(event.Kind), EventSchemaID: event.SchemaID, EventSchemaVersion: event.SchemaVersion,
		PreviousTxEventDigest: event.PreviousTxDigest[:], EventDigest: event.PayloadDigest[:], CanonicalEvent: event.CanonicalEvent,
	}
	if event.TransactionSeq == 1 {
		value.PreviousTxEventDigest = nil
	}
	if err := primary.repository.Append(ctx, value); err == nil {
		return false, nil
	} else {
		existing, getErr := primary.repository.GetByUUID(ctx, event.EventUUID[:])
		if getErr != nil {
			return false, fmt.Errorf("b5wal: primary append: %w", err)
		}
		if len(existing.EventDigest) != 32 || subtle.ConstantTimeCompare(existing.EventDigest, event.PayloadDigest[:]) != 1 {
			return false, ErrEventUUIDConflict
		}
		return true, nil
	}
}
