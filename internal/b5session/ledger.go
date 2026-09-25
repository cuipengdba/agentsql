package b5session

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/cuipengdba/agentsql/internal/b5"
)

var (
	ErrAdmissionDenied      = errors.New("b5session: shared admission denied")
	ErrCASConflict          = errors.New("b5session: generation compare-and-swap conflict")
	ErrInvalidTransition    = errors.New("b5session: invalid lease transition")
	ErrUnknownProofSchema   = errors.New("b5session: unknown proof schema")
	ErrBackendStillPresent  = errors.New("b5session: backend still present")
	ErrInventoryUnavailable = errors.New("b5session: backend inventory unavailable")
)

type ClaimKind string

const (
	ClaimPoolEnvelope ClaimKind = "POOL_ENVELOPE"
	ClaimDirectPinned ClaimKind = "DIRECT_PINNED"
	ClaimMigration    ClaimKind = "MIGRATION"
	ClaimHealth       ClaimKind = "HEALTH"
	ClaimAdmin        ClaimKind = "ADMIN"
)

type ClaimState string

const (
	ClaimActive   ClaimState = "ACTIVE"
	ClaimFenced   ClaimState = "FENCED"
	ClaimReleased ClaimState = "RELEASED"
)

type LeaseState string

const (
	LeaseDialPermitOutstanding LeaseState = "DIAL_PERMIT_OUTSTANDING"
	LeaseInUse                 LeaseState = "IN_USE"
	LeaseTerminating           LeaseState = "TERMINATING"
	LeaseQuarantined           LeaseState = "QUARANTINED"
	LeaseFree                  LeaseState = "FREE"
	LeaseNeverAllocated        LeaseState = "NEVER_ALLOCATED"
	LeaseBackendAbsent         LeaseState = "BACKEND_ABSENT_CONFIRMED"
)

type Budget struct {
	DatasourceID          string
	HardLimit             int
	AdminEmergencyReserve int
	ConnectionUnitsUsed   int
	PlanBytesLimit        int64
	PlanBytesUsed         int64
	Revision              int64
}

type Claim struct {
	ID, DatasourceID                  string
	Kind                              ClaimKind
	OwnerInstanceID, OwnerIncarnation string
	Generation                        uint64
	ClaimedCapacity, MaxOpenConns     int
	State                             ClaimState
	Revision                          int64
}

type BackendIdentity struct {
	ServerID       string
	Database       string
	PID            int32
	BackendStarted time.Time
	DialToken      string
}

func (identity BackendIdentity) Complete() bool {
	return identity.ServerID != "" && identity.Database != "" && identity.PID > 0 && !identity.BackendStarted.IsZero() && identity.DialToken != ""
}

func (identity BackendIdentity) Equal(other BackendIdentity) bool {
	return identity.ServerID == other.ServerID && identity.Database == other.Database && identity.PID == other.PID && identity.BackendStarted.Equal(other.BackendStarted) && identity.DialToken == other.DialToken
}

type Lease struct {
	ID, ClaimID, DatasourceID         string
	OwnerInstanceID, OwnerIncarnation string
	ClaimGeneration, Generation       uint64
	DialPermitID, DialToken           string
	DialAttempted                     bool
	State                             LeaseState
	Backend                           BackendIdentity
	Disposition                       b5.ConnectionDisposition
	ProofSchemaID                     string
	ProofSchemaVersion                uint16
	ProofDigest                       [32]byte
	InventoryEpoch                    uint64
	Charged                           bool
	Revision                          int64
}

type ClaimRequest struct {
	ClaimID, DatasourceID             string
	Kind                              ClaimKind
	OwnerInstanceID, OwnerIncarnation string
	Capacity, MaxOpenConns            int
}

type PlanLease struct {
	ID, DatasourceID                  string
	OwnerInstanceID, OwnerIncarnation string
	Bytes                             int64
}

type DispositionProof struct {
	SchemaID       string
	SchemaVersion  uint16
	Disposition    b5.ConnectionDisposition
	Digest         [32]byte
	PoolTransfer   bool
	BackendAbsence bool
	InventoryEpoch uint64
	SourceDigest   [32]byte
}

type TerminalDisposition struct {
	Schema        string
	SchemaVersion uint16
	Disposition   b5.ConnectionDisposition
}

// DispositionProofFromTerminal is the only normal constructor. It consumes the
// frozen b5terminal result instead of allowing the pool layer to infer a
// disposition from an error string.
func DispositionProofFromTerminal(resolution TerminalDisposition, sourceDigest [32]byte, poolTransfer, backendAbsence bool, inventoryEpoch uint64) (DispositionProof, error) {
	if resolution.Schema != b5.DispositionProofSchemaID || resolution.SchemaVersion != b5.DispositionProofVersion || sourceDigest == ([32]byte{}) {
		return DispositionProof{}, ErrUnknownProofSchema
	}
	proof := DispositionProof{SchemaID: b5.DispositionProofSchemaID, SchemaVersion: b5.DispositionProofVersion, Disposition: resolution.Disposition, PoolTransfer: poolTransfer, BackendAbsence: backendAbsence, InventoryEpoch: inventoryEpoch, SourceDigest: sourceDigest}
	proof.Digest = dispositionProofDigest(proof)
	if _, _, err := validateDisposition(LeaseInUse, proof); err != nil {
		return DispositionProof{}, err
	}
	return proof, nil
}

type AbsenceProof struct {
	SchemaID       string
	SchemaVersion  uint16
	Digest         [32]byte
	InventoryEpoch uint64
	ObservedAt     time.Time
}

func NewAbsenceProof(epoch uint64, observed time.Time, material []byte) AbsenceProof {
	return AbsenceProof{SchemaID: b5.PoolChildProofSchemaID, SchemaVersion: b5.PoolChildProofVersion, Digest: sha256.Sum256(material), InventoryEpoch: epoch, ObservedAt: observed}
}

// LedgerStore is the complete durable S3 surface. Implementations must
// linearize admission and every generation transition in their shared store.
type LedgerStore interface {
	ConfigureBudget(context.Context, Budget) (Budget, error)
	Budget(context.Context, string) (Budget, error)
	AcquireClaim(context.Context, ClaimRequest) (Claim, error)
	Claim(context.Context, string) (Claim, error)
	ReservePlan(context.Context, PlanLease) error
	ReleasePlan(context.Context, string) error
	IssueDialPermit(context.Context, string, uint64, string, string) (Lease, error)
	MarkDialStarted(context.Context, string, uint64) (Lease, error)
	BindBackend(context.Context, string, uint64, BackendIdentity) (Lease, error)
	AcquireFree(context.Context, string, uint64, string, string) (Lease, error)
	BeginTermination(context.Context, string, uint64) (Lease, error)
	ApplyDisposition(context.Context, string, uint64, DispositionProof) (Lease, error)
	FenceOwner(context.Context, string, string) ([]Claim, error)
	ListClaimLeases(context.Context, string) ([]Lease, error)
	AbandonUndialed(context.Context, string, uint64, [32]byte) (Lease, error)
	ReconcileAbsence(context.Context, string, uint64, AbsenceProof) (Lease, error)
	ShrinkFencedClaim(context.Context, string, uint64) (Claim, error)
}

func validateClaimRequest(request ClaimRequest) error {
	if request.ClaimID == "" || request.DatasourceID == "" || request.OwnerInstanceID == "" || request.OwnerIncarnation == "" || request.Capacity <= 0 || request.MaxOpenConns < 0 || request.MaxOpenConns > request.Capacity {
		return fmt.Errorf("%w: malformed claim", ErrAdmissionDenied)
	}
	switch request.Kind {
	case ClaimPoolEnvelope:
		if request.MaxOpenConns == 0 {
			return fmt.Errorf("%w: pool MaxOpenConns is zero", ErrAdmissionDenied)
		}
	case ClaimDirectPinned, ClaimMigration, ClaimHealth, ClaimAdmin:
		if request.Capacity != 1 || request.MaxOpenConns != 1 {
			return fmt.Errorf("%w: non-pool claims are one unit", ErrAdmissionDenied)
		}
	default:
		return fmt.Errorf("%w: unknown claim kind", ErrAdmissionDenied)
	}
	return nil
}

func validateDisposition(state LeaseState, proof DispositionProof) (LeaseState, bool, error) {
	if proof.SchemaID != b5.DispositionProofSchemaID || proof.SchemaVersion != b5.DispositionProofVersion || proof.SourceDigest == ([32]byte{}) || proof.Digest != dispositionProofDigest(proof) {
		return "", false, ErrUnknownProofSchema
	}
	switch proof.Disposition {
	case b5.DispositionReleased:
		if state != LeaseInUse && state != LeaseTerminating || !proof.PoolTransfer {
			return "", false, ErrInvalidTransition
		}
		return LeaseFree, true, nil
	case b5.DispositionDiscarded:
		if (state != LeaseInUse && state != LeaseTerminating && state != LeaseQuarantined) || !proof.BackendAbsence || proof.InventoryEpoch == 0 {
			return "", false, ErrInvalidTransition
		}
		return LeaseBackendAbsent, false, nil
	case b5.DispositionDiscardUnconfirmed:
		if state != LeaseInUse && state != LeaseTerminating {
			return "", false, ErrInvalidTransition
		}
		return LeaseQuarantined, true, nil
	default:
		return "", false, ErrInvalidTransition
	}
}

func dispositionProofDigest(proof DispositionProof) [32]byte {
	h := sha256.New()
	writeField(h, proof.SchemaID)
	writeUint64(h, uint64(proof.SchemaVersion))
	writeField(h, string(proof.Disposition))
	writeField(h, fmt.Sprintf("%t", proof.PoolTransfer))
	writeField(h, fmt.Sprintf("%t", proof.BackendAbsence))
	writeUint64(h, proof.InventoryEpoch)
	writeBytes(h, proof.SourceDigest[:])
	var digest [32]byte
	copy(digest[:], h.Sum(nil))
	return digest
}
