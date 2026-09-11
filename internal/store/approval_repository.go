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

type ApprovalPage struct {
	Total    int64
	Page     int
	PageSize int
	List     []model.Approval
}

// ListPage returns approvals ordered newest first, optionally filtered by status.
func (repository *ApprovalRepository) ListPage(
	ctx context.Context,
	status string,
	page int,
	pageSize int,
) (ApprovalPage, error) {
	if ctx == nil {
		return ApprovalPage{}, fmt.Errorf("list approvals: %w", ErrNilContext)
	}
	if page < 1 {
		return ApprovalPage{}, fmt.Errorf("list approvals: %w", ErrInvalidPage)
	}
	if pageSize < 1 || pageSize > 100 {
		return ApprovalPage{}, fmt.Errorf("list approvals: %w", ErrInvalidPageSize)
	}
	where := ""
	args := make([]any, 0, 1)
	if status != "" {
		where = " WHERE status = ?"
		args = append(args, status)
	}
	var total int64
	if err := repository.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM approvals"+where, args...).Scan(&total); err != nil {
		return ApprovalPage{}, fmt.Errorf("count approvals: %w", err)
	}
	query := `
SELECT id, audit_id, agent_id, sql_raw, reason, status, approver, decided_at,
       created_at, updated_at
FROM approvals` + where + `
ORDER BY created_at DESC, id DESC
LIMIT ? OFFSET ?`
	selectArgs := append(append([]any{}, args...), pageSize, (page-1)*pageSize)
	rows, err := repository.db.QueryContext(ctx, query, selectArgs...)
	if err != nil {
		return ApprovalPage{}, fmt.Errorf("query approvals: %w", err)
	}
	list := make([]model.Approval, 0, pageSize)
	for rows.Next() {
		approval, err := scanApproval(rows)
		if err != nil {
			return ApprovalPage{}, fmt.Errorf("scan approvals: %w", closeRowsAfterError(rows, err))
		}
		list = append(list, approval)
	}
	iterationError := rows.Err()
	closeError := rows.Close()
	if iterationError != nil || closeError != nil {
		return ApprovalPage{}, fmt.Errorf("finish approvals: %w", errors.Join(iterationError, closeError))
	}
	return ApprovalPage{Total: total, Page: page, PageSize: pageSize, List: list}, nil
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
