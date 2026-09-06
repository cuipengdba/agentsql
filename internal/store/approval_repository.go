package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/cuipengdba/agentsql/internal/model"
)

// ApprovalRepository provides CRUD operations for approval records.
type ApprovalRepository struct {
	db *sql.DB
}

// Create inserts an approval and returns the stored record.
func (repository *ApprovalRepository) Create(ctx context.Context, approval model.Approval) (model.Approval, error) {
	_, err := repository.db.ExecContext(ctx, `
INSERT INTO approvals (
  id, audit_id, agent_id, sql_raw, reason, status, approver, decided_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		approval.ID,
		optionalInt64(approval.AuditID),
		optionalString(approval.AgentID),
		optionalString(approval.SQLRaw),
		optionalString(approval.Reason),
		approval.Status,
		optionalString(approval.Approver),
		optionalTime(approval.DecidedAt),
	)
	if err != nil {
		return model.Approval{}, fmt.Errorf("create approval %q: %w", approval.ID, err)
	}
	created, err := repository.Get(ctx, approval.ID)
	if err != nil {
		return model.Approval{}, fmt.Errorf("read created approval %q: %w", approval.ID, err)
	}
	return created, nil
}

// Get returns an approval by ID.
func (repository *ApprovalRepository) Get(ctx context.Context, id string) (model.Approval, error) {
	approval, err := scanApproval(repository.db.QueryRowContext(ctx, `
SELECT id, audit_id, agent_id, sql_raw, reason, status, approver, decided_at,
       created_at, updated_at
FROM approvals
WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Approval{}, fmt.Errorf("get approval %q: %w", id, errors.Join(ErrNotFound, err))
	}
	if err != nil {
		return model.Approval{}, fmt.Errorf("get approval %q: %w", id, err)
	}
	return approval, nil
}

// Update replaces mutable approval fields and returns the stored record.
func (repository *ApprovalRepository) Update(ctx context.Context, approval model.Approval) (model.Approval, error) {
	result, err := repository.db.ExecContext(ctx, `
UPDATE approvals
SET audit_id = ?, agent_id = ?, sql_raw = ?, reason = ?, status = ?, approver = ?,
    decided_at = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?`,
		optionalInt64(approval.AuditID),
		optionalString(approval.AgentID),
		optionalString(approval.SQLRaw),
		optionalString(approval.Reason),
		approval.Status,
		optionalString(approval.Approver),
		optionalTime(approval.DecidedAt),
		approval.ID,
	)
	if err != nil {
		return model.Approval{}, fmt.Errorf("update approval %q: %w", approval.ID, err)
	}
	if err := checkRowsAffected(result, "approval", approval.ID); err != nil {
		return model.Approval{}, fmt.Errorf("update approval %q: %w", approval.ID, err)
	}
	updated, err := repository.Get(ctx, approval.ID)
	if err != nil {
		return model.Approval{}, fmt.Errorf("read updated approval %q: %w", approval.ID, err)
	}
	return updated, nil
}

// Delete removes an approval by ID.
func (repository *ApprovalRepository) Delete(ctx context.Context, id string) error {
	result, err := repository.db.ExecContext(ctx, "DELETE FROM approvals WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete approval %q: %w", id, err)
	}
	if err := checkRowsAffected(result, "approval", id); err != nil {
		return fmt.Errorf("delete approval %q: %w", id, err)
	}
	return nil
}

func scanApproval(scanner rowScanner) (model.Approval, error) {
	var approval model.Approval
	var auditID sql.NullInt64
	var agentID, sqlRaw, reason, approver sql.NullString
	var decidedAt, createdAt, updatedAt databaseTimestamp
	if err := scanner.Scan(
		&approval.ID,
		&auditID,
		&agentID,
		&sqlRaw,
		&reason,
		&approval.Status,
		&approver,
		&decidedAt,
		&createdAt,
		&updatedAt,
	); err != nil {
		return model.Approval{}, fmt.Errorf("scan approval: %w", err)
	}

	approval.AuditID = int64Pointer(auditID)
	approval.AgentID = stringPointer(agentID)
	approval.SQLRaw = stringPointer(sqlRaw)
	approval.Reason = stringPointer(reason)
	approval.Approver = stringPointer(approver)
	approval.DecidedAt = decidedAt.pointer()
	var err error
	approval.CreatedAt, err = createdAt.required("approvals.created_at")
	if err != nil {
		return model.Approval{}, fmt.Errorf("scan approval: %w", err)
	}
	approval.UpdatedAt, err = updatedAt.required("approvals.updated_at")
	if err != nil {
		return model.Approval{}, fmt.Errorf("scan approval: %w", err)
	}
	return approval, nil
}
