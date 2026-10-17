package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
)

// ClaimApproved consumes a sealed approval exactly once. A claimed request
// stays claimed across process restarts and is never made executable again.
func (repository *ApprovalRepository) ClaimApproved(
	ctx context.Context, id, agentID string, now time.Time,
) (claimed model.Approval, err error) {
	return repository.claimApproved(ctx, id, agentID, "", now)
}

// ClaimApprovedForRequester atomically checks the delegated human owner.
// A mismatch leaves the approval untouched and therefore cannot consume it.
func (repository *ApprovalRepository) ClaimApprovedForRequester(
	ctx context.Context, id, agentID, requesterUserID string, now time.Time,
) (model.Approval, error) {
	if strings.TrimSpace(requesterUserID) == "" {
		return model.Approval{}, ErrApprovalNotExecutable
	}
	return repository.claimApproved(ctx, id, agentID, requesterUserID, now)
}

func (repository *ApprovalRepository) claimApproved(
	ctx context.Context, id, agentID, requesterUserID string, now time.Time,
) (claimed model.Approval, err error) {
	if repository == nil || repository.db == nil || ctx == nil ||
		strings.TrimSpace(id) == "" || strings.TrimSpace(agentID) == "" || now.IsZero() {
		return model.Approval{}, ErrApprovalNotExecutable
	}
	tenantID, err := repository.requireTenant(ctx, "claim approval")
	if err != nil {
		return model.Approval{}, err
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Approval{}, fmt.Errorf("claim approval: begin transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
				err = errors.Join(err, rollbackErr)
				claimed = model.Approval{}
			}
		}
	}()
	ownerClause := " AND requester_user_id IS NULL"
	args := []any{now, id, tenantID, agentID, now}
	if requesterUserID != "" {
		ownerClause = " AND requester_user_id=?"
		args = append(args, requesterUserID)
	}
	matched, err := approvalCAS(ctx, tx, repository.dialect, `
UPDATE approvals SET status='claimed', claimed_at=?, updated_at=CURRENT_TIMESTAMP
WHERE id=? AND tenant_id=? AND agent_id=? AND status='approved'
  AND seal IS NOT NULL AND sql_raw IS NOT NULL AND sql_norm IS NOT NULL
  AND datasource_id IS NOT NULL AND expires_at>?`+ownerClause, args...)
	if err != nil {
		return model.Approval{}, fmt.Errorf("claim approval: %w", err)
	}
	if !matched {
		return model.Approval{}, ErrApprovalNotExecutable
	}
	claimed, err = getApproval(ctx, tx, repository.dialect, tenantID, id)
	if err != nil {
		return model.Approval{}, fmt.Errorf("claim approval: read claimed row: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return model.Approval{}, fmt.Errorf("claim approval: commit: %w", err)
	}
	committed = true
	return claimed, nil
}

// CompleteClaim records the final outcome after one claimed execution. A
// failed persistence attempt leaves claimed as a manual-review state.
func (repository *ApprovalRepository) CompleteClaim(
	ctx context.Context, id, agentID, status string, auditID *int64, outcomeCode *string, now time.Time,
) (model.Approval, error) {
	if repository == nil || repository.db == nil || ctx == nil ||
		strings.TrimSpace(id) == "" || strings.TrimSpace(agentID) == "" || now.IsZero() ||
		(status != "completed" && status != "failed" && status != "unknown") {
		return model.Approval{}, ErrApprovalNotExecutable
	}
	tenantID, err := repository.requireTenant(ctx, "complete approval")
	if err != nil {
		return model.Approval{}, err
	}
	matched, err := approvalCAS(ctx, repository.db, repository.dialect, `
UPDATE approvals SET status=?, finished_at=?, execution_audit_id=?, outcome_code=?,
    updated_at=CURRENT_TIMESTAMP
WHERE id=? AND tenant_id=? AND agent_id=? AND status='claimed'`,
		status, now, optionalInt64(auditID), optionalString(outcomeCode), id, tenantID, agentID)
	if err != nil {
		return model.Approval{}, fmt.Errorf("complete approval: %w", err)
	}
	if !matched {
		return model.Approval{}, ErrApprovalNotExecutable
	}
	return repository.Get(ctx, id)
}
