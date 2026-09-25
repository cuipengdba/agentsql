package b5wal

import (
	"context"
	"errors"
	"fmt"

	"github.com/cuipengdba/agentsql/internal/store"
)

// PGReceiptStore adapts the frozen b5_result_receipts repository to the WAL
// receipt contract. Despite the name it deliberately works with the SQLite
// repository too, so the exact persistence semantics can be tested cheaply.
// Production construction supplies Store.B5ResultReceipts(), backed by PG.
type PGReceiptStore struct {
	repository store.B5ResultReceiptStore
}

var _ ReceiptStore = (*PGReceiptStore)(nil)

func NewPGReceiptStore(repository store.B5ResultReceiptStore) (*PGReceiptStore, error) {
	if repository == nil {
		return nil, errors.New("b5wal: nil durable receipt repository")
	}
	return &PGReceiptStore{repository: repository}, nil
}

func (adapter *PGReceiptStore) PutWriteOnce(ctx context.Context, receipt ResultReceipt) (ResultReceipt, bool, error) {
	receipt = normalizeReceiptSchema(receipt)
	if err := validateReceipt(receipt); err != nil {
		return ResultReceipt{}, false, err
	}
	stored, inserted, err := adapter.repository.PutWriteOnce(ctx, toStoreReceipt(receipt))
	if errors.Is(err, store.ErrB5ReceiptConflict) {
		return ResultReceipt{}, false, ErrReceiptConflict
	}
	if err != nil {
		return ResultReceipt{}, false, fmt.Errorf("b5wal: persist result receipt: %w", err)
	}
	return fromStoreReceipt(stored), inserted, nil
}

func (adapter *PGReceiptStore) Get(ctx context.Context, key ReceiptKey) (ResultReceipt, bool, error) {
	stored, err := adapter.repository.Get(ctx, toStoreReceiptKey(key))
	if errors.Is(err, store.ErrNotFound) {
		return ResultReceipt{}, false, nil
	}
	if err != nil {
		return ResultReceipt{}, false, fmt.Errorf("b5wal: read result receipt: %w", err)
	}
	return fromStoreReceipt(stored), true, nil
}

// FindByEventUUID is used only by recovery. An empty result means the scanner
// must report HistoricalResponseUnknown and must not invent AUDIT_PENDING.
func (adapter *PGReceiptStore) FindByEventUUID(ctx context.Context, eventUUID [16]byte) ([]ResultReceipt, error) {
	rows, err := adapter.repository.ListByEventUUID(ctx, eventUUID[:])
	if err != nil {
		return nil, fmt.Errorf("b5wal: scan result receipts: %w", err)
	}
	result := make([]ResultReceipt, len(rows))
	for index := range rows {
		result[index] = fromStoreReceipt(rows[index])
	}
	return result, nil
}

func (adapter *PGReceiptStore) Advance(ctx context.Context, key ReceiptKey, update ReceiptAdvance) (ResultReceipt, error) {
	for attempt := 0; attempt < 8; attempt++ {
		current, err := adapter.repository.Get(ctx, toStoreReceiptKey(key))
		if errors.Is(err, store.ErrNotFound) {
			return ResultReceipt{}, ErrReceiptNotFound
		}
		if err != nil {
			return ResultReceipt{}, fmt.Errorf("b5wal: read receipt for advance: %w", err)
		}
		advanced, err := adapter.repository.Advance(ctx, current.Key, current.Revision, store.B5ReceiptAdvance{
			AppendConfirmation: update.Append,
			Reconciliation:     update.Reconciliation,
			DeliveryStatus:     update.Delivery,
		})
		if err == nil {
			return fromStoreReceipt(advanced), nil
		}
		if !errors.Is(err, store.ErrB5CASConflict) && !errors.Is(err, store.ErrB5InvalidTransition) {
			return ResultReceipt{}, fmt.Errorf("b5wal: advance receipt: %w", err)
		}
		latest, getErr := adapter.repository.Get(ctx, current.Key)
		if getErr == nil && receiptUpdateSatisfied(latest, update) {
			return fromStoreReceipt(latest), nil
		}
		if errors.Is(err, store.ErrB5InvalidTransition) && getErr == nil && latest.Revision == current.Revision {
			return ResultReceipt{}, ErrReceiptTransition
		}
	}
	return ResultReceipt{}, fmt.Errorf("b5wal: advance receipt: %w", store.ErrB5CASConflict)
}

func receiptUpdateSatisfied(value store.B5ResultReceipt, update ReceiptAdvance) bool {
	return (update.Append == nil || value.AppendConfirmation == *update.Append) &&
		(update.Reconciliation == nil || value.Reconciliation == *update.Reconciliation) &&
		(update.Delivery == nil || value.DeliveryStatus == *update.Delivery)
}

func toStoreReceiptKey(key ReceiptKey) store.B5ReceiptKey {
	return store.B5ReceiptKey{SessionID: key.SessionID, RequestID: key.RequestID, EventUUID: append([]byte(nil), key.EventUUID[:]...), AttemptGeneration: key.AttemptGeneration}
}

func toStoreReceipt(value ResultReceipt) store.B5ResultReceipt {
	return store.B5ResultReceipt{
		Key: toStoreReceiptKey(value.Key), SchemaID: value.SchemaID, SchemaVersion: value.SchemaVersion,
		BusinessEventDigest: append([]byte(nil), value.BusinessEventDigest[:]...), WALAppendReceiptDigest: append([]byte(nil), value.WALAppendReceiptDigest[:]...),
		ReportedDurability: value.ReportedDurabilityAtResponse, AppendConfirmation: value.AppendConfirmation,
		Reconciliation: value.Reconciliation, DeliveryStatus: value.Delivery,
	}
}

func fromStoreReceipt(value store.B5ResultReceipt) ResultReceipt {
	result := ResultReceipt{
		SchemaID: value.SchemaID, SchemaVersion: value.SchemaVersion,
		Key:                          ReceiptKey{SessionID: value.Key.SessionID, RequestID: value.Key.RequestID, AttemptGeneration: value.Key.AttemptGeneration},
		ReportedDurabilityAtResponse: value.ReportedDurability, AppendConfirmation: value.AppendConfirmation,
		Reconciliation: value.Reconciliation, Delivery: value.DeliveryStatus,
	}
	copy(result.Key.EventUUID[:], value.Key.EventUUID)
	copy(result.BusinessEventDigest[:], value.BusinessEventDigest)
	copy(result.WALAppendReceiptDigest[:], value.WALAppendReceiptDigest)
	return result
}
