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

const (
	// DemoSeedSessionPrefix is reserved exclusively for Live Demo seed data.
	// Only the demo-seed command may call SeedHistoricalAudits, and only after
	// verifying config.Demo.Enabled=true. Production paths must never use it.
	DemoSeedSessionPrefix = "demo-seed:v1:"
)

var (
	// ErrDemoSeedUnsafeDatabase means the audit target contains non-demo data.
	ErrDemoSeedUnsafeDatabase = errors.New("demo historical seed refused: audit store contains non-demo data")
	// ErrDemoSeedConflict means immutable seed data differs or only part of a
	// requested audit batch already exists.
	ErrDemoSeedConflict = errors.New("demo historical seed conflicts with persisted data")
)

// SeedHistoricalAudits is exclusively for Live Demo seed data. The caller
// must be the demo-seed command and must first verify config.Demo.Enabled=true;
// production code must never call this historical-write entry point.
//
// Approvals correspond, in order, to the approve-decision audits in audits.
// Their AuditID fields must be nil; this method fills the database-allocated
// audit identities and returns the persisted audit and approval records.
func (store *Store) SeedHistoricalAudits(
	ctx context.Context,
	audits []model.AuditLog,
	approvals []model.Approval,
) (recorded []model.AuditLog, persistedApprovals []model.Approval, err error) {
	if ctx == nil {
		return nil, nil, fmt.Errorf("seed demo historical audits: %w", ErrNilContext)
	}
	if store == nil || store.metaDB == nil || store.auditDB == nil {
		return nil, nil, fmt.Errorf("seed demo historical audits: store is not initialized")
	}
	if !store.auditSeparate && store.metaDB != store.auditDB {
		return nil, nil, fmt.Errorf("seed demo historical audits: invalid combined store layout")
	}

	if store.auditSeparate {
		recorded, err = store.seedDemoAuditsTransaction(ctx, audits, approvals)
		if err != nil {
			return nil, nil, err
		}
		persistedApprovals, err = seedDemoApprovalsTransaction(
			ctx, store.metaDB, store.metaDriver, audits, approvals, recorded,
		)
		if err != nil {
			return nil, nil, err
		}
		return recorded, persistedApprovals, nil
	}

	tx, err := store.metaDB.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("seed demo historical audits: begin transaction: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("seed demo historical audits: rollback: %w", rollbackErr))
			recorded = nil
			persistedApprovals = nil
		}
	}()

	if err = prepareDemoAuditSeed(ctx, tx, store.auditDriver, audits, approvals); err != nil {
		return nil, nil, err
	}
	recorded, err = seedDemoAudits(ctx, tx, store.auditDriver, audits)
	if err != nil {
		return nil, nil, err
	}
	persistedApprovals, err = seedDemoApprovals(
		ctx, tx, store.metaDriver, audits, approvals, recorded,
	)
	if err != nil {
		return nil, nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("seed demo historical audits: commit transaction: %w", err)
	}
	committed = true
	return recorded, persistedApprovals, nil
}

func (store *Store) seedDemoAuditsTransaction(
	ctx context.Context,
	audits []model.AuditLog,
	approvals []model.Approval,
) (recorded []model.AuditLog, err error) {
	tx, err := store.auditDB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("seed demo historical audits: begin audit transaction: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("seed demo historical audits: rollback audit transaction: %w", rollbackErr))
			recorded = nil
		}
	}()

	if err = prepareDemoAuditSeed(ctx, tx, store.auditDriver, audits, approvals); err != nil {
		return nil, err
	}
	recorded, err = seedDemoAudits(ctx, tx, store.auditDriver, audits)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("seed demo historical audits: commit audit transaction: %w", err)
	}
	committed = true
	return recorded, nil
}

func prepareDemoAuditSeed(
	ctx context.Context,
	executor sqlExecutor,
	dialect Dialect,
	audits []model.AuditLog,
	approvals []model.Approval,
) error {
	if dialect == DialectPostgres {
		if _, err := executor.ExecContext(ctx, "LOCK TABLE audit_logs IN SHARE ROW EXCLUSIVE MODE"); err != nil {
			return fmt.Errorf("seed demo historical audits: lock audit table: %w", err)
		}
	}
	var unsafeCount int64
	err := executor.QueryRowContext(ctx, repositoryBase{dialect: dialect}.bind(`
SELECT COUNT(*)
FROM audit_logs
WHERE session_id IS NULL OR substr(session_id, 1, ?) <> ?`),
		len(DemoSeedSessionPrefix), DemoSeedSessionPrefix,
	).Scan(&unsafeCount)
	if err != nil {
		return fmt.Errorf("seed demo historical audits: check demo-only guard: %w", err)
	}
	if unsafeCount != 0 {
		return fmt.Errorf("%w (%d row(s))", ErrDemoSeedUnsafeDatabase, unsafeCount)
	}
	return validateDemoHistoricalSeed(ctx, audits, approvals)
}

func validateDemoHistoricalSeed(
	ctx context.Context,
	audits []model.AuditLog,
	approvals []model.Approval,
) error {
	if len(audits) == 0 {
		return fmt.Errorf("seed demo historical audits: audit batch is empty")
	}
	seenSessions := make(map[string]struct{}, len(audits))
	approveCount := 0
	for index, auditLog := range audits {
		if err := validateAuditLogInsert(ctx, auditLog); err != nil {
			return fmt.Errorf("seed demo historical audits: audit %d: %w", index, err)
		}
		if auditLog.ID != 0 || auditLog.TS.IsZero() || auditLog.SessionID == nil ||
			!strings.HasPrefix(*auditLog.SessionID, DemoSeedSessionPrefix) ||
			len(*auditLog.SessionID) == len(DemoSeedSessionPrefix) {
			return fmt.Errorf("seed demo historical audits: audit %d has invalid identity, timestamp, or session_id", index)
		}
		if _, duplicate := seenSessions[*auditLog.SessionID]; duplicate {
			return fmt.Errorf("seed demo historical audits: duplicate session_id %q", *auditLog.SessionID)
		}
		seenSessions[*auditLog.SessionID] = struct{}{}
		if auditLog.Decision == "approve" {
			approveCount++
		}
	}
	if len(approvals) != approveCount {
		return fmt.Errorf(
			"seed demo historical audits: got %d approvals for %d approve audits",
			len(approvals), approveCount,
		)
	}
	seenApprovalIDs := make(map[string]struct{}, len(approvals))
	for index, approval := range approvals {
		if strings.TrimSpace(approval.ID) == "" || approval.AuditID != nil {
			return fmt.Errorf("seed demo historical audits: approval %d has invalid id or audit_id", index)
		}
		switch approval.Status {
		case "pending", "approved", "rejected", "expired":
		default:
			return fmt.Errorf("seed demo historical audits: approval %d has invalid status %q", index, approval.Status)
		}
		if _, duplicate := seenApprovalIDs[approval.ID]; duplicate {
			return fmt.Errorf("seed demo historical audits: duplicate approval id %q", approval.ID)
		}
		seenApprovalIDs[approval.ID] = struct{}{}
	}
	return nil
}

func seedDemoAudits(
	ctx context.Context,
	executor sqlExecutor,
	dialect Dialect,
	audits []model.AuditLog,
) ([]model.AuditLog, error) {
	recorded := make([]model.AuditLog, len(audits))
	existing := 0
	for index, expected := range audits {
		stored, found, err := demoAuditBySession(ctx, executor, dialect, *expected.SessionID)
		if err != nil {
			return nil, err
		}
		if found {
			existing++
			recorded[index] = stored
		}
	}
	if existing != 0 && existing != len(audits) {
		return nil, fmt.Errorf("%w: only %d of %d audit rows exist", ErrDemoSeedConflict, existing, len(audits))
	}
	if existing == len(audits) {
		for index := range audits {
			if !sameHistoricalAuditIntent(audits[index], recorded[index]) {
				return nil, fmt.Errorf("%w: audit session_id %q differs", ErrDemoSeedConflict, *audits[index].SessionID)
			}
		}
		return recorded, nil
	}

	for index, auditLog := range audits {
		inserted, err := insertHistoricalAuditLog(ctx, executor, dialect, auditLog)
		if err != nil {
			return nil, fmt.Errorf("seed demo historical audits: insert audit %d: %w", index, err)
		}
		if !auditLog.TS.Equal(inserted.TS) {
			return nil, fmt.Errorf("seed demo historical audits: timestamp changed for session_id %q", *auditLog.SessionID)
		}
		recorded[index] = inserted
	}
	return recorded, nil
}

func demoAuditBySession(
	ctx context.Context,
	executor sqlExecutor,
	dialect Dialect,
	sessionID string,
) (model.AuditLog, bool, error) {
	rows, err := executor.QueryContext(ctx, repositoryBase{dialect: dialect}.bind(`
SELECT id, ts, agent_id, datasource_id, session_id, conversation_id, mcp_tool,
       db_type, sql_raw, sql_norm, stmt_type, objects, decision, rule_hits,
       risk_level, est_rows, rows_returned, latency_ms, client_ip, model_name,
       error_msg, error_code, action, actor_type, actor_id, details_json
FROM audit_logs
WHERE session_id = ?
ORDER BY id`), sessionID)
	if err != nil {
		return model.AuditLog{}, false, fmt.Errorf("seed demo historical audits: read session_id %q: %w", sessionID, err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return model.AuditLog{}, false, fmt.Errorf("seed demo historical audits: read session_id %q: %w", sessionID, err)
		}
		return model.AuditLog{}, false, nil
	}
	stored, err := scanAuditLog(rows)
	if err != nil {
		return model.AuditLog{}, false, fmt.Errorf("seed demo historical audits: scan session_id %q: %w", sessionID, err)
	}
	if rows.Next() {
		return model.AuditLog{}, false, fmt.Errorf("%w: duplicate persisted session_id %q", ErrDemoSeedConflict, sessionID)
	}
	if err := rows.Err(); err != nil {
		return model.AuditLog{}, false, fmt.Errorf("seed demo historical audits: finish session_id %q: %w", sessionID, err)
	}
	return stored, true, nil
}

func sameHistoricalAuditIntent(expected, stored model.AuditLog) bool {
	expected.ID = stored.ID
	expectedTimestamp := expected.TS
	expected.TS = stored.TS
	return expectedTimestamp.Equal(stored.TS) && reflect.DeepEqual(expected, stored)
}

func seedDemoApprovalsTransaction(
	ctx context.Context,
	database *sql.DB,
	dialect Dialect,
	audits []model.AuditLog,
	approvals []model.Approval,
	recorded []model.AuditLog,
) (persisted []model.Approval, err error) {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("seed demo historical audits: begin approval transaction: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("seed demo historical audits: rollback approval transaction: %w", rollbackErr))
			persisted = nil
		}
	}()
	if dialect == DialectPostgres {
		if _, err = tx.ExecContext(ctx, "LOCK TABLE approvals IN SHARE ROW EXCLUSIVE MODE"); err != nil {
			return nil, fmt.Errorf("seed demo historical audits: lock approval table: %w", err)
		}
	}
	persisted, err = seedDemoApprovals(ctx, tx, dialect, audits, approvals, recorded)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("seed demo historical audits: commit approval transaction: %w", err)
	}
	committed = true
	return persisted, nil
}

func seedDemoApprovals(
	ctx context.Context,
	executor sqlExecutor,
	dialect Dialect,
	audits []model.AuditLog,
	approvals []model.Approval,
	recorded []model.AuditLog,
) ([]model.Approval, error) {
	expected := make([]model.Approval, 0, len(approvals))
	approvalIndex := 0
	for auditIndex, auditLog := range audits {
		if auditLog.Decision != "approve" {
			continue
		}
		approval := approvals[approvalIndex]
		approval.AuditID = &recorded[auditIndex].ID
		expected = append(expected, approval)
		approvalIndex++
	}

	persisted := make([]model.Approval, len(expected))
	missing := make([]bool, len(expected))
	for index, approval := range expected {
		stored, found, err := demoApprovalByAuditID(ctx, executor, dialect, *approval.AuditID)
		if err != nil {
			return nil, err
		}
		if !found {
			missing[index] = true
			continue
		}
		if !sameDemoApprovalIntent(approval, stored) {
			return nil, fmt.Errorf("%w: approval for audit_id %d differs", ErrDemoSeedConflict, *approval.AuditID)
		}
		persisted[index] = stored
	}

	for index, approval := range expected {
		if !missing[index] {
			continue
		}
		if err := insertApproval(ctx, executor, dialect, approval); err != nil {
			return nil, fmt.Errorf("seed demo historical audits: insert approval %q: %w", approval.ID, err)
		}
		stored, err := getApproval(ctx, executor, dialect, approval.ID)
		if err != nil {
			return nil, fmt.Errorf("seed demo historical audits: read approval %q: %w", approval.ID, err)
		}
		if !sameDemoApprovalIntent(approval, stored) {
			return nil, fmt.Errorf("%w: inserted approval %q differs", ErrDemoSeedConflict, approval.ID)
		}
		persisted[index] = stored
	}
	return persisted, nil
}

func demoApprovalByAuditID(
	ctx context.Context,
	executor sqlExecutor,
	dialect Dialect,
	auditID int64,
) (model.Approval, bool, error) {
	rows, err := executor.QueryContext(ctx, repositoryBase{dialect: dialect}.bind(`
SELECT id, audit_id, agent_id, sql_raw, reason, status, approver, decided_at,
       created_at, updated_at
FROM approvals
WHERE audit_id = ?
ORDER BY id`), auditID)
	if err != nil {
		return model.Approval{}, false, fmt.Errorf("seed demo historical audits: read approval for audit_id %d: %w", auditID, err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return model.Approval{}, false, fmt.Errorf("seed demo historical audits: read approval for audit_id %d: %w", auditID, err)
		}
		return model.Approval{}, false, nil
	}
	stored, err := scanApproval(rows)
	if err != nil {
		return model.Approval{}, false, fmt.Errorf("seed demo historical audits: scan approval for audit_id %d: %w", auditID, err)
	}
	if rows.Next() {
		return model.Approval{}, false, fmt.Errorf("%w: duplicate approvals for audit_id %d", ErrDemoSeedConflict, auditID)
	}
	if err := rows.Err(); err != nil {
		return model.Approval{}, false, fmt.Errorf("seed demo historical audits: finish approval for audit_id %d: %w", auditID, err)
	}
	return stored, true, nil
}

func sameDemoApprovalIntent(expected, stored model.Approval) bool {
	return expected.ID == stored.ID &&
		equalInt64Pointers(expected.AuditID, stored.AuditID) &&
		equalStringPointers(expected.AgentID, stored.AgentID) &&
		equalStringPointers(expected.SQLRaw, stored.SQLRaw) &&
		equalStringPointers(expected.Reason, stored.Reason) &&
		expected.Status == stored.Status &&
		equalStringPointers(expected.Approver, stored.Approver) &&
		equalTimePointers(expected.DecidedAt, stored.DecidedAt)
}

func equalTimePointers(left, right *time.Time) bool {
	return left == nil && right == nil || left != nil && right != nil && left.Equal(*right)
}
