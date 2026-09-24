package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
)

var ErrApprovalNotPending = errors.New("approval is no longer pending")

// ErrApprovalReplayConflict indicates that a separated-store saga found an
// approval with the same ID but different immutable pending-request fields.
var ErrApprovalReplayConflict = errors.New("approval replay conflicts with persisted record")

// ApprovalRepository provides CRUD operations for approval records.
type ApprovalRepository struct {
	repositoryBase
	auditDB       *sql.DB
	auditDialect  Dialect
	auditSeparate bool
	auditKeys     ChainKeyProvider
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
	countQuery := "SELECT COUNT(*) FROM approvals" + where
	if err := repository.db.QueryRowContext(ctx, repository.bind(countQuery), args...).Scan(&total); err != nil {
		return ApprovalPage{}, fmt.Errorf("count approvals: %w", err)
	}
	query := `
SELECT id, audit_id, agent_id, sql_raw, reason, status, approver, decided_at,
       created_at, updated_at
FROM approvals` + where + `
ORDER BY created_at DESC, id DESC
LIMIT ? OFFSET ?`
	selectArgs := append(append([]any{}, args...), pageSize, (page-1)*pageSize)
	rows, err := repository.db.QueryContext(ctx, repository.bind(query), selectArgs...)
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
	if err := insertApproval(ctx, repository.db, repository.dialect, approval); err != nil {
		return model.Approval{}, err
	}
	created, err := repository.Get(ctx, approval.ID)
	if err != nil {
		return model.Approval{}, fmt.Errorf("read created approval %q: %w", approval.ID, err)
	}
	return created, nil
}

func insertApproval(ctx context.Context, executor sqlExecutor, dialect Dialect, approval model.Approval) error {
	query := `
INSERT INTO approvals (
  id, audit_id, agent_id, sql_raw, reason, status, approver, decided_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := executor.ExecContext(ctx, repositoryBase{dialect: dialect}.bind(query),
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
	return getApproval(ctx, repository.db, repository.dialect, id)
}

func getApproval(ctx context.Context, executor sqlExecutor, dialect Dialect, id string) (model.Approval, error) {
	query := `
SELECT id, audit_id, agent_id, sql_raw, reason, status, approver, decided_at,
       created_at, updated_at
FROM approvals
WHERE id = ?`
	approval, err := scanApproval(executor.QueryRowContext(
		ctx, repositoryBase{dialect: dialect}.bind(query), id,
	))
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

	matched, err := approvalCAS(ctx, tx, repository.dialect, `
UPDATE approvals
SET status = ?, approver = ?, decided_at = ?, reason = COALESCE(?, reason),
    updated_at = CURRENT_TIMESTAMP
WHERE id = ? AND status = 'pending'`,
		status, approver, decidedAt, optionalString(reason), id,
	)
	if err != nil {
		return model.Approval{}, fmt.Errorf("decide approval %q: %w", id, err)
	}
	if !matched {
		var exists int
		err = tx.QueryRowContext(
			ctx,
			repository.bind("SELECT 1 FROM approvals WHERE id = ?"),
			id,
		).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return model.Approval{}, fmt.Errorf("decide approval %q: %w", id, ErrNotFound)
		}
		if err != nil {
			return model.Approval{}, fmt.Errorf("decide approval %q: check existence: %w", id, err)
		}
		return model.Approval{}, fmt.Errorf("decide approval %q: %w", id, ErrApprovalNotPending)
	}
	updated, err = getApproval(ctx, tx, repository.dialect, id)
	if err != nil {
		return model.Approval{}, fmt.Errorf("read decided approval %q: %w", id, err)
	}
	if err = tx.Commit(); err != nil {
		return model.Approval{}, fmt.Errorf("decide approval %q: commit: %w", id, err)
	}
	committed = true
	return updated, nil
}

// CreatePendingWithAudit atomically inserts both records when metadata and
// audit share a database. With separate databases it writes the immutable
// audit record first, then inserts the linked approval in a metadata
// transaction; a metadata failure therefore leaves a valid orphan audit.
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
	if auditLog.Decision != "approve" || auditLog.ID < 0 {
		return model.Approval{}, model.AuditLog{}, fmt.Errorf("create pending approval: invalid audit input")
	}
	if repository.auditSeparate {
		if repository.auditDB == nil {
			return model.Approval{}, model.AuditLog{}, fmt.Errorf("create pending approval: audit repository is not initialized")
		}
		if approval.Approver != nil || approval.DecidedAt != nil {
			return model.Approval{}, model.AuditLog{}, fmt.Errorf("create pending approval: invalid approval input")
		}
		return repository.createPendingWithAuditSaga(ctx, approval, auditLog)
	}
	if auditLog.ID != 0 {
		return model.Approval{}, model.AuditLog{}, fmt.Errorf("create pending approval: invalid audit input")
	}
	return repository.createPendingWithAuditTransaction(ctx, approval, auditLog)
}

// createPendingWithAuditTransaction keeps the approval, chain-aware audit
// append, and approval link in one transaction. The chain-state lock is always
// acquired before either business row is written.
func (repository *ApprovalRepository) createPendingWithAuditTransaction(
	ctx context.Context,
	approval model.Approval,
	auditLog model.AuditLog,
) (created model.Approval, recorded model.AuditLog, err error) {
	auditRepository := &AuditLogRepository{
		repositoryBase: repository.repositoryBase,
		chainID:        "management",
		keys:           repository.auditKeys,
	}
	tx, err := auditRepository.beginChainTransaction(ctx)
	if err != nil {
		return model.Approval{}, model.AuditLog{}, fmt.Errorf("create pending approval %q: begin transaction: %w", approval.ID, err)
	}
	defer func() {
		if err == nil {
			return
		}
		if rollbackErr := tx.Rollback(ctx); rollbackErr != nil {
			err = errors.Join(err, fmt.Errorf("rollback pending approval %q: %w", approval.ID, rollbackErr))
			created = model.Approval{}
			recorded = model.AuditLog{}
		}
	}()
	if repository.dialect == DialectSQLite {
		if _, immediate := tx.(*sqliteChainTransaction); !immediate {
			return model.Approval{}, model.AuditLog{}, fmt.Errorf("create pending approval %q: SQLite transaction is not immediate", approval.ID)
		}
	}

	// Acquire the chain lock before approval/audit rows. chainLogsInTransaction
	// reads the already-locked row again after the approval insert so all chain
	// derivation still comes from transaction-local locked state.
	if _, _, err := auditRepository.lockChainStateIfPresent(ctx, tx); err != nil {
		return model.Approval{}, model.AuditLog{}, err
	}

	approval.AuditID = nil
	if err := insertApproval(ctx, tx, repository.dialect, approval); err != nil {
		return model.Approval{}, model.AuditLog{}, err
	}
	logs, err := auditRepository.chainLogsInTransaction(ctx, tx, []model.AuditLog{auditLog})
	if err != nil {
		return model.Approval{}, model.AuditLog{}, err
	}
	recorded = logs[0]
	matched, err := approvalCAS(ctx, tx, repository.dialect, `
UPDATE approvals
SET audit_id = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ? AND status = 'pending'`, recorded.ID, approval.ID)
	if err != nil {
		return model.Approval{}, model.AuditLog{}, fmt.Errorf("link approval %q to audit %d: %w", approval.ID, recorded.ID, err)
	}
	if !matched {
		return model.Approval{}, model.AuditLog{}, fmt.Errorf("link approval %q: affected 0 rows", approval.ID)
	}
	created, err = getApproval(ctx, tx, repository.dialect, approval.ID)
	if err != nil {
		return model.Approval{}, model.AuditLog{}, fmt.Errorf("read pending approval %q: %w", approval.ID, err)
	}
	recorded, err = getInsertedAuditLog(ctx, tx, repository.dialect, recorded.ID)
	if err != nil {
		return model.Approval{}, model.AuditLog{}, fmt.Errorf("read approval audit %d: %w", recorded.ID, err)
	}
	if strings.TrimSpace(created.ID) == "" || recorded.ID <= 0 {
		return model.Approval{}, model.AuditLog{}, fmt.Errorf("create pending approval %q: empty persisted identity", approval.ID)
	}
	if err = tx.Commit(ctx); err != nil {
		return model.Approval{}, model.AuditLog{}, fmt.Errorf("create pending approval %q: commit: %w", approval.ID, err)
	}
	return created, recorded, nil
}

func (repository *ApprovalRepository) createPendingWithAuditSaga(
	ctx context.Context,
	approval model.Approval,
	auditLog model.AuditLog,
) (model.Approval, model.AuditLog, error) {
	recorded, err := repository.recordOrReadApprovalAudit(ctx, auditLog)
	if err != nil {
		return model.Approval{}, model.AuditLog{}, fmt.Errorf("create pending approval %q: persist audit: %w", approval.ID, err)
	}

	approval.AuditID = &recorded.ID
	created, err := repository.insertPendingApprovalSaga(ctx, approval)
	if err != nil {
		// The approve audit is an immutable fact and deliberately remains in the
		// audit store as an orphan when the metadata step fails.
		return model.Approval{}, model.AuditLog{}, err
	}
	return created, recorded, nil
}

// recordOrReadApprovalAudit supports an in-process retry after the audit step:
// an ID of zero appends once, while a positive ID reuses and verifies the
// already-recorded immutable audit instead of inserting another row.
func (repository *ApprovalRepository) recordOrReadApprovalAudit(
	ctx context.Context,
	auditLog model.AuditLog,
) (model.AuditLog, error) {
	if auditLog.ID == 0 {
		auditRepository := &AuditLogRepository{
			repositoryBase: repositoryBase{db: repository.auditDB, dialect: repository.auditDialect},
			chainID:        "traffic",
			keys:           repository.auditKeys,
		}
		inserted, err := auditRepository.AppendBatch(ctx, []model.AuditLog{auditLog})
		if err != nil {
			return model.AuditLog{}, err
		}
		return inserted[0], nil
	}
	recorded, err := getInsertedAuditLog(ctx, repository.auditDB, repository.auditDialect, auditLog.ID)
	if err != nil {
		return model.AuditLog{}, err
	}
	if !sameAuditInsertIntent(auditLog, recorded) {
		return model.AuditLog{}, fmt.Errorf("reuse approval audit %d: persisted audit differs", auditLog.ID)
	}
	return recorded, nil
}

func (repository *ApprovalRepository) insertPendingApprovalSaga(
	ctx context.Context,
	expected model.Approval,
) (model.Approval, error) {
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Approval{}, fmt.Errorf("create pending approval %q: begin metadata transaction: %w", expected.ID, err)
	}

	if err := insertApproval(ctx, tx, repository.dialect, expected); err != nil {
		rollbackErr := rollbackApprovalSaga(tx, expected.ID)
		if replayed, replayErr := repository.readMatchingApprovalReplay(ctx, expected); replayErr == nil {
			return replayed, nil
		} else if errors.Is(replayErr, ErrApprovalReplayConflict) {
			return model.Approval{}, replayErr
		}
		return model.Approval{}, errors.Join(err, rollbackErr)
	}

	created, err := getApproval(ctx, tx, repository.dialect, expected.ID)
	if err != nil {
		return model.Approval{}, errors.Join(
			fmt.Errorf("read pending approval %q: %w", expected.ID, err),
			rollbackApprovalSaga(tx, expected.ID),
		)
	}
	if !samePendingApprovalReplay(expected, created) {
		return model.Approval{}, errors.Join(
			fmt.Errorf("create pending approval %q: %w", expected.ID, ErrApprovalReplayConflict),
			rollbackApprovalSaga(tx, expected.ID),
		)
	}
	if err := tx.Commit(); err != nil {
		// Commit may have reached PostgreSQL even when its acknowledgement was
		// lost. A read-back of the exact same request makes that retry safe.
		if replayed, replayErr := repository.readMatchingApprovalReplay(ctx, expected); replayErr == nil {
			return replayed, nil
		} else if errors.Is(replayErr, ErrApprovalReplayConflict) {
			return model.Approval{}, replayErr
		}
		return model.Approval{}, fmt.Errorf("create pending approval %q: commit metadata transaction: %w", expected.ID, err)
	}
	return created, nil
}

func (repository *ApprovalRepository) readMatchingApprovalReplay(
	ctx context.Context,
	expected model.Approval,
) (model.Approval, error) {
	stored, err := getApproval(ctx, repository.db, repository.dialect, expected.ID)
	if err != nil {
		return model.Approval{}, err
	}
	if !samePendingApprovalReplay(expected, stored) {
		return model.Approval{}, fmt.Errorf("create pending approval %q: %w", expected.ID, ErrApprovalReplayConflict)
	}
	return stored, nil
}

func rollbackApprovalSaga(tx *sql.Tx, approvalID string) error {
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return fmt.Errorf("rollback pending approval %q: %w", approvalID, err)
	}
	return nil
}

func samePendingApprovalReplay(expected, stored model.Approval) bool {
	return stored.ID == expected.ID &&
		equalInt64Pointers(stored.AuditID, expected.AuditID) &&
		stored.Status == "pending" &&
		equalStringPointers(stored.AgentID, expected.AgentID) &&
		equalStringPointers(stored.SQLRaw, expected.SQLRaw) &&
		equalStringPointers(stored.Reason, expected.Reason) &&
		stored.Approver == nil && stored.DecidedAt == nil
}

func sameAuditInsertIntent(expected, stored model.AuditLog) bool {
	expected.ID = stored.ID
	expected.TS = stored.TS
	return reflect.DeepEqual(expected, stored)
}

func equalInt64Pointers(left, right *int64) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func equalStringPointers(left, right *string) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

// Update replaces mutable approval fields and returns the stored record.
func (repository *ApprovalRepository) Update(ctx context.Context, approval model.Approval) (model.Approval, error) {
	result, err := repository.db.ExecContext(ctx, repository.bind(`
UPDATE approvals
SET audit_id = ?, agent_id = ?, sql_raw = ?, reason = ?, status = ?, approver = ?,
    decided_at = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?`),
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
	result, err := repository.db.ExecContext(ctx, repository.bind("DELETE FROM approvals WHERE id = ?"), id)
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
