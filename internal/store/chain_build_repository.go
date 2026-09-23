package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const defaultBuildLease = 2 * time.Minute

var (
	// ErrBuildLeaseActive indicates that another owner holds an unexpired build lease.
	ErrBuildLeaseActive = errors.New("audit chain build lease is active")
	// ErrStaleBuildFencing indicates that a build owner or epoch no longer owns the build.
	ErrStaleBuildFencing = errors.New("stale audit chain build fencing token")
	// ErrChainAlreadyActive indicates that an active chain cannot be rebuilt in place.
	ErrChainAlreadyActive = errors.New("audit chain is already active")
	// ErrBuildNotInProgress indicates that the chain is not currently being built.
	ErrBuildNotInProgress = errors.New("audit chain build is not in progress")
	// ErrInvalidBuildMode indicates that a requested chain mode is unsupported.
	ErrInvalidBuildMode = errors.New("invalid audit chain build mode")
)

// BuildRequest describes a request to begin or take over an audit-chain build.
type BuildRequest struct {
	Mode  string
	Owner string
	Lease time.Duration
}

// BuildLease is the fencing token and lease held by one build owner.
type BuildLease struct {
	ChainID    string
	InstanceID string
	Epoch      int
	Owner      string
	LeaseUntil time.Time
}

// ChainBuildRepository owns the write-only lifecycle operations for chain builds.
// ChainStateRepository intentionally remains read-only.
type ChainBuildRepository struct {
	repositoryBase
}

// BeginBuild starts a new chain instance or takes over an expired build lease.
func (repository *ChainBuildRepository) BeginBuild(
	ctx context.Context,
	chainID string,
	req BuildRequest,
) (BuildLease, error) {
	if err := repository.validate(ctx, "begin"); err != nil {
		return BuildLease{}, err
	}
	if req.Mode != "keyless" && req.Mode != "hmac" {
		return BuildLease{}, fmt.Errorf("begin chain build %q: mode %q: %w", chainID, req.Mode, ErrInvalidBuildMode)
	}
	if strings.TrimSpace(req.Owner) == "" {
		return BuildLease{}, fmt.Errorf("begin chain build %q: owner is required", chainID)
	}
	leaseDuration, err := normalizeBuildLease(req.Lease)
	if err != nil {
		return BuildLease{}, fmt.Errorf("begin chain build %q: %w", chainID, err)
	}

	return runChainBuildTransaction(ctx, repository, func(transaction chainTransaction) (BuildLease, error) {
		state, err := repository.lockState(ctx, transaction, chainID)
		if err != nil {
			return BuildLease{}, err
		}
		now := time.Now().UTC()
		switch state.Status {
		case "DISABLED":
			instanceID := uuid.NewString()
			leaseUntil := now.Add(leaseDuration)
			result, err := transaction.ExecContext(ctx, repository.bind(`
UPDATE chain_state
SET chain_instance_id = ?, status = 'BUILDING', mode = ?,
    head_seq = 0, head_id = NULL, head_hash = NULL,
    genesis_at = ?, build_owner = ?, build_lease_until = ?,
    build_epoch = build_epoch + 1,
    last_built_id = NULL, last_built_seq = NULL, last_built_hash = NULL,
    updated_at = ?
WHERE chain_id = ? AND status = 'DISABLED'`),
				instanceID, req.Mode, now, req.Owner, leaseUntil, now, chainID)
			if err != nil {
				return BuildLease{}, fmt.Errorf("initialize chain build %q: %w", chainID, err)
			}
			if err := requireSingleBuildRow(result, "initialize chain build"); err != nil {
				return BuildLease{}, err
			}
			return BuildLease{
				ChainID: chainID, InstanceID: instanceID, Epoch: state.BuildEpoch + 1,
				Owner: req.Owner, LeaseUntil: leaseUntil,
			}, nil

		case "BUILDING":
			if state.BuildLeaseUntil != nil && state.BuildLeaseUntil.After(now) {
				if state.BuildOwner != nil && *state.BuildOwner == req.Owner {
					return buildLeaseFromState(state)
				}
				return BuildLease{}, fmt.Errorf("begin chain build %q: %w", chainID, ErrBuildLeaseActive)
			}

			leaseUntil := now.Add(leaseDuration)
			result, err := transaction.ExecContext(ctx, repository.bind(`
UPDATE chain_state
SET build_owner = ?, build_lease_until = ?, build_epoch = build_epoch + 1,
    updated_at = ?
WHERE chain_id = ? AND status = 'BUILDING' AND build_epoch = ?`),
				req.Owner, leaseUntil, now, chainID, state.BuildEpoch)
			if err != nil {
				return BuildLease{}, fmt.Errorf("take over chain build %q: %w", chainID, err)
			}
			if err := requireSingleBuildRow(result, "take over chain build"); err != nil {
				return BuildLease{}, err
			}
			if state.ChainInstanceID == nil {
				return BuildLease{}, fmt.Errorf("take over chain build %q: chain_instance_id is empty", chainID)
			}
			return BuildLease{
				ChainID: chainID, InstanceID: *state.ChainInstanceID, Epoch: state.BuildEpoch + 1,
				Owner: req.Owner, LeaseUntil: leaseUntil,
			}, nil

		case "ACTIVE":
			return BuildLease{}, fmt.Errorf("begin chain build %q: %w", chainID, ErrChainAlreadyActive)
		case "FAILED":
			return BuildLease{}, fmt.Errorf("begin chain build %q: failed build must be reset first: %w", chainID, ErrBuildNotInProgress)
		default:
			return BuildLease{}, fmt.Errorf("begin chain build %q: status %q: %w", chainID, state.Status, ErrBuildNotInProgress)
		}
	})
}

// RenewLease extends the lease held by the matching owner and epoch.
func (repository *ChainBuildRepository) RenewLease(
	ctx context.Context,
	chainID, owner string,
	epoch int,
	lease time.Duration,
) (BuildLease, error) {
	if err := repository.validate(ctx, "renew lease"); err != nil {
		return BuildLease{}, err
	}
	if strings.TrimSpace(owner) == "" {
		return BuildLease{}, fmt.Errorf("renew chain build lease %q: owner is required", chainID)
	}
	leaseDuration, err := normalizeBuildLease(lease)
	if err != nil {
		return BuildLease{}, fmt.Errorf("renew chain build lease %q: %w", chainID, err)
	}

	return runChainBuildTransaction(ctx, repository, func(transaction chainTransaction) (BuildLease, error) {
		state, err := repository.lockState(ctx, transaction, chainID)
		if err != nil {
			return BuildLease{}, err
		}
		if err := VerifyFencing(state, owner, epoch); err != nil {
			return BuildLease{}, fmt.Errorf("renew chain build lease %q: %w", chainID, err)
		}
		if state.ChainInstanceID == nil {
			return BuildLease{}, fmt.Errorf("renew chain build lease %q: chain_instance_id is empty", chainID)
		}

		now := time.Now().UTC()
		leaseUntil := now.Add(leaseDuration)
		result, err := transaction.ExecContext(ctx, repository.bind(`
UPDATE chain_state
SET build_lease_until = ?, updated_at = ?
WHERE chain_id = ? AND status = 'BUILDING' AND build_owner = ? AND build_epoch = ?`),
			leaseUntil, now, chainID, owner, epoch)
		if err != nil {
			return BuildLease{}, fmt.Errorf("renew chain build lease %q: %w", chainID, err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return BuildLease{}, fmt.Errorf("read renewed chain build row count: %w", err)
		}
		if affected != 1 {
			return BuildLease{}, fmt.Errorf("renew chain build lease %q: %w", chainID, ErrStaleBuildFencing)
		}
		return BuildLease{
			ChainID: chainID, InstanceID: *state.ChainInstanceID, Epoch: epoch,
			Owner: owner, LeaseUntil: leaseUntil,
		}, nil
	})
}

// ResetBuild abandons a fenced build and removes all derived chain data so a
// later BeginBuild creates a new chain instance from the beginning.
func (repository *ChainBuildRepository) ResetBuild(
	ctx context.Context,
	chainID, owner string,
	epoch int,
) error {
	if err := repository.validate(ctx, "reset"); err != nil {
		return err
	}
	_, err := runChainBuildTransaction(ctx, repository, func(transaction chainTransaction) (BuildLease, error) {
		state, err := repository.lockState(ctx, transaction, chainID)
		if err != nil {
			return BuildLease{}, err
		}
		if err := VerifyFencing(state, owner, epoch); err != nil {
			return BuildLease{}, fmt.Errorf("reset chain build %q: %w", chainID, err)
		}

		now := time.Now().UTC()
		result, err := transaction.ExecContext(ctx, repository.bind(`
UPDATE chain_state
SET status = 'DISABLED', chain_instance_id = NULL, mode = NULL,
    head_seq = 0, head_id = NULL, head_hash = NULL, genesis_at = NULL,
    build_owner = NULL, build_lease_until = NULL,
    last_built_id = NULL, last_built_seq = NULL, last_built_hash = NULL,
    updated_at = ?
WHERE chain_id = ? AND status = 'BUILDING' AND build_owner = ? AND build_epoch = ?`),
			now, chainID, owner, epoch)
		if err != nil {
			return BuildLease{}, fmt.Errorf("reset chain build %q: %w", chainID, err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return BuildLease{}, fmt.Errorf("read reset chain build row count: %w", err)
		}
		if affected != 1 {
			return BuildLease{}, fmt.Errorf("reset chain build %q: %w", chainID, ErrStaleBuildFencing)
		}
		if _, err := transaction.ExecContext(ctx, `
UPDATE audit_logs
SET chain_seq = NULL, prev_hash = NULL, self_hash = NULL,
    chain_key_version = NULL, chain_format_version = NULL`); err != nil {
			return BuildLease{}, fmt.Errorf("clear audit log chain data for %q: %w", chainID, err)
		}
		return BuildLease{}, nil
	})
	return err
}

// VerifyFencing verifies that state belongs to the supplied in-progress build.
func VerifyFencing(state ChainState, owner string, epoch int) error {
	if state.Status != "BUILDING" || state.BuildOwner == nil || *state.BuildOwner != owner || state.BuildEpoch != epoch {
		return ErrStaleBuildFencing
	}
	return nil
}

func (repository *ChainBuildRepository) validate(ctx context.Context, operation string) error {
	if ctx == nil {
		return fmt.Errorf("%s chain build: %w", operation, ErrNilContext)
	}
	if repository == nil || repository.db == nil {
		return fmt.Errorf("%s chain build: repository is not initialized", operation)
	}
	return nil
}

func normalizeBuildLease(lease time.Duration) (time.Duration, error) {
	if lease == 0 {
		return defaultBuildLease, nil
	}
	if lease < 0 {
		return 0, fmt.Errorf("lease must be positive")
	}
	return lease, nil
}

func (repository *ChainBuildRepository) lockState(
	ctx context.Context,
	transaction chainTransaction,
	chainID string,
) (ChainState, error) {
	query := `
SELECT chain_id, chain_instance_id, status, mode, head_seq, head_id, head_hash,
       genesis_at, protected_since_id, build_owner, build_lease_until, build_epoch,
       last_built_id, last_built_seq, last_built_hash, updated_at
FROM chain_state
WHERE chain_id = ?`
	if repository.dialect == DialectPostgres {
		query += " FOR UPDATE"
	}
	state, err := scanChainState(transaction.QueryRowContext(ctx, repository.bind(query), chainID))
	if errors.Is(err, sql.ErrNoRows) {
		return ChainState{}, fmt.Errorf("lock chain state %q: %w", chainID, errors.Join(ErrNotFound, err))
	}
	if err != nil {
		return ChainState{}, fmt.Errorf("lock chain state %q: %w", chainID, err)
	}
	return state, nil
}

func buildLeaseFromState(state ChainState) (BuildLease, error) {
	if state.ChainInstanceID == nil || state.BuildOwner == nil || state.BuildLeaseUntil == nil {
		return BuildLease{}, fmt.Errorf("chain build %q has incomplete lease state", state.ChainID)
	}
	return BuildLease{
		ChainID: state.ChainID, InstanceID: *state.ChainInstanceID, Epoch: state.BuildEpoch,
		Owner: *state.BuildOwner, LeaseUntil: *state.BuildLeaseUntil,
	}, nil
}

func requireSingleBuildRow(result sql.Result, operation string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read %s row count: %w", operation, err)
	}
	if affected != 1 {
		return fmt.Errorf("%s affected %d rows: %w", operation, affected, ErrStaleBuildFencing)
	}
	return nil
}

func runChainBuildTransaction(
	ctx context.Context,
	repository *ChainBuildRepository,
	operation func(chainTransaction) (BuildLease, error),
) (BuildLease, error) {
	deadline := time.Now().Add(chainAppendRetryBudget)
	backoff := chainAppendMinBackoff
	for {
		lease, err := repository.runChainBuildTransactionAttempt(ctx, operation)
		if err == nil {
			return lease, nil
		}
		appendRepository := &AuditLogRepository{repositoryBase: repository.repositoryBase}
		if !appendRepository.retryableChainAppend(err) || time.Now().After(deadline) {
			return BuildLease{}, err
		}
		if err := waitChainAppendRetry(ctx, backoff); err != nil {
			return BuildLease{}, err
		}
		if backoff < chainAppendMaxBackoff {
			backoff *= 2
			if backoff > chainAppendMaxBackoff {
				backoff = chainAppendMaxBackoff
			}
		}
	}
}

func (repository *ChainBuildRepository) runChainBuildTransactionAttempt(
	ctx context.Context,
	operation func(chainTransaction) (BuildLease, error),
) (lease BuildLease, resultErr error) {
	appendRepository := &AuditLogRepository{repositoryBase: repository.repositoryBase}
	transaction, err := appendRepository.beginChainTransaction(ctx)
	if err != nil {
		return BuildLease{}, fmt.Errorf("begin chain build transaction: %w", err)
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, transaction.Rollback(ctx))
		}
	}()

	lease, resultErr = operation(transaction)
	if resultErr != nil {
		return BuildLease{}, resultErr
	}
	if resultErr = transaction.Commit(ctx); resultErr != nil {
		return BuildLease{}, fmt.Errorf("commit chain build transaction: %w", resultErr)
	}
	return lease, nil
}
