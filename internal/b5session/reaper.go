package b5session

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"
)

type InventoryObservation struct {
	Epoch      uint64
	ObservedAt time.Time
	Backends   []BackendIdentity
}

type BackendInventory interface {
	ObservePermit(context.Context, string) (InventoryObservation, error)
	Terminate(context.Context, BackendIdentity) error
}

type Reaper struct {
	Ledger    LedgerStore
	Inventory BackendInventory
}

type ReapReport struct {
	FencedClaims, ReconciledChildren, TerminatedBackends int
	ClaimsReleased, CapacityReleased                     int
	InventoryBlocked                                     bool
}

// ReapOwner is restartable and idempotent. It first fences the old owner, then
// reconciles each durable child. Inventory errors never reduce a charged
// claim. A second observation after terminate is the only absence proof.
func (r Reaper) ReapOwner(ctx context.Context, owner, incarnation string) (ReapReport, error) {
	if r.Ledger == nil || r.Inventory == nil || owner == "" || incarnation == "" {
		return ReapReport{}, ErrInvalidTransition
	}
	claims, err := r.Ledger.FenceOwner(ctx, owner, incarnation)
	if err != nil {
		return ReapReport{}, err
	}
	report := ReapReport{FencedClaims: len(claims)}
	var joined error
	for _, claim := range claims {
		before := claim.ClaimedCapacity
		leases, listErr := r.Ledger.ListClaimLeases(ctx, claim.ID)
		if listErr != nil {
			joined = errors.Join(joined, listErr)
			continue
		}
		for _, lease := range leases {
			if !lease.Charged {
				continue
			}
			if lease.State == LeaseDialPermitOutstanding && !lease.DialAttempted {
				digest := sha256.Sum256([]byte("never-dialed\x00" + lease.DialPermitID + "\x00" + incarnation))
				if _, abandonErr := r.Ledger.AbandonUndialed(ctx, lease.ID, lease.Generation, digest); abandonErr != nil && !errors.Is(abandonErr, ErrCASConflict) {
					joined = errors.Join(joined, abandonErr)
				} else if abandonErr == nil {
					report.ReconciledChildren++
				}
				continue
			}
			observation, observeErr := r.Inventory.ObservePermit(ctx, lease.DialToken)
			if observeErr != nil {
				report.InventoryBlocked = true
				joined = errors.Join(joined, fmt.Errorf("observe dial permit %s: %w", lease.DialPermitID, observeErr))
				continue
			}
			for _, backend := range observation.Backends {
				if lease.Backend.Complete() && !lease.Backend.Equal(backend) {
					// Same PID with a different backend start/permit is PID reuse,
					// not authority to terminate the replacement process.
					continue
				}
				if terminateErr := r.Inventory.Terminate(ctx, backend); terminateErr != nil {
					joined = errors.Join(joined, terminateErr)
					continue
				}
				report.TerminatedBackends++
			}
			confirmed, confirmErr := r.Inventory.ObservePermit(ctx, lease.DialToken)
			if confirmErr != nil {
				report.InventoryBlocked = true
				joined = errors.Join(joined, confirmErr)
				continue
			}
			if len(confirmed.Backends) != 0 {
				joined = errors.Join(joined, ErrBackendStillPresent)
				continue
			}
			if confirmed.Epoch <= lease.InventoryEpoch {
				joined = errors.Join(joined, ErrUnknownProofSchema)
				continue
			}
			material := []byte(fmt.Sprintf("absence\x00%s\x00%d\x00%d", lease.DialPermitID, observation.Epoch, confirmed.Epoch))
			proof := NewAbsenceProof(confirmed.Epoch, confirmed.ObservedAt, material)
			if _, reconcileErr := r.Ledger.ReconcileAbsence(ctx, lease.ID, lease.Generation, proof); reconcileErr != nil && !errors.Is(reconcileErr, ErrCASConflict) {
				joined = errors.Join(joined, reconcileErr)
			} else if reconcileErr == nil {
				report.ReconciledChildren++
			}
		}
		shrunk, shrinkErr := r.Ledger.ShrinkFencedClaim(ctx, claim.ID, claim.Generation)
		if shrinkErr != nil {
			joined = errors.Join(joined, shrinkErr)
			continue
		}
		report.CapacityReleased += before - shrunk.ClaimedCapacity
		if shrunk.State == ClaimReleased {
			report.ClaimsReleased++
		}
	}
	return report, joined
}
