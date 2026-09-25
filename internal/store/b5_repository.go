package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/b5"
)

var (
	ErrB5CASConflict       = errors.New("b5 store compare-and-swap conflict")
	ErrB5ReceiptConflict   = errors.New("b5 result receipt immutable conflict")
	ErrB5InvalidTransition = errors.New("b5 store invalid state transition")
)

// Feature-off repository contracts are deliberately metadata-only. They are
// exposed for later B5 slices without granting access to a business executor.
type B5SessionStore interface {
	Create(context.Context, B5Session) (B5Session, error)
	Get(context.Context, string) (B5Session, error)
	CASStatus(context.Context, string, int64, b5.SessionStatus, b5.SessionStatus, time.Time) (B5Session, error)
	CASOwner(context.Context, string, int64, string, uint64, string, string, string, []byte) (B5Session, error)
	ListExpired(context.Context, time.Time, int) ([]B5Session, error)
}

type B5TransactionStore interface {
	Create(context.Context, B5Transaction) (B5Transaction, error)
	Get(context.Context, string) (B5Transaction, error)
	CASState(context.Context, string, int64, b5.TransactionStatus, b5.TransactionPhase, b5.TransactionStatus, b5.TransactionPhase) (B5Transaction, error)
	CASProgress(context.Context, string, int64, b5.TransactionStatus, b5.TransactionPhase, B5TransactionProgress) (B5Transaction, error)
	ListExpired(context.Context, time.Time, int) ([]B5Transaction, error)
}

type B5DMLGrantStore interface {
	Create(context.Context, B5DMLGrant) (B5DMLGrant, error)
	Get(context.Context, string) (B5DMLGrant, error)
	List(context.Context, string, string, b5.DMLAction, int) ([]B5DMLGrant, error)
	UpdateIfRevision(context.Context, B5DMLGrant, int64) (B5DMLGrant, error)
	DeleteIfRevision(context.Context, string, int64) error
}

type B5ResultReceiptStore interface {
	PutWriteOnce(context.Context, B5ResultReceipt) (B5ResultReceipt, bool, error)
	Get(context.Context, B5ReceiptKey) (B5ResultReceipt, error)
	ListByEventUUID(context.Context, []byte) ([]B5ResultReceipt, error)
	Advance(context.Context, B5ReceiptKey, int64, B5ReceiptAdvance) (B5ResultReceipt, error)
}

type B5TxEventStore interface {
	Append(context.Context, B5TxEvent) error
	Get(context.Context, string, uint64) (B5TxEvent, error)
	GetByUUID(context.Context, []byte) (B5TxEvent, error)
	List(context.Context, int) ([]B5TxEvent, error)
}

var (
	_ B5SessionStore       = (*B5SessionRepository)(nil)
	_ B5TransactionStore   = (*B5TransactionRepository)(nil)
	_ B5DMLGrantStore      = (*B5DMLGrantRepository)(nil)
	_ B5ResultReceiptStore = (*B5ResultReceiptRepository)(nil)
	_ B5TxEventStore       = (*B5TxEventRepository)(nil)
)

type B5Session struct {
	SessionID, AgentID, TenantID, PrincipalID, OwnerInstanceID string
	OwnerEpoch                                                 uint64
	ContinuationSchemaID                                       string
	ContinuationSchemaVersion                                  uint16
	ContinuationKeyCiphertext                                  string
	ContinuationHMACDigest                                     []byte
	StickyRoute                                                string
	Status                                                     b5.SessionStatus
	IdleExpiresAt, AbsoluteExpiresAt, CreatedAt, UpdatedAt     time.Time
	Revision                                                   int64
}

type B5SessionRepository struct{ repositoryBase }

type B5SessionFilter struct {
	Status, Owner, Query string
}

type B5SessionPage struct {
	Total          int64
	List           []B5Session
	Page, PageSize int
}

func (r *B5SessionRepository) Create(ctx context.Context, value B5Session) (B5Session, error) {
	if ctx == nil {
		return B5Session{}, fmt.Errorf("create b5 session: %w", ErrNilContext)
	}
	query := `INSERT INTO b5_sessions
(session_id,agent_id,tenant_id,principal_id,owner_instance_id,owner_epoch,continuation_schema_id,continuation_schema_version,continuation_key_ciphertext,continuation_hmac_digest,sticky_route,status,idle_expires_at,absolute_expires_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	_, err := r.db.ExecContext(ctx, r.bind(query), value.SessionID, value.AgentID, value.TenantID, value.PrincipalID,
		value.OwnerInstanceID, value.OwnerEpoch, value.ContinuationSchemaID, value.ContinuationSchemaVersion,
		value.ContinuationKeyCiphertext, value.ContinuationHMACDigest, value.StickyRoute, value.Status,
		value.IdleExpiresAt, value.AbsoluteExpiresAt)
	if err != nil {
		return B5Session{}, fmt.Errorf("create b5 session %q: %w", value.SessionID, err)
	}
	return r.Get(ctx, value.SessionID)
}

func (r *B5SessionRepository) Get(ctx context.Context, id string) (B5Session, error) {
	query := `SELECT session_id,agent_id,tenant_id,principal_id,owner_instance_id,owner_epoch,continuation_schema_id,continuation_schema_version,continuation_key_ciphertext,continuation_hmac_digest,sticky_route,status,idle_expires_at,absolute_expires_at,created_at,updated_at,revision FROM b5_sessions WHERE session_id=?`
	value, err := scanB5Session(r.db.QueryRowContext(ctx, r.bind(query), id))
	if errors.Is(err, sql.ErrNoRows) {
		return B5Session{}, fmt.Errorf("get b5 session %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return B5Session{}, fmt.Errorf("get b5 session %q: %w", id, err)
	}
	return value, nil
}

// ListPage exposes only non-secret directory metadata. Continuation key
// ciphertext and HMAC material never cross the repository boundary here.
func (r *B5SessionRepository) ListPage(ctx context.Context, filter B5SessionFilter, page, pageSize int) (B5SessionPage, error) {
	if ctx == nil {
		return B5SessionPage{}, ErrNilContext
	}
	if page < 1 {
		return B5SessionPage{}, ErrInvalidPage
	}
	if pageSize < 1 || pageSize > 100 {
		return B5SessionPage{}, ErrInvalidPageSize
	}
	where, args := []string{}, []any{}
	if filter.Status != "" {
		where = append(where, "status=?")
		args = append(args, filter.Status)
	}
	if filter.Owner != "" {
		where = append(where, "owner_instance_id=?")
		args = append(args, filter.Owner)
	}
	if filter.Query != "" {
		where = append(where, "(LOWER(session_id) LIKE ? OR LOWER(agent_id) LIKE ? OR LOWER(principal_id) LIKE ? OR LOWER(owner_instance_id) LIKE ?)")
		pattern := "%" + strings.ToLower(filter.Query) + "%"
		args = append(args, pattern, pattern, pattern, pattern)
	}
	clause := ""
	if len(where) != 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}
	var total int64
	if err := r.db.QueryRowContext(ctx, r.bind("SELECT COUNT(*) FROM b5_sessions"+clause), args...).Scan(&total); err != nil {
		return B5SessionPage{}, err
	}
	query := `SELECT session_id,agent_id,tenant_id,principal_id,owner_instance_id,owner_epoch,continuation_schema_id,continuation_schema_version,continuation_key_ciphertext,continuation_hmac_digest,sticky_route,status,idle_expires_at,absolute_expires_at,created_at,updated_at,revision FROM b5_sessions` + clause + ` ORDER BY updated_at DESC,session_id DESC LIMIT ? OFFSET ?`
	rows, err := r.db.QueryContext(ctx, r.bind(query), append(append([]any{}, args...), pageSize, (page-1)*pageSize)...)
	if err != nil {
		return B5SessionPage{}, err
	}
	defer rows.Close()
	values := make([]B5Session, 0, pageSize)
	for rows.Next() {
		value, scanErr := scanB5Session(rows)
		if scanErr != nil {
			return B5SessionPage{}, scanErr
		}
		values = append(values, value)
	}
	return B5SessionPage{Total: total, List: values, Page: page, PageSize: pageSize}, rows.Err()
}

func (r *B5SessionRepository) CASStatus(ctx context.Context, id string, expectedRevision int64, from, to b5.SessionStatus, idleExpiry time.Time) (B5Session, error) {
	if !validSessionTransition(from, to) {
		return B5Session{}, ErrB5InvalidTransition
	}
	result, err := r.db.ExecContext(ctx, r.bind(`UPDATE b5_sessions SET status=?,idle_expires_at=?,updated_at=CURRENT_TIMESTAMP,revision=revision+1 WHERE session_id=? AND revision=? AND status=?`), to, idleExpiry, id, expectedRevision, from)
	if err != nil {
		return B5Session{}, fmt.Errorf("advance b5 session %q: %w", id, err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return B5Session{}, fmt.Errorf("advance b5 session %q: %w", id, ErrB5CASConflict)
	}
	return r.Get(ctx, id)
}

// CASOwner is the S3 directory fencing primitive. Owner epoch advances by
// exactly one and the continuation key is re-sealed with AAD containing that
// new epoch in the same durable compare-and-swap.
func (r *B5SessionRepository) CASOwner(ctx context.Context, id string, expectedRevision int64, expectedOwner string, expectedEpoch uint64, newOwner, stickyRoute, keyCiphertext string, keyDigest []byte) (B5Session, error) {
	if ctx == nil {
		return B5Session{}, fmt.Errorf("transfer b5 session owner: %w", ErrNilContext)
	}
	if expectedEpoch == 0 || expectedEpoch >= math.MaxInt64 || expectedOwner == "" || newOwner == "" || stickyRoute == "" || keyCiphertext == "" || len(keyDigest) != 32 {
		return B5Session{}, fmt.Errorf("transfer b5 session owner: %w", ErrB5InvalidTransition)
	}
	result, err := r.db.ExecContext(ctx, r.bind(`UPDATE b5_sessions SET owner_instance_id=?,owner_epoch=?,sticky_route=?,continuation_key_ciphertext=?,continuation_hmac_digest=?,updated_at=CURRENT_TIMESTAMP,revision=revision+1 WHERE session_id=? AND revision=? AND owner_instance_id=? AND owner_epoch=? AND status IN ('READY','ACTIVE')`), newOwner, expectedEpoch+1, stickyRoute, keyCiphertext, keyDigest, id, expectedRevision, expectedOwner, expectedEpoch)
	if err != nil {
		return B5Session{}, fmt.Errorf("transfer b5 session owner %q: %w", id, err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return B5Session{}, fmt.Errorf("transfer b5 session owner %q: %w", id, ErrB5CASConflict)
	}
	return r.Get(ctx, id)
}

func validSessionTransition(from, to b5.SessionStatus) bool {
	return from == to || from == b5.SessionReady && to == b5.SessionActive ||
		from == b5.SessionActive && to == b5.SessionReady ||
		(from == b5.SessionReady || from == b5.SessionActive) && (to == b5.SessionTerminal || to == b5.SessionExpired)
}

func (r *B5SessionRepository) ListExpired(ctx context.Context, now time.Time, limit int) ([]B5Session, error) {
	if limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("list expired b5 sessions: invalid limit")
	}
	rows, err := r.db.QueryContext(ctx, r.bind(`SELECT session_id,agent_id,tenant_id,principal_id,owner_instance_id,owner_epoch,continuation_schema_id,continuation_schema_version,continuation_key_ciphertext,continuation_hmac_digest,sticky_route,status,idle_expires_at,absolute_expires_at,created_at,updated_at,revision FROM b5_sessions WHERE status IN ('READY','ACTIVE') AND (idle_expires_at<=? OR absolute_expires_at<=?) ORDER BY absolute_expires_at,session_id LIMIT ?`), now, now, limit)
	if err != nil {
		return nil, fmt.Errorf("list expired b5 sessions: %w", err)
	}
	defer rows.Close()
	values := make([]B5Session, 0)
	for rows.Next() {
		value, scanErr := scanB5Session(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func scanB5Session(row rowScanner) (B5Session, error) {
	var value B5Session
	var idle, absolute, created, updated databaseTimestamp
	var ownerEpoch int64
	var schemaVersion int64
	err := row.Scan(&value.SessionID, &value.AgentID, &value.TenantID, &value.PrincipalID, &value.OwnerInstanceID, &ownerEpoch,
		&value.ContinuationSchemaID, &schemaVersion, &value.ContinuationKeyCiphertext, &value.ContinuationHMACDigest, &value.StickyRoute, &value.Status,
		&idle, &absolute, &created, &updated, &value.Revision)
	if err != nil {
		return B5Session{}, err
	}
	value.OwnerEpoch = uint64(ownerEpoch)
	value.ContinuationSchemaVersion = uint16(schemaVersion)
	value.IdleExpiresAt = idle.time
	value.AbsoluteExpiresAt = absolute.time
	value.CreatedAt = created.time
	value.UpdatedAt = updated.time
	return value, nil
}

type B5Transaction struct {
	TransactionID, SessionID, DatasourceID string
	Status                                 b5.TransactionStatus
	Phase                                  b5.TransactionPhase
	PlanDigest                             []byte
	ApprovalID                             *string
	OwnerEpoch                             uint64
	IdleDeadline, WallDeadline             time.Time
	StatementDeadline                      *time.Time
	BackendPID                             *int
	BackendSecretDigest                    []byte
	BackendStartedAt                       *time.Time
	ConnectionGeneration, LeaseGeneration  uint64
	StatementCount                         int
	TransactionSeq                         uint64
	PreviousTxEventDigest                  []byte
	CreatedAt, UpdatedAt                   time.Time
	Revision                               int64
}

// B5TransactionProgress is the bounded mutable part of a live transaction.
// Pointers distinguish "leave unchanged" from a typed zero value.  Callers
// must still own the transaction's current revision, status and phase; this
// prevents a late statement result from extending a deadline or advancing a
// sequence after a terminal/watchdog CAS has won.
type B5TransactionProgress struct {
	IdleDeadline         *time.Time
	StatementDeadline    **time.Time
	BackendPID           **int
	BackendSecretDigest  *[]byte
	BackendStartedAt     **time.Time
	ConnectionGeneration *uint64
	LeaseGeneration      *uint64
	StatementCount       *int
	TransactionSeq       *uint64
	PreviousEventDigest  *[]byte
}

type B5TransactionRepository struct{ repositoryBase }

type B5TransactionFilter struct {
	Status, Phase, DatasourceID, Query string
}

type B5TransactionPage struct {
	Total          int64
	List           []B5Transaction
	Page, PageSize int
}

func (r *B5TransactionRepository) Create(ctx context.Context, value B5Transaction) (B5Transaction, error) {
	query := `INSERT INTO b5_transactions (transaction_id,session_id,datasource_id,status,phase,plan_digest,approval_id,owner_epoch,idle_deadline,wall_deadline,statement_deadline,backend_pid,backend_secret_digest,backend_started_at,connection_generation,lease_generation,statement_count,transaction_seq,previous_tx_event_digest) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	_, err := r.db.ExecContext(ctx, r.bind(query), value.TransactionID, value.SessionID, value.DatasourceID, value.Status, value.Phase, value.PlanDigest, optionalString(value.ApprovalID), value.OwnerEpoch, value.IdleDeadline, value.WallDeadline, optionalTime(value.StatementDeadline), optionalInt(value.BackendPID), nullableBytes(value.BackendSecretDigest), optionalTime(value.BackendStartedAt), value.ConnectionGeneration, value.LeaseGeneration, value.StatementCount, value.TransactionSeq, nullableBytes(value.PreviousTxEventDigest))
	if err != nil {
		return B5Transaction{}, fmt.Errorf("create b5 transaction %q: %w", value.TransactionID, err)
	}
	return r.Get(ctx, value.TransactionID)
}

func (r *B5TransactionRepository) Get(ctx context.Context, id string) (B5Transaction, error) {
	query := `SELECT transaction_id,session_id,datasource_id,status,phase,plan_digest,approval_id,owner_epoch,idle_deadline,wall_deadline,statement_deadline,backend_pid,backend_secret_digest,backend_started_at,connection_generation,lease_generation,statement_count,transaction_seq,previous_tx_event_digest,created_at,updated_at,revision FROM b5_transactions WHERE transaction_id=?`
	value, err := scanB5Transaction(r.db.QueryRowContext(ctx, r.bind(query), id))
	if errors.Is(err, sql.ErrNoRows) {
		return B5Transaction{}, fmt.Errorf("get b5 transaction %q: %w", id, ErrNotFound)
	}
	return value, err
}

func (r *B5TransactionRepository) ListPage(ctx context.Context, filter B5TransactionFilter, page, pageSize int) (B5TransactionPage, error) {
	if ctx == nil {
		return B5TransactionPage{}, ErrNilContext
	}
	if page < 1 {
		return B5TransactionPage{}, ErrInvalidPage
	}
	if pageSize < 1 || pageSize > 100 {
		return B5TransactionPage{}, ErrInvalidPageSize
	}
	where, args := []string{}, []any{}
	add := func(column, value string) {
		if value != "" {
			where = append(where, column+"=?")
			args = append(args, value)
		}
	}
	add("status", filter.Status)
	add("phase", filter.Phase)
	add("datasource_id", filter.DatasourceID)
	if filter.Query != "" {
		where = append(where, "(LOWER(transaction_id) LIKE ? OR LOWER(session_id) LIKE ? OR LOWER(datasource_id) LIKE ?)")
		pattern := "%" + strings.ToLower(filter.Query) + "%"
		args = append(args, pattern, pattern, pattern)
	}
	clause := ""
	if len(where) != 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}
	var total int64
	if err := r.db.QueryRowContext(ctx, r.bind("SELECT COUNT(*) FROM b5_transactions"+clause), args...).Scan(&total); err != nil {
		return B5TransactionPage{}, err
	}
	query := `SELECT transaction_id,session_id,datasource_id,status,phase,plan_digest,approval_id,owner_epoch,idle_deadline,wall_deadline,statement_deadline,backend_pid,backend_secret_digest,backend_started_at,connection_generation,lease_generation,statement_count,transaction_seq,previous_tx_event_digest,created_at,updated_at,revision FROM b5_transactions` + clause + ` ORDER BY updated_at DESC,transaction_id DESC LIMIT ? OFFSET ?`
	rows, err := r.db.QueryContext(ctx, r.bind(query), append(append([]any{}, args...), pageSize, (page-1)*pageSize)...)
	if err != nil {
		return B5TransactionPage{}, err
	}
	defer rows.Close()
	values := make([]B5Transaction, 0, pageSize)
	for rows.Next() {
		value, scanErr := scanB5Transaction(rows)
		if scanErr != nil {
			return B5TransactionPage{}, scanErr
		}
		values = append(values, value)
	}
	return B5TransactionPage{Total: total, List: values, Page: page, PageSize: pageSize}, rows.Err()
}

func (r *B5TransactionRepository) CASState(ctx context.Context, id string, revision int64, fromStatus b5.TransactionStatus, fromPhase b5.TransactionPhase, toStatus b5.TransactionStatus, toPhase b5.TransactionPhase) (B5Transaction, error) {
	if !validTransactionTransition(fromStatus, fromPhase, toStatus, toPhase) {
		return B5Transaction{}, ErrB5InvalidTransition
	}
	result, err := r.db.ExecContext(ctx, r.bind(`UPDATE b5_transactions SET status=?,phase=?,updated_at=CURRENT_TIMESTAMP,revision=revision+1 WHERE transaction_id=? AND revision=? AND status=? AND phase=?`), toStatus, toPhase, id, revision, fromStatus, fromPhase)
	if err != nil {
		return B5Transaction{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return B5Transaction{}, ErrB5CASConflict
	}
	return r.Get(ctx, id)
}

// CASProgress persists deadline, backend and ordered-statement progress
// without changing the lifecycle phase.  It is intentionally one SQL update:
// recovery must never observe a new statement count with an old deadline (or
// vice versa), and stale operation owners must lose the revision CAS.
func (r *B5TransactionRepository) CASProgress(ctx context.Context, id string, revision int64, status b5.TransactionStatus, phase b5.TransactionPhase, update B5TransactionProgress) (B5Transaction, error) {
	sets := []string{"updated_at=CURRENT_TIMESTAMP", "revision=revision+1"}
	args := make([]any, 0, 13)
	add := func(column string, value any) {
		sets = append(sets, column+"=?")
		args = append(args, value)
	}
	if update.IdleDeadline != nil {
		add("idle_deadline", *update.IdleDeadline)
	}
	if update.StatementDeadline != nil {
		add("statement_deadline", optionalTime(*update.StatementDeadline))
	}
	if update.BackendPID != nil {
		add("backend_pid", optionalInt(*update.BackendPID))
	}
	if update.BackendSecretDigest != nil {
		add("backend_secret_digest", nullableBytes(*update.BackendSecretDigest))
	}
	if update.BackendStartedAt != nil {
		add("backend_started_at", optionalTime(*update.BackendStartedAt))
	}
	if update.ConnectionGeneration != nil {
		add("connection_generation", *update.ConnectionGeneration)
	}
	if update.LeaseGeneration != nil {
		add("lease_generation", *update.LeaseGeneration)
	}
	if update.StatementCount != nil {
		add("statement_count", *update.StatementCount)
	}
	if update.TransactionSeq != nil {
		add("transaction_seq", *update.TransactionSeq)
	}
	if update.PreviousEventDigest != nil {
		add("previous_tx_event_digest", nullableBytes(*update.PreviousEventDigest))
	}
	args = append(args, id, revision, status, phase)
	query := `UPDATE b5_transactions SET ` + strings.Join(sets, ",") + ` WHERE transaction_id=? AND revision=? AND status=? AND phase=?`
	result, err := r.db.ExecContext(ctx, r.bind(query), args...)
	if err != nil {
		return B5Transaction{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return B5Transaction{}, ErrB5CASConflict
	}
	return r.Get(ctx, id)
}

func validTransactionTransition(fromStatus b5.TransactionStatus, fromPhase b5.TransactionPhase, toStatus b5.TransactionStatus, toPhase b5.TransactionPhase) bool {
	if fromStatus == toStatus && fromPhase == toPhase {
		return true
	}
	edges := map[b5.TransactionPhase][]b5.TransactionPhase{
		b5.PhaseReady:                {b5.PhasePlanReady},
		b5.PhasePlanReady:            {b5.PhaseApprovalConsumed},
		b5.PhaseApprovalConsumed:     {b5.PhaseConnectionPinned, b5.PhaseBeginFailTerminating},
		b5.PhaseConnectionPinned:     {b5.PhaseNativeBegun, b5.PhaseBeginFailTerminating},
		b5.PhaseNativeBegun:          {b5.PhaseContextFixed, b5.PhaseBeginFailTerminating},
		b5.PhaseContextFixed:         {b5.PhaseSealedInTx, b5.PhaseBeginFailTerminating},
		b5.PhaseSealedInTx:           {b5.PhaseBeginAuditing, b5.PhaseBeginFailTerminating},
		b5.PhaseBeginAuditing:        {b5.PhaseActive, b5.PhaseBeginFailTerminating},
		b5.PhaseActive:               {b5.PhaseRollbackOnly, b5.PhaseCommitting},
		b5.PhaseRollbackOnly:         {b5.PhaseRollingBack},
		b5.PhaseCommitting:           {b5.PhaseTerminal},
		b5.PhaseRollingBack:          {b5.PhaseTerminal},
		b5.PhaseBeginFailTerminating: {b5.PhaseTerminal},
		b5.PhaseTerminal:             {b5.PhaseFinalFence},
	}
	allowed := false
	for _, phase := range edges[fromPhase] {
		allowed = allowed || phase == toPhase
	}
	if !allowed {
		return false
	}
	switch toPhase {
	case b5.PhaseReady, b5.PhasePlanReady, b5.PhaseApprovalConsumed, b5.PhaseConnectionPinned, b5.PhaseNativeBegun, b5.PhaseContextFixed, b5.PhaseSealedInTx, b5.PhaseBeginAuditing:
		return toStatus == b5.TransactionPending
	case b5.PhaseActive, b5.PhaseCommitting, b5.PhaseRollingBack:
		return toStatus == b5.TransactionActive
	case b5.PhaseRollbackOnly:
		return toStatus == b5.TransactionRollbackOnly
	case b5.PhaseBeginFailTerminating, b5.PhaseTerminal, b5.PhaseFinalFence:
		return toStatus == b5.TransactionTerminal
	default:
		return false
	}
}

func (r *B5TransactionRepository) ListExpired(ctx context.Context, now time.Time, limit int) ([]B5Transaction, error) {
	if limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("list expired b5 transactions: invalid limit")
	}
	query := `SELECT transaction_id,session_id,datasource_id,status,phase,plan_digest,approval_id,owner_epoch,idle_deadline,wall_deadline,statement_deadline,backend_pid,backend_secret_digest,backend_started_at,connection_generation,lease_generation,statement_count,transaction_seq,previous_tx_event_digest,created_at,updated_at,revision FROM b5_transactions WHERE status<>'TERMINAL' AND (idle_deadline<=? OR wall_deadline<=? OR (statement_deadline IS NOT NULL AND statement_deadline<=?)) ORDER BY wall_deadline,transaction_id LIMIT ?`
	rows, err := r.db.QueryContext(ctx, r.bind(query), now, now, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]B5Transaction, 0)
	for rows.Next() {
		value, scanErr := scanB5Transaction(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func scanB5Transaction(row rowScanner) (B5Transaction, error) {
	var value B5Transaction
	var approval sql.NullString
	var owner, connection, lease, seq int64
	var statementDeadline, backendStarted databaseTimestamp
	var backendPID sql.NullInt64
	var idle, wall, created, updated databaseTimestamp
	var backendDigest, previous []byte
	err := row.Scan(&value.TransactionID, &value.SessionID, &value.DatasourceID, &value.Status, &value.Phase, &value.PlanDigest, &approval, &owner, &idle, &wall, &statementDeadline, &backendPID, &backendDigest, &backendStarted, &connection, &lease, &value.StatementCount, &seq, &previous, &created, &updated, &value.Revision)
	if err != nil {
		return value, err
	}
	value.ApprovalID = stringPointer(approval)
	value.OwnerEpoch = uint64(owner)
	value.IdleDeadline = idle.time
	value.WallDeadline = wall.time
	value.StatementDeadline = statementDeadline.pointer()
	value.BackendPID, _ = intPointer(backendPID)
	value.BackendSecretDigest = backendDigest
	value.BackendStartedAt = backendStarted.pointer()
	value.ConnectionGeneration = uint64(connection)
	value.LeaseGeneration = uint64(lease)
	value.TransactionSeq = uint64(seq)
	value.PreviousTxEventDigest = previous
	value.CreatedAt = created.time
	value.UpdatedAt = updated.time
	return value, nil
}

type B5DMLGrant struct {
	GrantID, PolicyID, PrincipalID, DatasourceID               string
	PolicyRevision                                             int64
	Effect                                                     b5.GrantEffect
	Element                                                    b5.GrantElement
	Action                                                     b5.DMLAction
	DatabaseOID, RelationOID                                   uint32
	RelationKind, SchemaName, RelationName, CatalogFingerprint string
	WriteTargetKind                                            *string
	ColumnAttnum                                               *int
	ColumnName                                                 *string
	ColumnTypeOID                                              *uint32
	ColumnTypeModifier                                         *int
	ColumnCollationOID                                         *uint32
	ReferenceKind                                              *string
	ProofSchemaID                                              string
	ProofSchemaVersion                                         uint16
	ProofDigest                                                []byte
	CreatedAt, UpdatedAt                                       time.Time
	Revision                                                   int64
}

type B5DMLGrantRepository struct{ repositoryBase }

func (r *B5DMLGrantRepository) Create(ctx context.Context, v B5DMLGrant) (B5DMLGrant, error) {
	q := `INSERT INTO b5_dml_grants (grant_id,policy_id,policy_revision,principal_id,datasource_id,effect,grant_element,action,database_oid,relation_oid,relation_kind,schema_name,relation_name,catalog_fingerprint,write_target_kind,column_attnum,column_name,column_type_oid,column_type_modifier,column_collation_oid,reference_kind,proof_schema_id,proof_schema_version,proof_digest) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	_, err := r.db.ExecContext(ctx, r.bind(q), v.GrantID, v.PolicyID, v.PolicyRevision, v.PrincipalID, v.DatasourceID, v.Effect, v.Element, v.Action, v.DatabaseOID, v.RelationOID, v.RelationKind, v.SchemaName, v.RelationName, v.CatalogFingerprint, optionalString(v.WriteTargetKind), optionalInt(v.ColumnAttnum), optionalString(v.ColumnName), optionalUint32(v.ColumnTypeOID), optionalInt(v.ColumnTypeModifier), optionalUint32(v.ColumnCollationOID), optionalString(v.ReferenceKind), v.ProofSchemaID, v.ProofSchemaVersion, v.ProofDigest)
	if err != nil {
		return B5DMLGrant{}, err
	}
	return r.Get(ctx, v.GrantID)
}

func (r *B5DMLGrantRepository) UpdateIfRevision(ctx context.Context, v B5DMLGrant, expected int64) (B5DMLGrant, error) {
	q := `UPDATE b5_dml_grants SET policy_id=?,policy_revision=?,principal_id=?,datasource_id=?,effect=?,grant_element=?,action=?,database_oid=?,relation_oid=?,relation_kind=?,schema_name=?,relation_name=?,catalog_fingerprint=?,write_target_kind=?,column_attnum=?,column_name=?,column_type_oid=?,column_type_modifier=?,column_collation_oid=?,reference_kind=?,proof_schema_id=?,proof_schema_version=?,proof_digest=?,updated_at=CURRENT_TIMESTAMP,revision=revision+1 WHERE grant_id=? AND revision=?`
	result, err := r.db.ExecContext(ctx, r.bind(q), v.PolicyID, v.PolicyRevision, v.PrincipalID, v.DatasourceID, v.Effect, v.Element, v.Action, v.DatabaseOID, v.RelationOID, v.RelationKind, v.SchemaName, v.RelationName, v.CatalogFingerprint, optionalString(v.WriteTargetKind), optionalInt(v.ColumnAttnum), optionalString(v.ColumnName), optionalUint32(v.ColumnTypeOID), optionalInt(v.ColumnTypeModifier), optionalUint32(v.ColumnCollationOID), optionalString(v.ReferenceKind), v.ProofSchemaID, v.ProofSchemaVersion, v.ProofDigest, v.GrantID, expected)
	if err != nil {
		return B5DMLGrant{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return B5DMLGrant{}, ErrB5CASConflict
	}
	return r.Get(ctx, v.GrantID)
}

func (r *B5DMLGrantRepository) Get(ctx context.Context, id string) (B5DMLGrant, error) {
	v, err := scanB5DMLGrant(r.db.QueryRowContext(ctx, r.bind(b5GrantSelect+` WHERE grant_id=?`), id))
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	return v, err
}

func (r *B5DMLGrantRepository) List(ctx context.Context, principal, datasource string, action b5.DMLAction, limit int) ([]B5DMLGrant, error) {
	if limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("list b5 grants: invalid limit")
	}
	rows, err := r.db.QueryContext(ctx, r.bind(b5GrantSelect+` WHERE principal_id=? AND datasource_id=? AND action=? ORDER BY policy_id,grant_id LIMIT ?`), principal, datasource, action, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]B5DMLGrant, 0)
	for rows.Next() {
		v, e := scanB5DMLGrant(rows)
		if e != nil {
			return nil, e
		}
		values = append(values, v)
	}
	return values, rows.Err()
}

func (r *B5DMLGrantRepository) DeleteIfRevision(ctx context.Context, id string, revision int64) error {
	result, err := r.db.ExecContext(ctx, r.bind(`DELETE FROM b5_dml_grants WHERE grant_id=? AND revision=?`), id, revision)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrB5CASConflict
	}
	return nil
}

const b5GrantSelect = `SELECT grant_id,policy_id,policy_revision,principal_id,datasource_id,effect,grant_element,action,database_oid,relation_oid,relation_kind,schema_name,relation_name,catalog_fingerprint,write_target_kind,column_attnum,column_name,column_type_oid,column_type_modifier,column_collation_oid,reference_kind,proof_schema_id,proof_schema_version,proof_digest,created_at,updated_at,revision FROM b5_dml_grants`

func scanB5DMLGrant(row rowScanner) (B5DMLGrant, error) {
	var v B5DMLGrant
	var writeKind, columnName, reference sql.NullString
	var attnum, typeOID, typeMod, collation sql.NullInt64
	var databaseOID, relationOID int64
	var schemaVersion int64
	var created, updated databaseTimestamp
	err := row.Scan(&v.GrantID, &v.PolicyID, &v.PolicyRevision, &v.PrincipalID, &v.DatasourceID, &v.Effect, &v.Element, &v.Action, &databaseOID, &relationOID, &v.RelationKind, &v.SchemaName, &v.RelationName, &v.CatalogFingerprint, &writeKind, &attnum, &columnName, &typeOID, &typeMod, &collation, &reference, &v.ProofSchemaID, &schemaVersion, &v.ProofDigest, &created, &updated, &v.Revision)
	if err != nil {
		return v, err
	}
	v.DatabaseOID = uint32(databaseOID)
	v.RelationOID = uint32(relationOID)
	v.WriteTargetKind = stringPointer(writeKind)
	v.ColumnAttnum, _ = intPointer(attnum)
	v.ColumnName = stringPointer(columnName)
	v.ColumnTypeOID = uint32Pointer(typeOID)
	v.ColumnTypeModifier, _ = intPointer(typeMod)
	v.ColumnCollationOID = uint32Pointer(collation)
	v.ReferenceKind = stringPointer(reference)
	v.ProofSchemaVersion = uint16(schemaVersion)
	v.CreatedAt = created.time
	v.UpdatedAt = updated.time
	return v, nil
}

type B5ReceiptKey struct {
	SessionID, RequestID string
	EventUUID            []byte
	AttemptGeneration    uint64
}
type B5ResultReceipt struct {
	Key                                         B5ReceiptKey
	SchemaID                                    string
	SchemaVersion                               uint16
	BusinessEventDigest, WALAppendReceiptDigest []byte
	ReportedDurability                          b5.AuditDurability
	AppendConfirmation                          b5.AppendConfirmation
	Reconciliation                              b5.Reconciliation
	DeliveryStatus                              b5.DeliveryStatus
	CreatedAt, UpdatedAt                        time.Time
	Revision                                    int64
}
type B5ReceiptAdvance struct {
	AppendConfirmation *b5.AppendConfirmation
	Reconciliation     *b5.Reconciliation
	DeliveryStatus     *b5.DeliveryStatus
}
type B5ResultReceiptRepository struct{ repositoryBase }

func (r *B5ResultReceiptRepository) PutWriteOnce(ctx context.Context, v B5ResultReceipt) (B5ResultReceipt, bool, error) {
	q := `INSERT INTO b5_result_receipts (session_id,request_id,event_uuid,attempt_generation,schema_id,schema_version,business_event_digest,wal_append_receipt_digest,reported_durability,append_confirmation,reconciliation,delivery_status) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`
	_, err := r.db.ExecContext(ctx, r.bind(q), v.Key.SessionID, v.Key.RequestID, v.Key.EventUUID, v.Key.AttemptGeneration, v.SchemaID, v.SchemaVersion, v.BusinessEventDigest, v.WALAppendReceiptDigest, v.ReportedDurability, v.AppendConfirmation, v.Reconciliation, v.DeliveryStatus)
	if err == nil {
		stored, e := r.Get(ctx, v.Key)
		return stored, true, e
	}
	stored, getErr := r.Get(ctx, v.Key)
	if getErr != nil {
		return B5ResultReceipt{}, false, err
	}
	if !sameB5Receipt(stored, v) {
		return B5ResultReceipt{}, false, ErrB5ReceiptConflict
	}
	return stored, false, nil
}

func (r *B5ResultReceiptRepository) Get(ctx context.Context, key B5ReceiptKey) (B5ResultReceipt, error) {
	q := `SELECT session_id,request_id,event_uuid,attempt_generation,schema_id,schema_version,business_event_digest,wal_append_receipt_digest,reported_durability,append_confirmation,reconciliation,delivery_status,created_at,updated_at,revision FROM b5_result_receipts WHERE session_id=? AND request_id=? AND event_uuid=? AND attempt_generation=?`
	v, err := scanB5Receipt(r.db.QueryRowContext(ctx, r.bind(q), key.SessionID, key.RequestID, key.EventUUID, key.AttemptGeneration))
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	return v, err
}

// ListByEventUUID joins the durable response plane to a verified WAL record.
// More than one row is possible because a request can be retried with a new
// attempt generation; callers must preserve every immutable historical value.
func (r *B5ResultReceiptRepository) ListByEventUUID(ctx context.Context, eventUUID []byte) ([]B5ResultReceipt, error) {
	q := `SELECT session_id,request_id,event_uuid,attempt_generation,schema_id,schema_version,business_event_digest,wal_append_receipt_digest,reported_durability,append_confirmation,reconciliation,delivery_status,created_at,updated_at,revision FROM b5_result_receipts WHERE event_uuid=? ORDER BY session_id,request_id,attempt_generation`
	rows, err := r.db.QueryContext(ctx, r.bind(q), eventUUID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]B5ResultReceipt, 0)
	for rows.Next() {
		value, scanErr := scanB5Receipt(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (r *B5ResultReceiptRepository) Advance(ctx context.Context, key B5ReceiptKey, revision int64, u B5ReceiptAdvance) (B5ResultReceipt, error) {
	current, err := r.Get(ctx, key)
	if err != nil {
		return current, err
	}
	if current.Revision != revision || !validReceiptAdvance(current, u) {
		return B5ResultReceipt{}, ErrB5InvalidTransition
	}
	appendValue, reconcileValue, deliveryValue := current.AppendConfirmation, current.Reconciliation, current.DeliveryStatus
	if u.AppendConfirmation != nil {
		appendValue = *u.AppendConfirmation
	}
	if u.Reconciliation != nil {
		reconcileValue = *u.Reconciliation
	}
	if u.DeliveryStatus != nil {
		deliveryValue = *u.DeliveryStatus
	}
	result, err := r.db.ExecContext(ctx, r.bind(`UPDATE b5_result_receipts SET append_confirmation=?,reconciliation=?,delivery_status=?,updated_at=CURRENT_TIMESTAMP,revision=revision+1 WHERE session_id=? AND request_id=? AND event_uuid=? AND attempt_generation=? AND revision=?`), appendValue, reconcileValue, deliveryValue, key.SessionID, key.RequestID, key.EventUUID, key.AttemptGeneration, revision)
	if err != nil {
		return B5ResultReceipt{}, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return B5ResultReceipt{}, ErrB5CASConflict
	}
	return r.Get(ctx, key)
}

func scanB5Receipt(row rowScanner) (B5ResultReceipt, error) {
	var v B5ResultReceipt
	var attempt, schemaVersion int64
	var created, updated databaseTimestamp
	err := row.Scan(&v.Key.SessionID, &v.Key.RequestID, &v.Key.EventUUID, &attempt, &v.SchemaID, &schemaVersion, &v.BusinessEventDigest, &v.WALAppendReceiptDigest, &v.ReportedDurability, &v.AppendConfirmation, &v.Reconciliation, &v.DeliveryStatus, &created, &updated, &v.Revision)
	v.Key.AttemptGeneration = uint64(attempt)
	v.SchemaVersion = uint16(schemaVersion)
	v.CreatedAt = created.time
	v.UpdatedAt = updated.time
	return v, err
}
func sameB5Receipt(a, b B5ResultReceipt) bool {
	return string(a.BusinessEventDigest) == string(b.BusinessEventDigest) && string(a.WALAppendReceiptDigest) == string(b.WALAppendReceiptDigest) && a.ReportedDurability == b.ReportedDurability && a.SchemaID == b.SchemaID && a.SchemaVersion == b.SchemaVersion
}
func validReceiptAdvance(v B5ResultReceipt, u B5ReceiptAdvance) bool {
	if u.AppendConfirmation != nil && !validAppendAdvance(v.AppendConfirmation, *u.AppendConfirmation) {
		return false
	}
	if u.Reconciliation != nil && !validReconcileAdvance(v.Reconciliation, *u.Reconciliation) {
		return false
	}
	if u.DeliveryStatus != nil && !validDeliveryAdvance(v.DeliveryStatus, *u.DeliveryStatus) {
		return false
	}
	return true
}
func validAppendAdvance(a, c b5.AppendConfirmation) bool {
	return a == c || a == b5.AppendUnknown && c == b5.AppendTimeout || a == b5.AppendTimeout && (c == b5.AppendLateConfirmed || c == b5.AppendRecovered) || a == b5.AppendLateConfirmed && c == b5.AppendRecovered
}
func validReconcileAdvance(a, c b5.Reconciliation) bool {
	return a == c || a == b5.ReconciliationNone && c == b5.ReconciliationReplayStaged || a == b5.ReconciliationReplayStaged && c == b5.ReconciliationPrimaryDurable
}
func validDeliveryAdvance(a, c b5.DeliveryStatus) bool {
	return a == c || a == b5.DeliveryPrepared && c == b5.DeliverySendStarted || a == b5.DeliverySendStarted && c == b5.DeliverySendComplete
}

type B5TxEvent struct {
	TransactionID                                      string
	TransactionSeq                                     uint64
	EventUUID                                          []byte
	EventType, EventSchemaID                           string
	EventSchemaVersion                                 uint16
	PreviousTxEventDigest, EventDigest, CanonicalEvent []byte
	TerminalEvidenceText, DispositionProofText         *string
	AuditLogID                                         *int64
	CreatedAt                                          time.Time
}
type B5TxEventRepository struct{ repositoryBase }

func (r *B5TxEventRepository) Append(ctx context.Context, v B5TxEvent) error {
	q := `INSERT INTO b5_tx_events (transaction_id,transaction_seq,event_uuid,event_type,event_schema_id,event_schema_version,previous_tx_event_digest,event_digest,canonical_event,terminal_evidence_text,disposition_proof_text,audit_log_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`
	_, err := r.db.ExecContext(ctx, r.bind(q), v.TransactionID, v.TransactionSeq, v.EventUUID, v.EventType, v.EventSchemaID, v.EventSchemaVersion, nullableBytes(v.PreviousTxEventDigest), v.EventDigest, v.CanonicalEvent, optionalString(v.TerminalEvidenceText), optionalString(v.DispositionProofText), optionalInt64(v.AuditLogID))
	return err
}

func (r *B5TxEventRepository) Get(ctx context.Context, transactionID string, sequence uint64) (B5TxEvent, error) {
	q := `SELECT transaction_id,transaction_seq,event_uuid,event_type,event_schema_id,event_schema_version,previous_tx_event_digest,event_digest,canonical_event,terminal_evidence_text,disposition_proof_text,audit_log_id,created_at FROM b5_tx_events WHERE transaction_id=? AND transaction_seq=?`
	return scanB5TxEvent(r.db.QueryRowContext(ctx, r.bind(q), transactionID, sequence))
}

func (r *B5TxEventRepository) GetByUUID(ctx context.Context, eventUUID []byte) (B5TxEvent, error) {
	q := `SELECT transaction_id,transaction_seq,event_uuid,event_type,event_schema_id,event_schema_version,previous_tx_event_digest,event_digest,canonical_event,terminal_evidence_text,disposition_proof_text,audit_log_id,created_at FROM b5_tx_events WHERE event_uuid=?`
	return scanB5TxEvent(r.db.QueryRowContext(ctx, r.bind(q), eventUUID))
}

func (r *B5TxEventRepository) List(ctx context.Context, limit int) ([]B5TxEvent, error) {
	if limit < 1 || limit > 100000 {
		return nil, fmt.Errorf("list b5 tx events: invalid limit")
	}
	q := `SELECT transaction_id,transaction_seq,event_uuid,event_type,event_schema_id,event_schema_version,previous_tx_event_digest,event_digest,canonical_event,terminal_evidence_text,disposition_proof_text,audit_log_id,created_at FROM b5_tx_events ORDER BY created_at,transaction_id,transaction_seq LIMIT ?`
	rows, err := r.db.QueryContext(ctx, r.bind(q), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]B5TxEvent, 0)
	for rows.Next() {
		value, scanErr := scanB5TxEvent(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (r *B5TxEventRepository) ListByTransaction(ctx context.Context, transactionID string, limit int) ([]B5TxEvent, error) {
	if transactionID == "" || limit < 1 || limit > 10000 {
		return nil, fmt.Errorf("list b5 transaction events: invalid input")
	}
	q := `SELECT transaction_id,transaction_seq,event_uuid,event_type,event_schema_id,event_schema_version,previous_tx_event_digest,event_digest,canonical_event,terminal_evidence_text,disposition_proof_text,audit_log_id,created_at FROM b5_tx_events WHERE transaction_id=? ORDER BY transaction_seq LIMIT ?`
	rows, err := r.db.QueryContext(ctx, r.bind(q), transactionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]B5TxEvent, 0)
	for rows.Next() {
		value, scanErr := scanB5TxEvent(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func scanB5TxEvent(row rowScanner) (B5TxEvent, error) {
	var value B5TxEvent
	var seq, schemaVersion int64
	var terminal, disposition sql.NullString
	var auditID sql.NullInt64
	var created databaseTimestamp
	err := row.Scan(&value.TransactionID, &seq, &value.EventUUID, &value.EventType, &value.EventSchemaID, &schemaVersion, &value.PreviousTxEventDigest, &value.EventDigest, &value.CanonicalEvent, &terminal, &disposition, &auditID, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return B5TxEvent{}, ErrNotFound
	}
	if err != nil {
		return B5TxEvent{}, err
	}
	value.TransactionSeq = uint64(seq)
	value.EventSchemaVersion = uint16(schemaVersion)
	value.TerminalEvidenceText = stringPointer(terminal)
	value.DispositionProofText = stringPointer(disposition)
	value.AuditLogID = int64Pointer(auditID)
	value.CreatedAt = created.time
	return value, nil
}

func nullableBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}
func optionalUint32(value *uint32) any {
	if value == nil {
		return nil
	}
	return int64(*value)
}
func uint32Pointer(value sql.NullInt64) *uint32 {
	if !value.Valid {
		return nil
	}
	result := uint32(value.Int64)
	return &result
}
