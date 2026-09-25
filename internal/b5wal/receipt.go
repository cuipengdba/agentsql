package b5wal

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
)

type ReportedDurability uint8

const (
	DurabilityUnspecified ReportedDurability = iota
	DurabilityDurable
	DurabilityAuditPending
	DurabilityLost
)

type AppendConfirmationState uint8

const (
	AppendUnknown AppendConfirmationState = iota
	AppendTimeout
	AppendLateConfirmed
	AppendRecovered
)

type ReconciliationState uint8

const (
	ReconciliationNone ReconciliationState = iota
	ReconciliationReplayStaged
	ReconciliationPrimaryDurable
)

type ResponseDeliveryState uint8

const (
	ResponsePrepared ResponseDeliveryState = iota
	ResponseSendStarted
	ResponseSendCompleted
)

type ReceiptKey struct {
	SessionID         string
	RequestID         string
	EventUUID         [16]byte
	AttemptGeneration uint64
}

// ResultReceipt is write-once for Key, both digests, and
// ReportedDurabilityAtResponse. Mutable state advances only through the
// transition functions below. Persisting Prepared before sending makes the
// exact terminal result reproducible even if the process dies before or during
// network delivery; SendStarted cannot prove that a peer received the bytes.
type ResultReceipt struct {
	Key                          ReceiptKey
	BusinessEventDigest          [32]byte
	WALAppendReceiptDigest       [32]byte
	ReportedDurabilityAtResponse ReportedDurability
	AppendConfirmation           AppendConfirmationState
	Reconciliation               ReconciliationState
	Delivery                     ResponseDeliveryState
}

type ReceiptStore interface {
	PutWriteOnce(ctx context.Context, receipt ResultReceipt) (ResultReceipt, bool, error)
	Get(ctx context.Context, key ReceiptKey) (ResultReceipt, bool, error)
	Advance(ctx context.Context, key ReceiptKey, update ReceiptAdvance) (ResultReceipt, error)
}

type ReceiptAdvance struct {
	Append         *AppendConfirmationState
	Reconciliation *ReconciliationState
	Delivery       *ResponseDeliveryState
}

var (
	ErrReceiptConflict   = errors.New("b5wal: result receipt immutable conflict")
	ErrReceiptNotFound   = errors.New("b5wal: result receipt not found")
	ErrReceiptTransition = errors.New("b5wal: invalid result receipt transition")
)

// WALAppendReceiptDigest binds the business result receipt to the exact local
// append identity. It is separate from the business event digest.
func WALAppendReceiptDigest(identity AppendIdentity) [32]byte {
	encoded := make([]byte, 0, 16+8+12+16+32+32)
	encoded = append(encoded, identity.SegmentID[:]...)
	var ordinal [8]byte
	binary.BigEndian.PutUint64(ordinal[:], identity.RecordOrdinal)
	encoded = append(encoded, ordinal[:]...)
	encoded = append(encoded, identity.Nonce[:]...)
	encoded = append(encoded, identity.EventUUID[:]...)
	encoded = append(encoded, identity.EventDigest[:]...)
	encoded = append(encoded, identity.ExtentDigest[:]...)
	return sha256.Sum256(encoded)
}

type MemoryReceiptStore struct {
	mu         sync.Mutex
	receipts   map[ReceiptKey]ResultReceipt
	PutErr     error
	AdvanceErr error
}

// NewMemoryReceiptStore is a test contract implementation. Keeping the same
// instance across a simulated service restart models an external durable store;
// it is not the production PostgreSQL schema, which remains frozen for S1b.
func NewMemoryReceiptStore() *MemoryReceiptStore {
	return &MemoryReceiptStore{receipts: make(map[ReceiptKey]ResultReceipt)}
}

func (store *MemoryReceiptStore) PutWriteOnce(_ context.Context, receipt ResultReceipt) (ResultReceipt, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.PutErr != nil {
		return ResultReceipt{}, false, store.PutErr
	}
	if err := validateReceipt(receipt); err != nil {
		return ResultReceipt{}, false, err
	}
	current, exists := store.receipts[receipt.Key]
	if exists {
		if !sameImmutableReceipt(current, receipt) {
			return ResultReceipt{}, false, ErrReceiptConflict
		}
		return current, false, nil
	}
	store.receipts[receipt.Key] = receipt
	return receipt, true, nil
}

func (store *MemoryReceiptStore) Get(_ context.Context, key ReceiptKey) (ResultReceipt, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	receipt, ok := store.receipts[key]
	return receipt, ok, nil
}

func (store *MemoryReceiptStore) Advance(_ context.Context, key ReceiptKey, update ReceiptAdvance) (ResultReceipt, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.AdvanceErr != nil {
		return ResultReceipt{}, store.AdvanceErr
	}
	receipt, exists := store.receipts[key]
	if !exists {
		return ResultReceipt{}, ErrReceiptNotFound
	}
	if update.Append != nil {
		if !validAppendTransition(receipt.AppendConfirmation, *update.Append) {
			return ResultReceipt{}, fmt.Errorf("%w: append %d -> %d", ErrReceiptTransition, receipt.AppendConfirmation, *update.Append)
		}
		receipt.AppendConfirmation = *update.Append
	}
	if update.Reconciliation != nil {
		if !validReconciliationTransition(receipt.Reconciliation, *update.Reconciliation) {
			return ResultReceipt{}, fmt.Errorf("%w: reconciliation %d -> %d", ErrReceiptTransition, receipt.Reconciliation, *update.Reconciliation)
		}
		receipt.Reconciliation = *update.Reconciliation
	}
	if update.Delivery != nil {
		if *update.Delivery < receipt.Delivery || *update.Delivery > ResponseSendCompleted {
			return ResultReceipt{}, fmt.Errorf("%w: delivery %d -> %d", ErrReceiptTransition, receipt.Delivery, *update.Delivery)
		}
		receipt.Delivery = *update.Delivery
	}
	store.receipts[key] = receipt
	return receipt, nil
}

func validateReceipt(receipt ResultReceipt) error {
	if len(receipt.Key.SessionID) == 0 || len(receipt.Key.SessionID) > 128 || len(receipt.Key.RequestID) == 0 || len(receipt.Key.RequestID) > 128 || receipt.ReportedDurabilityAtResponse < DurabilityDurable || receipt.ReportedDurabilityAtResponse > DurabilityLost {
		return fmt.Errorf("b5wal: invalid result receipt")
	}
	if receipt.Key.EventUUID == ([16]byte{}) || receipt.BusinessEventDigest == ([32]byte{}) || receipt.WALAppendReceiptDigest == ([32]byte{}) {
		return fmt.Errorf("b5wal: result receipt is not bound to event and append digests")
	}
	if receipt.Delivery != ResponsePrepared {
		return fmt.Errorf("b5wal: new result receipt must be PREPARED")
	}
	return nil
}

func sameImmutableReceipt(left, right ResultReceipt) bool {
	return left.Key == right.Key && subtle.ConstantTimeCompare(left.BusinessEventDigest[:], right.BusinessEventDigest[:]) == 1 && subtle.ConstantTimeCompare(left.WALAppendReceiptDigest[:], right.WALAppendReceiptDigest[:]) == 1 && left.ReportedDurabilityAtResponse == right.ReportedDurabilityAtResponse
}

func validAppendTransition(from, to AppendConfirmationState) bool {
	if from == to {
		return true
	}
	switch from {
	case AppendUnknown:
		return to == AppendTimeout
	case AppendTimeout:
		return to == AppendLateConfirmed || to == AppendRecovered
	case AppendLateConfirmed:
		return to == AppendRecovered
	default:
		return false
	}
}

func validReconciliationTransition(from, to ReconciliationState) bool {
	if from == to {
		return true
	}
	return (from == ReconciliationNone && to == ReconciliationReplayStaged) || (from == ReconciliationReplayStaged && to == ReconciliationPrimaryDurable)
}

// PersistThenSend is the mandatory response fence. A Put or SendStarted update
// failure prevents send. A post-send completion-write failure is returned, but
// the immutable prepared result remains reproducible on retry.
func PersistThenSend(ctx context.Context, store ReceiptStore, receipt ResultReceipt, send func(ResultReceipt) error) error {
	stored, _, err := store.PutWriteOnce(ctx, receipt)
	if err != nil {
		return err
	}
	if stored.Delivery == ResponsePrepared {
		started := ResponseSendStarted
		stored, err = store.Advance(ctx, receipt.Key, ReceiptAdvance{Delivery: &started})
		if err != nil {
			return err
		}
	}
	if err := send(stored); err != nil {
		return err
	}
	if stored.Delivery != ResponseSendCompleted {
		completed := ResponseSendCompleted
		_, err = store.Advance(ctx, receipt.Key, ReceiptAdvance{Delivery: &completed})
	}
	return err
}

type HistoricalResponseState uint8

const (
	HistoricalResponseUnknown HistoricalResponseState = iota
	HistoricalResponsePrepared
)

type RestartView struct {
	HistoricalResponse HistoricalResponseState
	Receipt            ResultReceipt
	RecordValid        bool
}

// RestartJoin never derives a historical AUDIT_PENDING/LOST value from WAL.
// Without a durable receipt, the historical response is unknown/unreported.
// With one, its reported value is immutable while append/reconciliation may
// advance monotonically based on verified scan and replay facts.
func RestartJoin(ctx context.Context, store ReceiptStore, key ReceiptKey, recordValid, primaryDurable bool) (RestartView, error) {
	receipt, exists, err := store.Get(ctx, key)
	if err != nil {
		return RestartView{}, err
	}
	if !exists {
		return RestartView{HistoricalResponse: HistoricalResponseUnknown, RecordValid: recordValid}, nil
	}
	view := RestartView{HistoricalResponse: HistoricalResponsePrepared, Receipt: receipt, RecordValid: recordValid}
	if !recordValid {
		return view, nil
	}
	if receipt.AppendConfirmation == AppendTimeout || receipt.AppendConfirmation == AppendLateConfirmed {
		recovered := AppendRecovered
		receipt, err = store.Advance(ctx, key, ReceiptAdvance{Append: &recovered})
		if err != nil {
			return RestartView{}, err
		}
	}
	if receipt.Reconciliation == ReconciliationNone {
		staged := ReconciliationReplayStaged
		receipt, err = store.Advance(ctx, key, ReceiptAdvance{Reconciliation: &staged})
		if err != nil {
			return RestartView{}, err
		}
	}
	if primaryDurable && receipt.Reconciliation == ReconciliationReplayStaged {
		durable := ReconciliationPrimaryDurable
		receipt, err = store.Advance(ctx, key, ReceiptAdvance{Reconciliation: &durable})
		if err != nil {
			return RestartView{}, err
		}
	}
	view.Receipt = receipt
	return view, nil
}
