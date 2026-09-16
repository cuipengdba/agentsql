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

var ErrApprovalNotPending = errors.New("approval is no longer pending")

type approvalExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

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
	if repository == nil || repository.db == nil {
		return model.Approval{}, fmt.Errorf("create approval: repository is not initialized")
	}
	if ctx == nil {
		return model.Approval{}, fmt.Errorf("create approval: %w", ErrNilContext)
	}
	if err := insertApproval(ctx, repository.db, approval); err != nil {
		return model.Approval{}, err
	}
	created, err := repository.Get(ctx, approval.ID)
	if err != nil {
		return model.Approval{}, fmt.Errorf("read created approval %q: %w", approval.ID, err)
	}
	return created, nil
}

func insertApproval(ctx context.Context, executor approvalExecutor, approval model.Approval) error {
	_, err := executor.ExecContext(ctx, `
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
		return fmt.Errorf("create approval %q: %w", approval.ID, err)
	}
	return nil
}

// Get returns an approval by ID.
func (repository *ApprovalRepository) Get(ctx context.Context, id string) (model.Approval, error) {
	return getApproval(ctx, repository.db, id)
}

func getApproval(ctx context.Context, executor approvalExecutor, id string) (model.Approval, error) {
	approval, err := scanApproval(executor.QueryRowContext(ctx, `
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

// DecidePending atomically transitions one pending approval and returns the
// committed record. A missing approval and a completed approval are distinct.
func (repository *ApprovalRepository) DecidePending(
	ctx context.Context,
	id string,
	status string,
	approver string,
	reason *string,
	decidedAt time.Time,
) (updated model.Approval, err error) {
	if repository == nil || repository.db == nil {
		return model.Approval{}, fmt.Errorf("decide approval: repository is not initialized")
	}
	if ctx == nil {
		return model.Approval{}, fmt.Errorf("decide approval: %w", ErrNilContext)
	}
	if strings.TrimSpace(id) == "" || (status != "approved" && status != "rejected") ||
		strings.TrimSpace(approver) == "" || decidedAt.IsZero() {
		return model.Approval{}, fmt.Errorf("decide approval: invalid decision input")
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Approval{}, fmt.Errorf("decide approval %q: begin transaction: %w", id, err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("rollback approval decision %q: %w", id, rollbackErr))
			updated = model.Approval{}
		}
	}()

	result, err := tx.ExecContext(ctx, `
UPDATE approvals
SET status = ?, approver = ?, decided_at = ?, reason = COALESCE(?, reason),
    updated_at = CURRENT_TIMESTAMP
WHERE id = ? AND status = 'pending'`,
		status, approver, decidedAt, optionalString(reason), id,
	)
	if err != nil {
		return model.Approval{}, fmt.Errorf("decide approval %q: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return model.Approval{}, fmt.Errorf("decide approval %q: read affected rows: %w", id, err)
	}
	if affected == 0 {
		var exists int
		err = tx.QueryRowContext(ctx, "SELECT 1 FROM approvals WHERE id = ?", id).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return model.Approval{}, fmt.Errorf("decide approval %q: %w", id, ErrNotFound)
		}
		if err != nil {
			return model.Approval{}, fmt.Errorf("decide approval %q: check existence: %w", id, err)
		}
		return model.Approval{}, fmt.Errorf("decide approval %q: %w", id, ErrApprovalNotPending)
	}
	if affected != 1 {
		return model.Approval{}, fmt.Errorf("decide approval %q: affected %d rows", id, affected)
	}
	updated, err = getApproval(ctx, tx, id)
	if err != nil {
		return model.Approval{}, fmt.Errorf("read decided approval %q: %w", id, err)
	}
	if err = tx.Commit(); err != nil {
		return model.Approval{}, fmt.Errorf("decide approval %q: commit: %w", id, err)
	}
	committed = true
	return updated, nil
}

// CreatePendingWithAudit atomically inserts a pending approval, its approve
// audit event, and the approval-to-audit link.
func (repository *ApprovalRepository) CreatePendingWithAudit(
	ctx context.Context,
	approval model.Approval,
	auditLog model.AuditLog,
) (created model.Approval, recorded model.AuditLog, err error) {
	if repository == nil || repository.db == nil {
		return model.Approval{}, model.AuditLog{}, fmt.Errorf("create pending approval: repository is not initialized")
	}
	if ctx == nil {
		return model.Approval{}, model.AuditLog{}, fmt.Errorf("create pending approval: %w", ErrNilContext)
	}
	if strings.TrimSpace(approval.ID) == "" || approval.Status != "pending" || approval.AuditID != nil {
		return model.Approval{}, model.AuditLog{}, fmt.Errorf("create pending approval: invalid approval input")
	}
	if err := validateAuditLogInsert(ctx, auditLog); err != nil {
		return model.Approval{}, model.AuditLog{}, fmt.Errorf("create pending approval: %w", err)
	}
	if auditLog.Decision != "approve" || auditLog.ID != 0 {
		return model.Approval{}, model.AuditLog{}, fmt.Errorf("create pending approval: invalid audit input")
	}

	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Approval{}, model.AuditLog{}, fmt.Errorf("create pending approval %q: begin transaction: %w", approval.ID, err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("rollback pending approval %q: %w", approval.ID, rollbackErr))
			created = model.Approval{}
			recorded = model.AuditLog{}
		}
	}()

	approval.AuditID = nil
	if err := insertApproval(ctx, tx, approval); err != nil {
		return model.Approval{}, model.AuditLog{}, err
	}
	recorded, err = insertAuditLog(ctx, tx, auditLog)
	if err != nil {
		return model.Approval{}, model.AuditLog{}, err
	}
	result, err := tx.ExecContext(ctx, `
UPDATE approvals
SET audit_id = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ? AND status = 'pending'`, recorded.ID, approval.ID)
	if err != nil {
		return model.Approval{}, model.AuditLog{}, fmt.Errorf("link approval %q to audit %d: %w", approval.ID, recorded.ID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return model.Approval{}, model.AuditLog{}, fmt.Errorf("link approval %q: read affected rows: %w", approval.ID, err)
	}
	if affected != 1 {
		return model.Approval{}, model.AuditLog{}, fmt.Errorf("link approval %q: affected %d rows", approval.ID, affected)
	}
	created, err = getApproval(ctx, tx, approval.ID)
	if err != nil {
		return model.Approval{}, model.AuditLog{}, fmt.Errorf("read pending approval %q: %w", approval.ID, err)
	}
	recorded, err = getInsertedAuditLog(ctx, tx, recorded.ID)
	if err != nil {
		return model.Approval{}, model.AuditLog{}, fmt.Errorf("read approval audit %d: %w", recorded.ID, err)
	}
	if strings.TrimSpace(created.ID) == "" || recorded.ID <= 0 {
		return model.Approval{}, model.AuditLog{}, fmt.Errorf("create pending approval %q: empty persisted identity", approval.ID)
	}
	if err = tx.Commit(); err != nil {
		return model.Approval{}, model.AuditLog{}, fmt.Errorf("create pending approval %q: commit: %w", approval.ID, err)
	}
	committed = true
	return created, recorded, nil
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
