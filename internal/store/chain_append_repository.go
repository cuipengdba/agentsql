package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/auditchain"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/jackc/pgx/v5/pgconn"
	modernsqlite "modernc.org/sqlite"
)

const (
	chainAppendRetryBudget = 15 * time.Second
	chainAppendMinBackoff  = 25 * time.Millisecond
	chainAppendMaxBackoff  = time.Second
)

var errChainAppendRetry = errors.New("audit chain state changed during append")

// ChainKeyProvider returns trusted HMAC key material for a frozen chain key
// version. It must never silently substitute a keyless mode for a missing key.
type ChainKeyProvider interface {
	ChainKeyForVersion(ctx context.Context, version int) ([]byte, error)
}

// AppendBatch appends logs in input order. When chaining is enabled, the
// complete batch and the single head update are committed atomically.
func (repository *AuditLogRepository) AppendBatch(ctx context.Context, logs []model.AuditLog) ([]model.AuditLog, error) {
	if repository == nil || repository.db == nil {
		return nil, fmt.Errorf("append audit log batch: repository is not initialized")
	}
	if ctx == nil {
		return nil, fmt.Errorf("append audit log batch: %w", ErrNilContext)
	}
	for index, auditLog := range logs {
		if err := validateAuditLogInsert(ctx, auditLog); err != nil {
			return nil, fmt.Errorf("append audit log batch item %d: %w", index, err)
		}
	}
	if len(logs) == 0 {
		return []model.AuditLog{}, nil
	}

	state, enabled, err := repository.chainStateForAppend(ctx)
	if err != nil {
		return nil, err
	}
	if enabled {
		inserted, err := repository.chainInsertBatch(ctx, state, logs)
		if err != nil {
			return nil, fmt.Errorf("append audit log batch on %s chain: %w", repository.chainID, err)
		}
		return inserted, nil
	}

	inserted := make([]model.AuditLog, 0, len(logs))
	for index, auditLog := range logs {
		row, err := insertAuditLog(ctx, repository.db, repository.dialect, auditLog)
		if err != nil {
			return nil, fmt.Errorf("append audit log batch item %d: %w", index, err)
		}
		inserted = append(inserted, row)
	}
	return inserted, nil
}

func (repository *AuditLogRepository) chainStateForAppend(ctx context.Context) (ChainState, bool, error) {
	// A zero chain ID is retained as a compatibility escape hatch for legacy
	// package-local repository construction. Store constructors always inject
	// an explicit management or traffic domain.
	if repository.chainID == "" {
		return ChainState{}, false, nil
	}
	stateRepository := &ChainStateRepository{repositoryBase: repository.repositoryBase}
	state, err := stateRepository.Get(ctx, repository.chainID)
	if errors.Is(err, ErrNotFound) {
		return ChainState{}, false, nil
	}
	if err != nil {
		return ChainState{}, false, fmt.Errorf("read audit chain state %q: %w", repository.chainID, err)
	}
	switch state.Status {
	case "DISABLED":
		return state, false, nil
	case "BUILDING", "ACTIVE":
		return state, true, nil
	default:
		return ChainState{}, false, fmt.Errorf("audit chain %q has non-writable status %q", repository.chainID, state.Status)
	}
}

func (repository *AuditLogRepository) chainInsertOne(
	ctx context.Context,
	observed ChainState,
	auditLog model.AuditLog,
) (model.AuditLog, error) {
	inserted, err := repository.chainInsertBatch(ctx, observed, []model.AuditLog{auditLog})
	if err != nil {
		return model.AuditLog{}, err
	}
	return inserted[0], nil
}

func (repository *AuditLogRepository) chainInsertBatch(
	ctx context.Context,
	observed ChainState,
	logs []model.AuditLog,
) ([]model.AuditLog, error) {
	deadline := time.Now().Add(chainAppendRetryBudget)
	backoff := chainAppendMinBackoff
	for {
		inserted, err := repository.chainInsertBatchAttempt(ctx, observed, logs)
		if err == nil {
			return inserted, nil
		}
		if !repository.retryableChainAppend(err) || time.Now().After(deadline) {
			return nil, err
		}
		if err := waitChainAppendRetry(ctx, backoff); err != nil {
			return nil, err
		}
		if backoff < chainAppendMaxBackoff {
			backoff *= 2
			if backoff > chainAppendMaxBackoff {
				backoff = chainAppendMaxBackoff
			}
		}
		state, enabled, stateErr := repository.chainStateForAppend(ctx)
		if stateErr != nil {
			return nil, stateErr
		}
		if !enabled {
			return nil, fmt.Errorf("audit chain %q became disabled while appending", repository.chainID)
		}
		observed = state
	}
}

func (repository *AuditLogRepository) chainInsertBatchAttempt(
	ctx context.Context,
	observed ChainState,
	logs []model.AuditLog,
) (inserted []model.AuditLog, resultErr error) {
	transaction, err := repository.beginChainTransaction(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin chain append transaction: %w", err)
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, transaction.Rollback(ctx))
		}
	}()

	locked, err := repository.lockChainState(ctx, transaction)
	if err != nil {
		return nil, err
	}
	if !sameAppendState(observed, locked) {
		return nil, errChainAppendRetry
	}
	mode, keyVersion, key, err := repository.chainHashParameters(ctx, locked)
	if err != nil {
		return nil, err
	}
	if locked.ChainInstanceID == nil {
		return nil, fmt.Errorf("chain_state %q has no chain_instance_id", repository.chainID)
	}

	sequence := locked.HeadSeq
	previousHash := auditchain.GenesisPrevHex
	if sequence > 0 {
		if locked.HeadHash == nil {
			return nil, fmt.Errorf("chain_state %q head_seq %d has no head_hash", repository.chainID, sequence)
		}
		previousHash = *locked.HeadHash
	}
	inserted = make([]model.AuditLog, 0, len(logs))
	var headID int64
	for index, auditLog := range logs {
		sequence++
		var row model.AuditLog
		if locked.Status == "ACTIVE" {
			row, err = insertActiveAuditLog(
				ctx, transaction, repository.dialect, auditLog, sequence, previousHash, keyVersion,
			)
		} else {
			row, err = insertAuditLog(ctx, transaction, repository.dialect, auditLog)
		}
		if err != nil {
			return nil, fmt.Errorf("insert batch item %d before hashing: %w", index, err)
		}
		selfHash, err := repository.hashAuditLog(locked, row, mode, keyVersion, sequence, previousHash, key)
		if err != nil {
			return nil, fmt.Errorf("hash batch item %d: %w", index, err)
		}
		if err := repository.updateAuditChainColumns(ctx, transaction, row.ID, sequence, previousHash, selfHash, keyVersion); err != nil {
			return nil, fmt.Errorf("update batch item %d chain columns: %w", index, err)
		}
		previousHash = selfHash
		headID = row.ID
		inserted = append(inserted, row)
	}
	if err := repository.updateChainHead(ctx, transaction, sequence, headID, previousHash); err != nil {
		return nil, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit chain append transaction: %w", err)
	}
	return inserted, nil
}

type chainTransaction interface {
	sqlExecutor
	Commit(context.Context) error
	Rollback(context.Context) error
}

type postgresChainTransaction struct {
	*sql.Tx
}

func (transaction postgresChainTransaction) Commit(_ context.Context) error {
	return transaction.Tx.Commit()
}

func (transaction postgresChainTransaction) Rollback(_ context.Context) error {
	err := transaction.Tx.Rollback()
	if errors.Is(err, sql.ErrTxDone) {
		return nil
	}
	return err
}

type sqliteChainTransaction struct {
	connection *sql.Conn
	finished   bool
}

func (transaction *sqliteChainTransaction) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return transaction.connection.ExecContext(ctx, query, args...)
}

func (transaction *sqliteChainTransaction) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return transaction.connection.QueryRowContext(ctx, query, args...)
}

func (transaction *sqliteChainTransaction) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return transaction.connection.QueryContext(ctx, query, args...)
}

func (transaction *sqliteChainTransaction) Commit(ctx context.Context) error {
	if transaction.finished {
		return nil
	}
	if _, err := transaction.connection.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	transaction.finished = true
	return transaction.connection.Close()
}

func (transaction *sqliteChainTransaction) Rollback(ctx context.Context) error {
	if transaction.finished {
		return nil
	}
	_, err := transaction.connection.ExecContext(ctx, "ROLLBACK")
	transaction.finished = true
	closeErr := transaction.connection.Close()
	return errors.Join(err, closeErr)
}

func (repository *AuditLogRepository) beginChainTransaction(ctx context.Context) (chainTransaction, error) {
	switch repository.dialect {
	case DialectPostgres:
		transaction, err := repository.db.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		return postgresChainTransaction{Tx: transaction}, nil
	case DialectSQLite:
		connection, err := repository.db.Conn(ctx)
		if err != nil {
			return nil, err
		}
		if _, err := connection.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
			_ = connection.Close()
			return nil, fmt.Errorf("set SQLite busy timeout: %w", err)
		}
		if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
			_ = connection.Close()
			return nil, err
		}
		return &sqliteChainTransaction{connection: connection}, nil
	default:
		return nil, fmt.Errorf("unsupported audit store dialect %q", repository.dialect)
	}
}

func (repository *AuditLogRepository) lockChainState(ctx context.Context, transaction sqlExecutor) (ChainState, error) {
	query := `
SELECT chain_id, chain_instance_id, status, mode, head_seq, head_id, head_hash,
       genesis_at, protected_since_id, build_owner, build_lease_until, build_epoch,
       last_built_id, last_built_seq, last_built_hash, updated_at
FROM chain_state
WHERE chain_id = ?`
	if repository.dialect == DialectPostgres {
		query += " FOR UPDATE"
	}
	state, err := scanChainState(transaction.QueryRowContext(ctx, repository.bind(query), repository.chainID))
	if errors.Is(err, sql.ErrNoRows) {
		return ChainState{}, fmt.Errorf("lock chain state %q: %w", repository.chainID, errors.Join(ErrNotFound, err))
	}
	if err != nil {
		return ChainState{}, fmt.Errorf("lock chain state %q: %w", repository.chainID, err)
	}
	if state.Status != "BUILDING" && state.Status != "ACTIVE" {
		return ChainState{}, fmt.Errorf("chain_state %q changed to non-writable status %q", repository.chainID, state.Status)
	}
	return state, nil
}

func sameAppendState(left, right ChainState) bool {
	return left.ChainID == right.ChainID &&
		left.Status == right.Status &&
		equalStringPointers(left.Mode, right.Mode) &&
		equalStringPointers(left.ChainInstanceID, right.ChainInstanceID) &&
		left.HeadSeq == right.HeadSeq &&
		equalInt64Pointers(left.HeadID, right.HeadID) &&
		equalStringPointers(left.HeadHash, right.HeadHash)
}

func (repository *AuditLogRepository) chainHashParameters(ctx context.Context, state ChainState) (string, int, []byte, error) {
	if state.Mode == nil {
		return "", 0, nil, fmt.Errorf("chain_state %q has no mode", repository.chainID)
	}
	switch *state.Mode {
	case "keyless":
		return auditchain.AlgorithmSHA256, 0, nil, nil
	case "hmac":
		// S2a has no chain_state key-version column. S2b-1 therefore uses
		// the initial frozen HMAC version; S2b-2 will source this from its
		// build manifest/state when key rotation is introduced.
		const keyVersion = initialHMACChainKeyVersion
		if repository.keys == nil {
			return "", 0, nil, fmt.Errorf("HMAC chain key provider is unavailable for version %d", keyVersion)
		}
		key, err := repository.keys.ChainKeyForVersion(ctx, keyVersion)
		if err != nil {
			return "", 0, nil, fmt.Errorf("load HMAC chain key version %d: %w", keyVersion, err)
		}
		if len(key) == 0 {
			return "", 0, nil, fmt.Errorf("HMAC chain key version %d is empty", keyVersion)
		}
		return auditchain.AlgorithmHMACSHA256, keyVersion, key, nil
	default:
		return "", 0, nil, fmt.Errorf("chain_state %q has unknown mode %q", repository.chainID, *state.Mode)
	}
}

func (repository *AuditLogRepository) hashAuditLog(
	state ChainState,
	auditLog model.AuditLog,
	algorithm string,
	keyVersion int,
	sequence int64,
	previousHash string,
	key []byte,
) (string, error) {
	previousBytes, err := hex.DecodeString(previousHash)
	if err != nil || len(previousBytes) != 32 {
		return "", fmt.Errorf("decode previous hash %q: expected 64 lowercase hex characters", previousHash)
	}
	if previousHash != strings.ToLower(previousHash) {
		return "", fmt.Errorf("previous hash must be lowercase hexadecimal")
	}
	var previous [32]byte
	copy(previous[:], previousBytes)
	canonical, err := auditchain.EncodeCanonical(modelToChainRow(auditLog))
	if err != nil {
		return "", fmt.Errorf("encode canonical audit row %d: %w", auditLog.ID, err)
	}
	envelope := auditchain.NewEnvelope(
		*state.ChainInstanceID,
		repository.chainID,
		algorithm,
		int64(keyVersion),
		sequence,
		previous,
		canonical,
	)
	encoded, err := auditchain.EncodeEnvelope(envelope)
	if err != nil {
		return "", fmt.Errorf("encode audit chain envelope: %w", err)
	}
	selfHash, err := auditchain.HashEnvelope(envelope, encoded, key)
	if err != nil {
		return "", fmt.Errorf("hash audit chain envelope: %w", err)
	}
	return selfHash, nil
}

func modelToChainRow(auditLog model.AuditLog) auditchain.Row {
	return auditchain.Row{
		ID:             auditLog.ID,
		TS:             auditLog.TS.UTC().Format(auditchain.TimestampLayout),
		AgentID:        auditLog.AgentID,
		DatasourceID:   auditLog.DatasourceID,
		SessionID:      auditLog.SessionID,
		ConversationID: auditLog.ConversationID,
		MCPTool:        auditLog.MCPTool,
		DBType:         auditLog.DBType,
		SQLRaw:         auditLog.SQLRaw,
		SQLNorm:        auditLog.SQLNorm,
		StmtType:       auditLog.StmtType,
		Objects:        auditLog.Objects,
		Decision:       auditLog.Decision,
		RuleHits:       auditLog.RuleHits,
		RiskLevel:      intPointerToInt64(auditLog.RiskLevel),
		EstRows:        auditLog.EstRows,
		RowsReturned:   intPointerToInt64(auditLog.RowsReturned),
		LatencyMS:      auditLog.LatencyMS,
		ClientIP:       auditLog.ClientIP,
		ModelName:      auditLog.ModelName,
		ErrorMsg:       auditLog.ErrorMsg,
		ErrorCode:      auditLog.ErrorCode,
		Action:         auditLog.Action,
		ActorType:      auditLog.ActorType,
		ActorID:        auditLog.ActorID,
		DetailsJSON:    auditLog.DetailsJSON,
		EventUUID:      auditLog.EventUUID,
	}
}

func intPointerToInt64(value *int) *int64 {
	if value == nil {
		return nil
	}
	converted := int64(*value)
	return &converted
}

func (repository *AuditLogRepository) updateAuditChainColumns(
	ctx context.Context,
	transaction sqlExecutor,
	id, sequence int64,
	previousHash, selfHash string,
	keyVersion int,
) error {
	result, err := transaction.ExecContext(ctx, repository.bind(`
UPDATE audit_logs
SET chain_seq = ?, prev_hash = ?, self_hash = ?, chain_key_version = ?, chain_format_version = ?
WHERE id = ?`), sequence, previousHash, selfHash, keyVersion, auditchain.ChainFormatVersionV1, id)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read updated row count: %w", err)
	}
	if rows != 1 {
		return fmt.Errorf("audit log %d chain update affected %d rows", id, rows)
	}
	return nil
}

func (repository *AuditLogRepository) updateChainHead(
	ctx context.Context,
	transaction sqlExecutor,
	sequence, headID int64,
	headHash string,
) error {
	result, err := transaction.ExecContext(ctx, repository.bind(`
UPDATE chain_state
SET head_seq = ?, head_id = ?, head_hash = ?, updated_at = CURRENT_TIMESTAMP
WHERE chain_id = ?`), sequence, headID, headHash, repository.chainID)
	if err != nil {
		return fmt.Errorf("update chain head: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read chain head update count: %w", err)
	}
	if rows != 1 {
		return fmt.Errorf("chain head update affected %d rows", rows)
	}
	return nil
}

func (repository *AuditLogRepository) retryableChainAppend(err error) bool {
	if errors.Is(err, errChainAppendRetry) {
		return true
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		if postgresError.Code == "23505" && postgresError.ConstraintName == "ux_audit_logs_event_uuid" {
			return false
		}
		return postgresError.Code == "40001" || postgresError.Code == "40P01" || postgresError.Code == "23505"
	}
	var sqliteError *modernsqlite.Error
	if errors.As(err, &sqliteError) {
		code := sqliteError.Code() & 0xff
		return code == 5 || code == 6
	}
	return false
}

func waitChainAppendRetry(ctx context.Context, backoff time.Duration) error {
	// E11 freezes +/-20% jitter around exponential backoff.
	jitter := 0.8 + rand.Float64()*0.4
	timer := time.NewTimer(time.Duration(float64(backoff) * jitter))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
