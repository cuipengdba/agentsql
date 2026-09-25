package b5session

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/cuipengdba/agentsql/internal/b5"
	"github.com/cuipengdba/agentsql/internal/store"
)

// MemorySessionStore is a deterministic CAS reference implementation. Multiple
// Directory instances may share it in race tests; cross-process deployments
// use B5SessionRepository.
type MemorySessionStore struct {
	mu       sync.Mutex
	sessions map[string]store.B5Session
}

func NewMemorySessionStore() *MemorySessionStore {
	return &MemorySessionStore{sessions: make(map[string]store.B5Session)}
}

func (s *MemorySessionStore) Create(_ context.Context, value store.B5Session) (store.B5Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.sessions[value.SessionID]; exists {
		return store.B5Session{}, store.ErrB5CASConflict
	}
	value.Revision = 1
	value.CreatedAt = time.Now().UTC()
	value.UpdatedAt = value.CreatedAt
	value.ContinuationHMACDigest = append([]byte(nil), value.ContinuationHMACDigest...)
	s.sessions[value.SessionID] = value
	return cloneSession(value), nil
}

func (s *MemorySessionStore) Get(_ context.Context, id string) (store.B5Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.sessions[id]
	if !ok {
		return store.B5Session{}, store.ErrNotFound
	}
	return cloneSession(value), nil
}

func (s *MemorySessionStore) CASStatus(_ context.Context, id string, revision int64, from, to b5.SessionStatus, idle time.Time) (store.B5Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.sessions[id]
	if !ok || value.Revision != revision || value.Status != from {
		return store.B5Session{}, store.ErrB5CASConflict
	}
	if !(from == to || from == b5.SessionReady && to == b5.SessionActive || from == b5.SessionActive && to == b5.SessionReady || (from == b5.SessionReady || from == b5.SessionActive) && (to == b5.SessionTerminal || to == b5.SessionExpired)) {
		return store.B5Session{}, store.ErrB5InvalidTransition
	}
	value.Status, value.IdleExpiresAt = to, idle
	value.Revision++
	value.UpdatedAt = time.Now().UTC()
	s.sessions[id] = value
	return cloneSession(value), nil
}

func (s *MemorySessionStore) CASOwner(_ context.Context, id string, revision int64, owner string, epoch uint64, newOwner, route, ciphertext string, digest []byte) (store.B5Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.sessions[id]
	if !ok || value.Revision != revision || value.OwnerInstanceID != owner || value.OwnerEpoch != epoch || value.Status == b5.SessionTerminal || value.Status == b5.SessionExpired {
		return store.B5Session{}, store.ErrB5CASConflict
	}
	value.OwnerInstanceID, value.OwnerEpoch, value.StickyRoute = newOwner, epoch+1, route
	value.ContinuationKeyCiphertext = ciphertext
	value.ContinuationHMACDigest = append([]byte(nil), digest...)
	value.Revision++
	value.UpdatedAt = time.Now().UTC()
	s.sessions[id] = value
	return cloneSession(value), nil
}

func (s *MemorySessionStore) ListExpired(_ context.Context, now time.Time, limit int) ([]store.B5Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	values := make([]store.B5Session, 0)
	for _, value := range s.sessions {
		if (value.Status == b5.SessionReady || value.Status == b5.SessionActive) && (!value.IdleExpiresAt.After(now) || !value.AbsoluteExpiresAt.After(now)) {
			values = append(values, cloneSession(value))
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].SessionID < values[j].SessionID })
	if len(values) > limit {
		values = values[:limit]
	}
	return values, nil
}

func cloneSession(value store.B5Session) store.B5Session {
	value.ContinuationHMACDigest = append([]byte(nil), value.ContinuationHMACDigest...)
	return value
}

type MemoryLedger struct {
	mu      sync.Mutex
	budgets map[string]Budget
	claims  map[string]Claim
	leases  map[string]Lease
	plans   map[string]PlanLease
}

func NewMemoryLedger() *MemoryLedger {
	return &MemoryLedger{budgets: make(map[string]Budget), claims: make(map[string]Claim), leases: make(map[string]Lease), plans: make(map[string]PlanLease)}
}

func (l *MemoryLedger) ConfigureBudget(_ context.Context, budget Budget) (Budget, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if budget.DatasourceID == "" || budget.HardLimit <= 0 || budget.AdminEmergencyReserve < 0 || budget.AdminEmergencyReserve >= budget.HardLimit || budget.PlanBytesLimit <= 0 {
		return Budget{}, ErrAdmissionDenied
	}
	old := l.budgets[budget.DatasourceID]
	if old.ConnectionUnitsUsed > budget.HardLimit-budget.AdminEmergencyReserve || old.PlanBytesUsed > budget.PlanBytesLimit {
		return Budget{}, ErrAdmissionDenied
	}
	budget.ConnectionUnitsUsed, budget.PlanBytesUsed = old.ConnectionUnitsUsed, old.PlanBytesUsed
	budget.Revision = old.Revision + 1
	l.budgets[budget.DatasourceID] = budget
	return budget, nil
}

func (l *MemoryLedger) Budget(_ context.Context, datasource string) (Budget, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	value, ok := l.budgets[datasource]
	if !ok {
		return Budget{}, store.ErrNotFound
	}
	return value, nil
}

func (l *MemoryLedger) AcquireClaim(_ context.Context, request ClaimRequest) (Claim, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := validateClaimRequest(request); err != nil {
		return Claim{}, err
	}
	if _, exists := l.claims[request.ClaimID]; exists {
		return Claim{}, ErrCASConflict
	}
	budget, ok := l.budgets[request.DatasourceID]
	if !ok || budget.ConnectionUnitsUsed+request.Capacity > budget.HardLimit-budget.AdminEmergencyReserve {
		return Claim{}, ErrAdmissionDenied
	}
	budget.ConnectionUnitsUsed += request.Capacity
	budget.Revision++
	l.budgets[request.DatasourceID] = budget
	claim := Claim{ID: request.ClaimID, DatasourceID: request.DatasourceID, Kind: request.Kind, OwnerInstanceID: request.OwnerInstanceID, OwnerIncarnation: request.OwnerIncarnation, Generation: 1, ClaimedCapacity: request.Capacity, MaxOpenConns: request.MaxOpenConns, State: ClaimActive, Revision: 1}
	l.claims[claim.ID] = claim
	return claim, nil
}

func (l *MemoryLedger) Claim(_ context.Context, id string) (Claim, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	value, ok := l.claims[id]
	if !ok {
		return Claim{}, store.ErrNotFound
	}
	return value, nil
}

func (l *MemoryLedger) ReservePlan(_ context.Context, lease PlanLease) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if lease.ID == "" || lease.DatasourceID == "" || lease.OwnerInstanceID == "" || lease.OwnerIncarnation == "" || lease.Bytes <= 0 {
		return ErrAdmissionDenied
	}
	if _, exists := l.plans[lease.ID]; exists {
		return ErrCASConflict
	}
	budget, ok := l.budgets[lease.DatasourceID]
	if !ok || budget.PlanBytesUsed+lease.Bytes > budget.PlanBytesLimit {
		return ErrAdmissionDenied
	}
	budget.PlanBytesUsed += lease.Bytes
	budget.Revision++
	l.budgets[lease.DatasourceID] = budget
	l.plans[lease.ID] = lease
	return nil
}

func (l *MemoryLedger) ReleasePlan(_ context.Context, id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	lease, ok := l.plans[id]
	if !ok {
		return nil
	}
	budget := l.budgets[lease.DatasourceID]
	budget.PlanBytesUsed -= lease.Bytes
	budget.Revision++
	l.budgets[lease.DatasourceID] = budget
	delete(l.plans, id)
	return nil
}

func (l *MemoryLedger) IssueDialPermit(_ context.Context, claimID string, generation uint64, permitID, token string) (Lease, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	claim, ok := l.claims[claimID]
	if !ok || claim.State != ClaimActive || claim.Generation != generation || permitID == "" || token == "" {
		return Lease{}, ErrCASConflict
	}
	if _, exists := l.leases[permitID]; exists {
		return Lease{}, ErrCASConflict
	}
	charged := 0
	for _, lease := range l.leases {
		if lease.ClaimID == claimID && lease.Charged {
			charged++
		}
	}
	if charged >= claim.MaxOpenConns || charged >= claim.ClaimedCapacity {
		return Lease{}, ErrAdmissionDenied
	}
	lease := Lease{ID: permitID, ClaimID: claim.ID, DatasourceID: claim.DatasourceID, OwnerInstanceID: claim.OwnerInstanceID, OwnerIncarnation: claim.OwnerIncarnation, ClaimGeneration: claim.Generation, Generation: 1, DialPermitID: permitID, DialToken: token, State: LeaseDialPermitOutstanding, Charged: true, Revision: 1}
	l.leases[lease.ID] = lease
	return lease, nil
}

func (l *MemoryLedger) MarkDialStarted(_ context.Context, id string, generation uint64) (Lease, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	lease, ok := l.leases[id]
	if !ok || lease.Generation != generation || lease.State != LeaseDialPermitOutstanding || !l.leaseOwnerActive(lease) {
		return Lease{}, ErrCASConflict
	}
	lease.DialAttempted = true
	lease.Generation++
	lease.Revision++
	l.leases[id] = lease
	return lease, nil
}

func (l *MemoryLedger) BindBackend(_ context.Context, id string, generation uint64, backend BackendIdentity) (Lease, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	lease, ok := l.leases[id]
	if !ok || lease.Generation != generation || lease.State != LeaseDialPermitOutstanding || !l.leaseOwnerActive(lease) || !lease.DialAttempted || !backend.Complete() || backend.DialToken != lease.DialToken {
		return Lease{}, ErrInvalidTransition
	}
	lease.Backend, lease.State = backend, LeaseInUse
	lease.Generation++
	lease.Revision++
	l.leases[id] = lease
	return lease, nil
}

func (l *MemoryLedger) AcquireFree(_ context.Context, id string, generation uint64, owner, incarnation string) (Lease, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	lease, ok := l.leases[id]
	claim := l.claims[lease.ClaimID]
	if !ok || lease.Generation != generation || lease.State != LeaseFree || claim.State != ClaimActive || claim.Generation != lease.ClaimGeneration || claim.OwnerInstanceID != owner || claim.OwnerIncarnation != incarnation {
		return Lease{}, ErrCASConflict
	}
	lease.State = LeaseInUse
	lease.Generation++
	lease.Revision++
	l.leases[id] = lease
	return lease, nil
}

func (l *MemoryLedger) BeginTermination(_ context.Context, id string, generation uint64) (Lease, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	lease, ok := l.leases[id]
	if !ok || lease.Generation != generation || !l.leaseOwnerActive(lease) || (lease.State != LeaseInUse && lease.State != LeaseFree && lease.State != LeaseQuarantined) {
		return Lease{}, ErrInvalidTransition
	}
	lease.State = LeaseTerminating
	lease.Generation++
	lease.Revision++
	l.leases[id] = lease
	return lease, nil
}

func (l *MemoryLedger) ApplyDisposition(_ context.Context, id string, generation uint64, proof DispositionProof) (Lease, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	lease, ok := l.leases[id]
	if !ok || lease.Generation != generation || !l.leaseOwnerActive(lease) {
		return Lease{}, ErrCASConflict
	}
	next, charged, err := validateDisposition(lease.State, proof)
	if err != nil {
		return Lease{}, err
	}
	lease.State, lease.Charged, lease.Disposition = next, charged, proof.Disposition
	lease.ProofSchemaID, lease.ProofSchemaVersion, lease.ProofDigest = proof.SchemaID, proof.SchemaVersion, proof.Digest
	lease.InventoryEpoch = proof.InventoryEpoch
	lease.Generation++
	lease.Revision++
	l.leases[id] = lease
	return lease, nil
}

func (l *MemoryLedger) FenceOwner(_ context.Context, owner, incarnation string) ([]Claim, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	result := make([]Claim, 0)
	for id, claim := range l.claims {
		if claim.OwnerInstanceID == owner && claim.OwnerIncarnation == incarnation {
			if claim.State == ClaimActive {
				claim.State = ClaimFenced
				claim.Generation++
				claim.Revision++
				l.claims[id] = claim
			}
			if claim.State == ClaimFenced {
				result = append(result, claim)
			}
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func (l *MemoryLedger) ListClaimLeases(_ context.Context, claimID string) ([]Lease, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	result := make([]Lease, 0)
	for _, lease := range l.leases {
		if lease.ClaimID == claimID {
			result = append(result, lease)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func (l *MemoryLedger) AbandonUndialed(_ context.Context, id string, generation uint64, digest [32]byte) (Lease, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	lease, ok := l.leases[id]
	if !ok || lease.Generation != generation || lease.State != LeaseDialPermitOutstanding || lease.DialAttempted || digest == ([32]byte{}) {
		return Lease{}, ErrInvalidTransition
	}
	lease.State, lease.Charged = LeaseNeverAllocated, false
	lease.ProofSchemaID, lease.ProofSchemaVersion, lease.ProofDigest = b5.PoolChildProofSchemaID, b5.PoolChildProofVersion, digest
	lease.Generation++
	lease.Revision++
	l.leases[id] = lease
	return lease, nil
}

func (l *MemoryLedger) ReconcileAbsence(_ context.Context, id string, generation uint64, proof AbsenceProof) (Lease, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	lease, ok := l.leases[id]
	if !ok || lease.Generation != generation {
		return Lease{}, ErrCASConflict
	}
	if proof.SchemaID != b5.PoolChildProofSchemaID || proof.SchemaVersion != b5.PoolChildProofVersion || proof.Digest == ([32]byte{}) || proof.InventoryEpoch == 0 || proof.InventoryEpoch <= lease.InventoryEpoch {
		return Lease{}, ErrUnknownProofSchema
	}
	switch lease.State {
	case LeaseDialPermitOutstanding:
		if lease.DialAttempted {
			lease.State = LeaseBackendAbsent
		} else {
			lease.State = LeaseNeverAllocated
		}
	case LeaseInUse, LeaseFree, LeaseTerminating, LeaseQuarantined:
		lease.State = LeaseBackendAbsent
	default:
		return Lease{}, ErrInvalidTransition
	}
	lease.Charged = false
	lease.InventoryEpoch = proof.InventoryEpoch
	lease.ProofSchemaID, lease.ProofSchemaVersion, lease.ProofDigest = proof.SchemaID, proof.SchemaVersion, proof.Digest
	lease.Generation++
	lease.Revision++
	l.leases[id] = lease
	return lease, nil
}

func (l *MemoryLedger) ShrinkFencedClaim(_ context.Context, id string, generation uint64) (Claim, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	claim, ok := l.claims[id]
	if !ok || claim.State != ClaimFenced || claim.Generation != generation {
		return Claim{}, ErrCASConflict
	}
	charged := 0
	for _, lease := range l.leases {
		if lease.ClaimID == id && lease.Charged {
			charged++
		}
	}
	if charged > claim.ClaimedCapacity {
		return Claim{}, ErrInvalidTransition
	}
	delta := claim.ClaimedCapacity - charged
	budget := l.budgets[claim.DatasourceID]
	budget.ConnectionUnitsUsed -= delta
	budget.Revision++
	l.budgets[claim.DatasourceID] = budget
	claim.ClaimedCapacity, claim.MaxOpenConns = charged, charged
	if charged == 0 {
		claim.State = ClaimReleased
	}
	claim.Generation++
	claim.Revision++
	l.claims[id] = claim
	return claim, nil
}

func (l *MemoryLedger) leaseOwnerActive(lease Lease) bool {
	claim, ok := l.claims[lease.ClaimID]
	return ok && claim.State == ClaimActive && claim.Generation == lease.ClaimGeneration && claim.OwnerInstanceID == lease.OwnerInstanceID && claim.OwnerIncarnation == lease.OwnerIncarnation
}

var _ SessionStore = (*MemorySessionStore)(nil)
var _ LedgerStore = (*MemoryLedger)(nil)

func isNotFound(err error) bool { return errors.Is(err, store.ErrNotFound) }
