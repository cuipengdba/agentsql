package b5coordinator

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/cuipengdba/agentsql/internal/b5"
	"github.com/cuipengdba/agentsql/internal/store"
)

// MemoryTransactionStore is a deterministic implementation of the exact
// durable-store contract. It is intended for coordinator model/fault tests;
// production construction uses store.B5TransactionRepository.
type MemoryTransactionStore struct {
	mu     sync.Mutex
	values map[string]store.B5Transaction
}

func NewMemoryTransactionStore() *MemoryTransactionStore {
	return &MemoryTransactionStore{values: make(map[string]store.B5Transaction)}
}
func (memory *MemoryTransactionStore) Create(_ context.Context, value store.B5Transaction) (store.B5Transaction, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	if _, exists := memory.values[value.TransactionID]; exists {
		return store.B5Transaction{}, errors.New("duplicate transaction")
	}
	value.Revision = 1
	if value.CreatedAt.IsZero() {
		value.CreatedAt = time.Now().UTC()
	}
	value.UpdatedAt = value.CreatedAt
	memory.values[value.TransactionID] = cloneTransaction(value)
	return cloneTransaction(value), nil
}
func (memory *MemoryTransactionStore) Get(_ context.Context, id string) (store.B5Transaction, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	value, ok := memory.values[id]
	if !ok {
		return store.B5Transaction{}, store.ErrNotFound
	}
	return cloneTransaction(value), nil
}
func (memory *MemoryTransactionStore) CASState(_ context.Context, id string, revision int64, fromStatus b5.TransactionStatus, fromPhase b5.TransactionPhase, toStatus b5.TransactionStatus, toPhase b5.TransactionPhase) (store.B5Transaction, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	value, ok := memory.values[id]
	if !ok {
		return store.B5Transaction{}, store.ErrNotFound
	}
	if value.Revision != revision || value.Status != fromStatus || value.Phase != fromPhase {
		return store.B5Transaction{}, store.ErrB5CASConflict
	}
	if !memoryTransition(fromPhase, toPhase, toStatus) {
		return store.B5Transaction{}, store.ErrB5InvalidTransition
	}
	value.Status, value.Phase, value.Revision, value.UpdatedAt = toStatus, toPhase, value.Revision+1, time.Now().UTC()
	memory.values[id] = value
	return cloneTransaction(value), nil
}
func (memory *MemoryTransactionStore) CASProgress(_ context.Context, id string, revision int64, status b5.TransactionStatus, phase b5.TransactionPhase, update store.B5TransactionProgress) (store.B5Transaction, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	value, ok := memory.values[id]
	if !ok {
		return store.B5Transaction{}, store.ErrNotFound
	}
	if value.Revision != revision || value.Status != status || value.Phase != phase {
		return store.B5Transaction{}, store.ErrB5CASConflict
	}
	if update.IdleDeadline != nil {
		value.IdleDeadline = *update.IdleDeadline
	}
	if update.StatementDeadline != nil {
		value.StatementDeadline = cloneTimePointer(*update.StatementDeadline)
	}
	if update.BackendPID != nil {
		value.BackendPID = cloneIntPointer(*update.BackendPID)
	}
	if update.BackendSecretDigest != nil {
		value.BackendSecretDigest = append([]byte(nil), (*update.BackendSecretDigest)...)
	}
	if update.BackendStartedAt != nil {
		value.BackendStartedAt = cloneTimePointer(*update.BackendStartedAt)
	}
	if update.ConnectionGeneration != nil {
		value.ConnectionGeneration = *update.ConnectionGeneration
	}
	if update.LeaseGeneration != nil {
		value.LeaseGeneration = *update.LeaseGeneration
	}
	if update.StatementCount != nil {
		value.StatementCount = *update.StatementCount
	}
	if update.TransactionSeq != nil {
		value.TransactionSeq = *update.TransactionSeq
	}
	if update.PreviousEventDigest != nil {
		value.PreviousTxEventDigest = append([]byte(nil), (*update.PreviousEventDigest)...)
	}
	value.Revision++
	value.UpdatedAt = time.Now().UTC()
	memory.values[id] = value
	return cloneTransaction(value), nil
}
func (memory *MemoryTransactionStore) ListExpired(_ context.Context, now time.Time, limit int) ([]store.B5Transaction, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	values := make([]store.B5Transaction, 0)
	for _, value := range memory.values {
		if value.Status != b5.TransactionTerminal && (!value.IdleDeadline.After(now) || !value.WallDeadline.After(now) || value.StatementDeadline != nil && !value.StatementDeadline.After(now)) {
			values = append(values, cloneTransaction(value))
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].WallDeadline.Before(values[j].WallDeadline) })
	if len(values) > limit {
		values = values[:limit]
	}
	return values, nil
}
func memoryTransition(from, to b5.TransactionPhase, status b5.TransactionStatus) bool {
	edges := map[b5.TransactionPhase][]b5.TransactionPhase{b5.PhaseReady: {b5.PhasePlanReady}, b5.PhasePlanReady: {b5.PhaseApprovalConsumed}, b5.PhaseApprovalConsumed: {b5.PhaseConnectionPinned, b5.PhaseBeginFailTerminating}, b5.PhaseConnectionPinned: {b5.PhaseNativeBegun, b5.PhaseBeginFailTerminating}, b5.PhaseNativeBegun: {b5.PhaseContextFixed, b5.PhaseBeginFailTerminating}, b5.PhaseContextFixed: {b5.PhaseSealedInTx, b5.PhaseBeginFailTerminating}, b5.PhaseSealedInTx: {b5.PhaseBeginAuditing, b5.PhaseBeginFailTerminating}, b5.PhaseBeginAuditing: {b5.PhaseActive, b5.PhaseBeginFailTerminating}, b5.PhaseActive: {b5.PhaseRollbackOnly, b5.PhaseCommitting}, b5.PhaseRollbackOnly: {b5.PhaseRollingBack}, b5.PhaseCommitting: {b5.PhaseTerminal}, b5.PhaseRollingBack: {b5.PhaseTerminal}, b5.PhaseBeginFailTerminating: {b5.PhaseTerminal}, b5.PhaseTerminal: {b5.PhaseFinalFence}}
	found := false
	for _, candidate := range edges[from] {
		found = found || candidate == to
	}
	if !found {
		return false
	}
	switch to {
	case b5.PhasePlanReady, b5.PhaseApprovalConsumed, b5.PhaseConnectionPinned, b5.PhaseNativeBegun, b5.PhaseContextFixed, b5.PhaseSealedInTx, b5.PhaseBeginAuditing:
		return status == b5.TransactionPending
	case b5.PhaseActive, b5.PhaseCommitting, b5.PhaseRollingBack:
		return status == b5.TransactionActive
	case b5.PhaseRollbackOnly:
		return status == b5.TransactionRollbackOnly
	case b5.PhaseBeginFailTerminating, b5.PhaseTerminal, b5.PhaseFinalFence:
		return status == b5.TransactionTerminal
	}
	return false
}
func cloneTransaction(value store.B5Transaction) store.B5Transaction {
	value.PlanDigest = append([]byte(nil), value.PlanDigest...)
	value.BackendSecretDigest = append([]byte(nil), value.BackendSecretDigest...)
	value.PreviousTxEventDigest = append([]byte(nil), value.PreviousTxEventDigest...)
	value.ApprovalID = cloneStringPointer(value.ApprovalID)
	value.StatementDeadline = cloneTimePointer(value.StatementDeadline)
	value.BackendPID = cloneIntPointer(value.BackendPID)
	value.BackendStartedAt = cloneTimePointer(value.BackendStartedAt)
	return value
}
func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
func cloneTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
func cloneIntPointer(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

type MemoryApproval struct {
	ID            string
	PlanDigest    [32]byte
	BoundsDigest  [32]byte
	ExpiresAt     time.Time
	Status        string
	Generation    uint64
	TransactionID string
}
type MemoryApprovalStore struct {
	mu     sync.Mutex
	values map[string]MemoryApproval
}

func NewMemoryApprovalStore() *MemoryApprovalStore {
	return &MemoryApprovalStore{values: make(map[string]MemoryApproval)}
}
func (memory *MemoryApprovalStore) Put(value MemoryApproval) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	if value.Status == "" {
		value.Status = "approved"
	}
	memory.values[value.ID] = value
}
func (memory *MemoryApprovalStore) Get(id string) (MemoryApproval, bool) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	value, ok := memory.values[id]
	return value, ok
}
func (memory *MemoryApprovalStore) Consume(_ context.Context, request ApprovalConsumeRequest) (ApprovalLease, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	value, ok := memory.values[request.ApprovalID]
	if !ok || value.Status != "approved" || !value.ExpiresAt.After(request.Now) || subtle.ConstantTimeCompare(value.PlanDigest[:], request.PlanDigest[:]) != 1 {
		return ApprovalLease{}, ErrApprovalInvalid
	}
	value.Generation++
	value.Status = "consumed_begin_pending"
	value.TransactionID = request.TransactionID
	memory.values[value.ID] = value
	return ApprovalLease{ID: value.ID, Generation: value.Generation, ApprovedBoundsDigest: value.BoundsDigest, ExpiresAt: value.ExpiresAt}, nil
}
func (memory *MemoryApprovalStore) MarkBeginFailed(_ context.Context, lease ApprovalLease, transactionID string) error {
	return memory.mark(lease, transactionID, "consumed_begin_pending", "consumed_begin_failed")
}
func (memory *MemoryApprovalStore) MarkActive(_ context.Context, lease ApprovalLease, transactionID string) error {
	return memory.mark(lease, transactionID, "consumed_begin_pending", "consumed_active")
}
func (memory *MemoryApprovalStore) mark(lease ApprovalLease, transactionID, from, to string) error {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	value, ok := memory.values[lease.ID]
	if !ok || value.Generation != lease.Generation || value.TransactionID != transactionID || value.Status != from {
		return ErrApprovalInvalid
	}
	value.Status = to
	memory.values[value.ID] = value
	return nil
}

type StaticSessionGate struct {
	SessionID  string
	OwnerEpoch uint64
}

func (gate StaticSessionGate) Validate(_ context.Context, session SessionAuthorization) error {
	if session.SessionID != gate.SessionID || session.OwnerEpoch != gate.OwnerEpoch {
		return ErrWrongOwner
	}
	return nil
}

type MemoryAuditor struct {
	mu         sync.Mutex
	Events     []AuditEvent
	Fail       func(AuditEvent) error
	Durability func(AuditEvent) b5.AuditDurability
}

func (audit *MemoryAuditor) Barrier(_ context.Context, event AuditEvent) (AuditResult, error) {
	audit.mu.Lock()
	defer audit.mu.Unlock()
	audit.Events = append(audit.Events, event)
	if audit.Fail != nil {
		if err := audit.Fail(event); err != nil {
			return AuditResult{}, err
		}
	}
	durability := b5.DurabilityDurable
	if audit.Durability != nil {
		durability = audit.Durability(event)
	}
	digest := sha256.Sum256(append([]byte(event.Kind), event.PlanDigest[:]...))
	return AuditResult{Durability: durability, EventDigest: digest}, nil
}
