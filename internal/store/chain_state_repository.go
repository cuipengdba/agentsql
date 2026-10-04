package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ChainState is the persisted runtime state for one audit-chain domain.
type ChainState struct {
	ChainID          string
	ChainInstanceID  *string
	Status           string
	Mode             *string
	HeadSeq          int64
	HeadID           *int64
	HeadHash         *string
	GenesisAt        *time.Time
	ProtectedSinceID *int64
	BuildOwner       *string
	BuildLeaseUntil  *time.Time
	BuildEpoch       int
	LastBuiltID      *int64
	LastBuiltSeq     *int64
	LastBuiltHash    *string
	UpdatedAt        time.Time
}

// ChainVerification is the last verification observation for one chain.
type ChainVerification struct {
	ChainID             string
	ObservedInstanceID  *string
	ObservedHeadHash    *string
	Result              *string
	LastVerifiedHeadSeq *int64
	LastVerifiedAt      *time.Time
	BreakSeq            *int64
	BreakID             *int64
	BreakReason         *string
}

// ChainStateRepository provides read-only access to audit-chain state.
type ChainStateRepository struct {
	repositoryBase
}

// Get returns the state for one audit-chain domain.
func (repository *ChainStateRepository) Get(ctx context.Context, chainID string) (ChainState, error) {
	if err := repository.validate(ctx, "get"); err != nil {
		return ChainState{}, err
	}
	tenantID, tenantErr := repository.requireTenant(ctx, "get chain state")
	if tenantErr != nil {
		return ChainState{}, tenantErr
	}
	state, err := scanChainState(repository.db.QueryRowContext(ctx, repository.bind(`
SELECT chain_id, chain_instance_id, status, mode, head_seq, head_id, head_hash,
       genesis_at, protected_since_id, build_owner, build_lease_until, build_epoch,
       last_built_id, last_built_seq, last_built_hash, updated_at
FROM chain_state
WHERE chain_id = ? AND tenant_id = ?`), chainID, tenantID))
	if errors.Is(err, sql.ErrNoRows) {
		return ChainState{}, fmt.Errorf("get chain state %q: %w", chainID, errors.Join(ErrNotFound, err))
	}
	if err != nil {
		return ChainState{}, fmt.Errorf("get chain state %q: %w", chainID, err)
	}
	return state, nil
}

// GetVerification returns the last verification observation for one chain.
func (repository *ChainStateRepository) GetVerification(ctx context.Context, chainID string) (ChainVerification, error) {
	if err := repository.validate(ctx, "get verification"); err != nil {
		return ChainVerification{}, err
	}
	tenantID, tenantErr := repository.requireTenant(ctx, "get chain verification")
	if tenantErr != nil {
		return ChainVerification{}, tenantErr
	}
	verification, err := scanChainVerification(repository.db.QueryRowContext(ctx, repository.bind(`
SELECT chain_id, observed_instance_id, observed_head_hash, result,
       last_verified_head_seq, last_verified_at, break_seq, break_id, break_reason
FROM chain_verification
WHERE chain_id = ? AND tenant_id = ?`), chainID, tenantID))
	if errors.Is(err, sql.ErrNoRows) {
		return ChainVerification{}, fmt.Errorf("get chain verification %q: %w", chainID, errors.Join(ErrNotFound, err))
	}
	if err != nil {
		return ChainVerification{}, fmt.Errorf("get chain verification %q: %w", chainID, err)
	}
	return verification, nil
}

func (repository *ChainStateRepository) validate(ctx context.Context, operation string) error {
	if ctx == nil {
		return fmt.Errorf("%s chain state: %w", operation, ErrNilContext)
	}
	if repository == nil || repository.db == nil {
		return fmt.Errorf("%s chain state: repository is not initialized", operation)
	}
	return nil
}

func scanChainState(scanner rowScanner) (ChainState, error) {
	var state ChainState
	var chainInstanceID, mode, headHash, buildOwner, lastBuiltHash sql.NullString
	var headID, protectedSinceID, lastBuiltID, lastBuiltSeq sql.NullInt64
	var genesisAt, buildLeaseUntil, updatedAt databaseTimestamp
	if err := scanner.Scan(
		&state.ChainID,
		&chainInstanceID,
		&state.Status,
		&mode,
		&state.HeadSeq,
		&headID,
		&headHash,
		&genesisAt,
		&protectedSinceID,
		&buildOwner,
		&buildLeaseUntil,
		&state.BuildEpoch,
		&lastBuiltID,
		&lastBuiltSeq,
		&lastBuiltHash,
		&updatedAt,
	); err != nil {
		return ChainState{}, err
	}
	var err error
	state.UpdatedAt, err = updatedAt.required("chain state updated_at")
	if err != nil {
		return ChainState{}, err
	}
	state.ChainInstanceID = stringPointer(chainInstanceID)
	state.Mode = stringPointer(mode)
	state.HeadID = int64Pointer(headID)
	state.HeadHash = stringPointer(headHash)
	state.GenesisAt = genesisAt.pointer()
	state.ProtectedSinceID = int64Pointer(protectedSinceID)
	state.BuildOwner = stringPointer(buildOwner)
	state.BuildLeaseUntil = buildLeaseUntil.pointer()
	state.LastBuiltID = int64Pointer(lastBuiltID)
	state.LastBuiltSeq = int64Pointer(lastBuiltSeq)
	state.LastBuiltHash = stringPointer(lastBuiltHash)
	return state, nil
}

func scanChainVerification(scanner rowScanner) (ChainVerification, error) {
	var verification ChainVerification
	var observedInstanceID, observedHeadHash, result, breakReason sql.NullString
	var lastVerifiedHeadSeq, breakSeq, breakID sql.NullInt64
	var lastVerifiedAt databaseTimestamp
	if err := scanner.Scan(
		&verification.ChainID,
		&observedInstanceID,
		&observedHeadHash,
		&result,
		&lastVerifiedHeadSeq,
		&lastVerifiedAt,
		&breakSeq,
		&breakID,
		&breakReason,
	); err != nil {
		return ChainVerification{}, err
	}
	verification.ObservedInstanceID = stringPointer(observedInstanceID)
	verification.ObservedHeadHash = stringPointer(observedHeadHash)
	verification.Result = stringPointer(result)
	verification.LastVerifiedHeadSeq = int64Pointer(lastVerifiedHeadSeq)
	verification.LastVerifiedAt = lastVerifiedAt.pointer()
	verification.BreakSeq = int64Pointer(breakSeq)
	verification.BreakID = int64Pointer(breakID)
	verification.BreakReason = stringPointer(breakReason)
	return verification, nil
}
