package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/auditchain"
	"github.com/cuipengdba/agentsql/internal/model"
)

const (
	defaultPostgresBackfillBatchSize = 2000
	defaultSQLiteBackfillBatchSize   = 500
	defaultBackfillLease             = 2 * time.Minute
	defaultRenewBeforeFraction       = 0.6
	backfillVerifyPageSize           = 500

	backfillReasonHalfChained    = "half_chained"
	backfillReasonSequenceGap    = "seq_gap"
	backfillReasonDuplicate      = "seq_duplicate"
	backfillReasonPrevMismatch   = "prev_mismatch"
	backfillReasonSelfMismatch   = "self_mismatch"
	backfillReasonHeadMismatch   = "head_mismatch"
	backfillReasonKeyUnavailable = "key_unavailable"
	backfillReasonBadFormat      = "bad_format"
)

// BackfillConfig controls bounded backfill transactions and build-lease renewal.
type BackfillConfig struct {
	BatchSize           int
	Lease               time.Duration
	RenewBeforeFraction float64
}

// BackfillResult describes the frontier reached by one complete Run call.
type BackfillResult struct {
	Linked             int64
	RemainingUnchained int64
	HeadSeq            int64
	Complete           bool
}

// BackfillService links historical audit rows while a chain remains BUILDING.
type BackfillService struct {
	repositoryBase
	chainID string
	keys    ChainKeyProvider
	cfg     BackfillConfig
}

// NewBackfillService constructs a backfill service for one audit-chain domain.
func NewBackfillService(
	db *sql.DB,
	dialect Dialect,
	chainID string,
	keys ChainKeyProvider,
	cfg BackfillConfig,
) *BackfillService {
	return &BackfillService{
		repositoryBase: repositoryBase{db: db, dialect: dialect},
		chainID:        chainID,
		keys:           keys,
		cfg:            normalizeBackfillConfig(dialect, cfg),
	}
}

// Run verifies the existing prefix, then links every currently unchained row
// in bounded transactions. It deliberately leaves the chain in BUILDING.
func (service *BackfillService) Run(ctx context.Context, owner string, epoch int) (BackfillResult, error) {
	if err := service.validate(ctx, owner, "run"); err != nil {
		return BackfillResult{}, err
	}
	headSeq, reason, err := service.VerifyPrefix(ctx, owner, epoch)
	if err != nil {
		return BackfillResult{HeadSeq: headSeq}, err
	}
	if reason != "" {
		return BackfillResult{HeadSeq: headSeq}, fmt.Errorf("audit chain %q prefix verification failed: %s", service.chainID, reason)
	}

	result := BackfillResult{HeadSeq: headSeq}
	for {
		batch, err := service.runBatch(ctx, owner, epoch)
		if err != nil {
			return result, err
		}
		result.Linked += batch.Linked
		result.RemainingUnchained = batch.RemainingUnchained
		result.HeadSeq = batch.HeadSeq
		result.Complete = batch.Complete
		if batch.Complete {
			return result, nil
		}
	}
}

// VerifyPrefix verifies the currently linked BUILDING prefix from genesis to
// the state head. A structural or cryptographic break is returned as a stable
// reason string rather than as an operational error.
func (service *BackfillService) VerifyPrefix(ctx context.Context, owner string, epoch int) (headSeq int64, reason string, err error) {
	if err := service.validate(ctx, owner, "verify prefix"); err != nil {
		return 0, "", err
	}

	deadline := time.Now().Add(chainAppendRetryBudget)
	backoff := chainAppendMinBackoff
	for {
		headSeq, reason, err = service.verifyPrefixAttempt(ctx, owner, epoch)
		if err == nil || !service.retryable(err) || time.Now().After(deadline) {
			return headSeq, reason, err
		}
		if err = waitChainAppendRetry(ctx, backoff); err != nil {
			return headSeq, "", err
		}
		backoff = nextChainBackoff(backoff)
	}
}

func normalizeBackfillConfig(dialect Dialect, cfg BackfillConfig) BackfillConfig {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = defaultSQLiteBackfillBatchSize
		if dialect == DialectPostgres {
			cfg.BatchSize = defaultPostgresBackfillBatchSize
		}
	}
	if cfg.Lease <= 0 {
		cfg.Lease = defaultBackfillLease
	}
	if cfg.RenewBeforeFraction <= 0 || cfg.RenewBeforeFraction >= 1 {
		cfg.RenewBeforeFraction = defaultRenewBeforeFraction
	}
	return cfg
}

func (service *BackfillService) validate(ctx context.Context, owner, operation string) error {
	if ctx == nil {
		return fmt.Errorf("%s audit chain backfill: %w", operation, ErrNilContext)
	}
	if service == nil || service.db == nil {
		return fmt.Errorf("%s audit chain backfill: service is not initialized", operation)
	}
	if strings.TrimSpace(service.chainID) == "" {
		return fmt.Errorf("%s audit chain backfill: chain ID is required", operation)
	}
	if strings.TrimSpace(owner) == "" {
		return fmt.Errorf("%s audit chain backfill %q: owner is required", operation, service.chainID)
	}
	if service.dialect != DialectSQLite && service.dialect != DialectPostgres {
		return fmt.Errorf("%s audit chain backfill %q: unsupported dialect %q", operation, service.chainID, service.dialect)
	}
	return nil
}

func (service *BackfillService) runBatch(ctx context.Context, owner string, epoch int) (BackfillResult, error) {
	deadline := time.Now().Add(chainAppendRetryBudget)
	backoff := chainAppendMinBackoff
	for {
		result, err := service.runBatchAttempt(ctx, owner, epoch)
		if err == nil || !service.retryable(err) || time.Now().After(deadline) {
			return result, err
		}
		if err := waitChainAppendRetry(ctx, backoff); err != nil {
			return BackfillResult{}, err
		}
		backoff = nextChainBackoff(backoff)
	}
}

func nextChainBackoff(backoff time.Duration) time.Duration {
	if backoff >= chainAppendMaxBackoff {
		return chainAppendMaxBackoff
	}
	backoff *= 2
	if backoff > chainAppendMaxBackoff {
		return chainAppendMaxBackoff
	}
	return backoff
}

func (service *BackfillService) retryable(err error) bool {
	repository := &AuditLogRepository{repositoryBase: service.repositoryBase}
	return repository.retryableChainAppend(err)
}

func (service *BackfillService) runBatchAttempt(
	ctx context.Context,
	owner string,
	epoch int,
) (result BackfillResult, resultErr error) {
	appendRepository := service.appendRepository()
	transaction, err := appendRepository.beginChainTransaction(ctx)
	if err != nil {
		return BackfillResult{}, fmt.Errorf("begin audit chain backfill transaction: %w", err)
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, transaction.Rollback(ctx))
		}
	}()

	state, err := service.lockAndFence(ctx, transaction, owner, epoch)
	if err != nil {
		return BackfillResult{}, err
	}
	if err := service.renewLeaseIfNeeded(ctx, transaction, &state, owner, epoch); err != nil {
		return BackfillResult{}, err
	}
	mode, keyVersion, key, err := appendRepository.chainHashParameters(ctx, state)
	if err != nil {
		return BackfillResult{}, fmt.Errorf("load audit chain hash parameters: %w", err)
	}
	if state.ChainInstanceID == nil {
		return BackfillResult{}, fmt.Errorf("chain_state %q has no chain_instance_id", service.chainID)
	}

	logs, err := service.selectUnchainedBatch(ctx, transaction)
	if err != nil {
		return BackfillResult{}, err
	}
	if len(logs) == 0 {
		if err := service.renewLeaseIfNeeded(ctx, transaction, &state, owner, epoch); err != nil {
			return BackfillResult{}, err
		}
		remaining, err := service.countUnchained(ctx, transaction)
		if err != nil {
			return BackfillResult{}, err
		}
		if err := transaction.Commit(ctx); err != nil {
			return BackfillResult{}, fmt.Errorf("commit completed audit chain backfill: %w", err)
		}
		return BackfillResult{RemainingUnchained: remaining, HeadSeq: state.HeadSeq, Complete: remaining == 0}, nil
	}

	sequence := state.HeadSeq
	previousHash := auditchain.GenesisPrevHex
	if sequence > 0 {
		if state.HeadHash == nil {
			return BackfillResult{}, fmt.Errorf("chain_state %q head_seq %d has no head_hash", service.chainID, sequence)
		}
		previousHash = *state.HeadHash
	}
	var headID int64
	for index, auditLog := range logs {
		sequence++
		selfHash, err := appendRepository.hashAuditLog(state, auditLog, mode, keyVersion, sequence, previousHash, key)
		if err != nil {
			return BackfillResult{}, fmt.Errorf("hash backfill row %d (batch item %d): %w", auditLog.ID, index, err)
		}
		if err := appendRepository.updateAuditChainColumns(ctx, transaction, auditLog.ID, sequence, previousHash, selfHash, keyVersion); err != nil {
			return BackfillResult{}, fmt.Errorf("link backfill row %d (batch item %d): %w", auditLog.ID, index, err)
		}
		previousHash = selfHash
		headID = auditLog.ID
	}

	if err := service.updateFrontier(ctx, transaction, &state, owner, epoch, sequence, headID, previousHash); err != nil {
		return BackfillResult{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return BackfillResult{}, fmt.Errorf("commit audit chain backfill batch: %w", err)
	}
	return BackfillResult{Linked: int64(len(logs)), HeadSeq: sequence}, nil
}

func (service *BackfillService) verifyPrefixAttempt(
	ctx context.Context,
	owner string,
	epoch int,
) (headSeq int64, reason string, resultErr error) {
	appendRepository := service.appendRepository()
	transaction, err := service.beginVerificationTransaction(ctx)
	if err != nil {
		return 0, "", fmt.Errorf("begin audit chain prefix verification: %w", err)
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, transaction.Rollback(ctx))
		}
	}()

	state, err := service.lockAndFence(ctx, transaction, owner, epoch)
	if err != nil {
		return 0, "", err
	}
	headSeq = state.HeadSeq
	if state.ChainInstanceID == nil || state.Mode == nil {
		return service.finishVerification(ctx, transaction, state, owner, epoch, backfillReasonBadFormat)
	}
	if *state.Mode != "keyless" && *state.Mode != "hmac" {
		return service.finishVerification(ctx, transaction, state, owner, epoch, backfillReasonBadFormat)
	}
	mode, _, initialKey, err := appendRepository.chainHashParameters(ctx, state)
	if err != nil {
		return service.finishVerification(ctx, transaction, state, owner, epoch, backfillReasonKeyUnavailable)
	}
	if err := service.renewLeaseIfNeeded(ctx, transaction, &state, owner, epoch); err != nil {
		return 0, "", err
	}

	halfChained, err := service.hasHalfChainedRow(ctx, transaction)
	if err != nil {
		return 0, "", err
	}
	if halfChained {
		return service.finishVerification(ctx, transaction, state, owner, epoch, backfillReasonHalfChained)
	}

	expectedSequence := int64(1)
	previousHash := auditchain.GenesisPrevHex
	lastID := int64(0)
	cursorSequence := int64(math.MinInt64)
	cursorID := int64(0)
	for {
		rows, err := service.selectLinkedVerificationPage(ctx, transaction, cursorSequence, cursorID)
		if err != nil {
			return 0, "", err
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			cursorSequence, cursorID = row.sequence, row.auditLog.ID
			if row.sequence < expectedSequence {
				return service.finishVerification(ctx, transaction, state, owner, epoch, backfillReasonDuplicate)
			}
			if row.sequence > expectedSequence {
				return service.finishVerification(ctx, transaction, state, owner, epoch, backfillReasonSequenceGap)
			}
			if row.formatVersion != auditchain.ChainFormatVersionV1 || !validLowerHexHash(row.previousHash) || !validLowerHexHash(row.selfHash) {
				return service.finishVerification(ctx, transaction, state, owner, epoch, backfillReasonBadFormat)
			}
			if row.previousHash != previousHash {
				return service.finishVerification(ctx, transaction, state, owner, epoch, backfillReasonPrevMismatch)
			}

			key := initialKey
			switch mode {
			case auditchain.AlgorithmSHA256:
				if row.keyVersion != 0 {
					return service.finishVerification(ctx, transaction, state, owner, epoch, backfillReasonBadFormat)
				}
			case auditchain.AlgorithmHMACSHA256:
				if row.keyVersion != 1 {
					return service.finishVerification(ctx, transaction, state, owner, epoch, backfillReasonKeyUnavailable)
				}
			default:
				return service.finishVerification(ctx, transaction, state, owner, epoch, backfillReasonBadFormat)
			}
			computed, err := appendRepository.hashAuditLog(state, row.auditLog, mode, row.keyVersion, row.sequence, row.previousHash, key)
			if err != nil {
				return service.finishVerification(ctx, transaction, state, owner, epoch, backfillReasonBadFormat)
			}
			if computed != row.selfHash {
				return service.finishVerification(ctx, transaction, state, owner, epoch, backfillReasonSelfMismatch)
			}
			previousHash = row.selfHash
			lastID = row.auditLog.ID
			expectedSequence++
		}
		if err := service.renewLeaseIfNeeded(ctx, transaction, &state, owner, epoch); err != nil {
			return 0, "", err
		}
	}

	verifiedHead := expectedSequence - 1
	if verifiedHead != state.HeadSeq || !headMatches(state, verifiedHead, lastID, previousHash) {
		return service.finishVerification(ctx, transaction, state, owner, epoch, backfillReasonHeadMismatch)
	}
	return service.finishVerification(ctx, transaction, state, owner, epoch, "")
}

func (service *BackfillService) beginVerificationTransaction(ctx context.Context) (chainTransaction, error) {
	if service.dialect != DialectPostgres {
		return service.appendRepository().beginChainTransaction(ctx)
	}
	transaction, err := service.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	return postgresChainTransaction{Tx: transaction}, nil
}

func (service *BackfillService) finishVerification(
	ctx context.Context,
	transaction chainTransaction,
	state ChainState,
	owner string,
	epoch int,
	reason string,
) (int64, string, error) {
	if err := service.renewLeaseIfNeeded(ctx, transaction, &state, owner, epoch); err != nil {
		return 0, "", err
	}
	if err := transaction.Commit(ctx); err != nil {
		return 0, "", fmt.Errorf("commit audit chain prefix verification: %w", err)
	}
	return state.HeadSeq, reason, nil
}

func (service *BackfillService) appendRepository() *AuditLogRepository {
	return &AuditLogRepository{
		repositoryBase: service.repositoryBase,
		chainID:        service.chainID,
		keys:           service.keys,
	}
}

func (service *BackfillService) lockAndFence(
	ctx context.Context,
	transaction chainTransaction,
	owner string,
	epoch int,
) (ChainState, error) {
	buildRepository := &ChainBuildRepository{repositoryBase: service.repositoryBase}
	state, err := buildRepository.lockState(ctx, transaction, service.chainID)
	if err != nil {
		return ChainState{}, err
	}
	if err := VerifyFencing(state, owner, epoch); err != nil {
		return ChainState{}, fmt.Errorf("backfill chain %q: %w", service.chainID, err)
	}
	now := time.Now().UTC()
	if state.BuildLeaseUntil == nil || !state.BuildLeaseUntil.After(now) {
		return ChainState{}, fmt.Errorf("backfill chain %q lease expired: %w", service.chainID, ErrStaleBuildFencing)
	}
	return state, nil
}

func (service *BackfillService) renewLeaseIfNeeded(
	ctx context.Context,
	transaction chainTransaction,
	state *ChainState,
	owner string,
	epoch int,
) error {
	now := time.Now().UTC()
	if state.BuildLeaseUntil == nil || !state.BuildLeaseUntil.After(now) {
		return fmt.Errorf("renew backfill lease %q: %w", service.chainID, ErrStaleBuildFencing)
	}
	remaining := state.BuildLeaseUntil.Sub(now)
	renewThreshold := time.Duration(float64(service.cfg.Lease) * (1 - service.cfg.RenewBeforeFraction))
	if remaining > renewThreshold {
		return nil
	}
	leaseUntil := now.Add(service.cfg.Lease)
	result, err := transaction.ExecContext(ctx, service.bind(`
UPDATE chain_state
SET build_lease_until = ?, updated_at = ?
WHERE chain_id = ? AND status = 'BUILDING' AND build_owner = ? AND build_epoch = ?
  AND build_lease_until > ?`), leaseUntil, now, service.chainID, owner, epoch, now)
	if err != nil {
		return fmt.Errorf("renew backfill lease %q: %w", service.chainID, err)
	}
	if err := requireSingleBuildRow(result, "renew backfill lease"); err != nil {
		return fmt.Errorf("renew backfill lease %q: %w", service.chainID, err)
	}
	state.BuildLeaseUntil = &leaseUntil
	return nil
}

func (service *BackfillService) updateFrontier(
	ctx context.Context,
	transaction chainTransaction,
	state *ChainState,
	owner string,
	epoch int,
	sequence, headID int64,
	headHash string,
) error {
	now := time.Now().UTC()
	if state.BuildLeaseUntil == nil || !state.BuildLeaseUntil.After(now) {
		return fmt.Errorf("update backfill frontier %q: %w", service.chainID, ErrStaleBuildFencing)
	}
	leaseUntil := *state.BuildLeaseUntil
	remaining := leaseUntil.Sub(now)
	renewThreshold := time.Duration(float64(service.cfg.Lease) * (1 - service.cfg.RenewBeforeFraction))
	if remaining <= renewThreshold {
		leaseUntil = now.Add(service.cfg.Lease)
	}
	result, err := transaction.ExecContext(ctx, service.bind(`
UPDATE chain_state
SET head_seq = ?, head_id = ?, head_hash = ?,
    last_built_id = ?, last_built_seq = ?, last_built_hash = ?,
    build_lease_until = ?, updated_at = ?
WHERE chain_id = ? AND status = 'BUILDING' AND build_owner = ? AND build_epoch = ?
  AND build_lease_until > ?`),
		sequence, headID, headHash,
		headID, sequence, headHash,
		leaseUntil, now,
		service.chainID, owner, epoch, now)
	if err != nil {
		return fmt.Errorf("update backfill frontier %q: %w", service.chainID, err)
	}
	if err := requireSingleBuildRow(result, "update backfill frontier"); err != nil {
		return fmt.Errorf("update backfill frontier %q: %w", service.chainID, err)
	}
	state.BuildLeaseUntil = &leaseUntil
	return nil
}

func (service *BackfillService) selectUnchainedBatch(ctx context.Context, transaction chainTransaction) ([]model.AuditLog, error) {
	tenantID, tenantErr := service.requireTenant(ctx, "select unchained audit rows")
	if tenantErr != nil {
		return nil, tenantErr
	}
	query := auditBusinessColumnsSQL + `
FROM audit_logs
WHERE tenant_id = ? AND chain_seq IS NULL
ORDER BY id ASC
LIMIT ?`
	if service.dialect == DialectPostgres {
		query += " FOR UPDATE"
	}
	rows, err := transaction.QueryContext(ctx, service.bind(query), tenantID, service.cfg.BatchSize)
	if err != nil {
		return nil, fmt.Errorf("select unchained audit rows: %w", err)
	}
	logs := make([]model.AuditLog, 0, service.cfg.BatchSize)
	for rows.Next() {
		auditLog, err := scanAuditLog(rows)
		if err != nil {
			return nil, fmt.Errorf("scan unchained audit row: %w", closeRowsAfterError(rows, err))
		}
		logs = append(logs, auditLog)
	}
	iterationErr := rows.Err()
	closeErr := rows.Close()
	if iterationErr != nil || closeErr != nil {
		return nil, fmt.Errorf("finish unchained audit rows: %w", errors.Join(iterationErr, closeErr))
	}
	return logs, nil
}

func (service *BackfillService) countUnchained(ctx context.Context, transaction chainTransaction) (int64, error) {
	tenantID, tenantErr := service.requireTenant(ctx, "count unchained audit rows")
	if tenantErr != nil {
		return 0, tenantErr
	}
	var count int64
	if err := transaction.QueryRowContext(ctx, service.bind(`SELECT COUNT(*) FROM audit_logs WHERE tenant_id = ? AND chain_seq IS NULL`), tenantID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count unchained audit rows: %w", err)
	}
	return count, nil
}

func (service *BackfillService) hasHalfChainedRow(ctx context.Context, transaction chainTransaction) (bool, error) {
	tenantID, tenantErr := service.requireTenant(ctx, "detect half-chained audit rows")
	if tenantErr != nil {
		return false, tenantErr
	}
	var id int64
	err := transaction.QueryRowContext(ctx, service.bind(`
SELECT id
FROM audit_logs
WHERE tenant_id = ? AND NOT (
        chain_seq IS NULL AND prev_hash IS NULL AND self_hash IS NULL
        AND chain_key_version IS NULL AND chain_format_version IS NULL
      )
  AND NOT (
        chain_seq IS NOT NULL AND prev_hash IS NOT NULL AND self_hash IS NOT NULL
        AND chain_key_version IS NOT NULL AND chain_format_version IS NOT NULL
      )
LIMIT 1`), tenantID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("detect half-chained audit rows: %w", err)
	}
	return true, nil
}

type backfillVerificationRow struct {
	auditLog      model.AuditLog
	sequence      int64
	previousHash  string
	selfHash      string
	keyVersion    int
	formatVersion int64
}

func (service *BackfillService) selectLinkedVerificationPage(
	ctx context.Context,
	transaction chainTransaction,
	cursorSequence, cursorID int64,
) ([]backfillVerificationRow, error) {
	tenantID, tenantErr := service.requireTenant(ctx, "select linked audit rows")
	if tenantErr != nil {
		return nil, tenantErr
	}
	query := auditBusinessColumnsSQL + `,
       chain_seq, prev_hash, self_hash, chain_key_version, chain_format_version
FROM audit_logs
WHERE tenant_id = ? AND chain_seq IS NOT NULL
  AND (chain_seq > ? OR (chain_seq = ? AND id > ?))
ORDER BY chain_seq ASC, id ASC
LIMIT ?`
	rows, err := transaction.QueryContext(
		ctx,
		service.bind(query),
		tenantID,
		cursorSequence,
		cursorSequence,
		cursorID,
		backfillVerifyPageSize,
	)
	if err != nil {
		return nil, fmt.Errorf("select linked audit rows for verification: %w", err)
	}
	page := make([]backfillVerificationRow, 0, backfillVerifyPageSize)
	for rows.Next() {
		row, err := scanBackfillVerificationRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scan linked audit row: %w", closeRowsAfterError(rows, err))
		}
		page = append(page, row)
	}
	iterationErr := rows.Err()
	closeErr := rows.Close()
	if iterationErr != nil || closeErr != nil {
		return nil, fmt.Errorf("finish linked audit rows: %w", errors.Join(iterationErr, closeErr))
	}
	return page, nil
}

const auditBusinessColumnsSQL = `
SELECT id, tenant_id, ts, agent_id, datasource_id, session_id, conversation_id, mcp_tool,
       db_type, sql_raw, sql_norm, stmt_type, objects, decision, rule_hits,
       risk_level, est_rows, rows_returned, latency_ms, client_ip, model_name,
       error_msg, error_code, action, actor_type, actor_id, details_json, event_uuid`

func scanBackfillVerificationRow(scanner rowScanner) (backfillVerificationRow, error) {
	var row backfillVerificationRow
	var timestamp databaseTimestamp
	var agentID, datasourceID, sessionID, conversationID sql.NullString
	var mcpTool, databaseType, sqlRaw, sqlNormalized, statementType sql.NullString
	var objects, ruleHits, clientIP, modelName, errorMessage, errorCode sql.NullString
	var action, actorType, actorID, detailsJSON, eventUUID sql.NullString
	var riskLevel, estimatedRows, rowsReturned, latencyMS sql.NullInt64
	if err := scanner.Scan(
		&row.auditLog.ID, &row.auditLog.TenantID, &timestamp,
		&agentID, &datasourceID, &sessionID, &conversationID,
		&mcpTool, &databaseType, &sqlRaw, &sqlNormalized, &statementType,
		&objects, &row.auditLog.Decision, &ruleHits,
		&riskLevel, &estimatedRows, &rowsReturned, &latencyMS,
		&clientIP, &modelName, &errorMessage, &errorCode,
		&action, &actorType, &actorID, &detailsJSON, &eventUUID,
		&row.sequence, &row.previousHash, &row.selfHash, &row.keyVersion, &row.formatVersion,
	); err != nil {
		return backfillVerificationRow{}, err
	}
	row.auditLog.AgentID = stringPointer(agentID)
	row.auditLog.DatasourceID = stringPointer(datasourceID)
	row.auditLog.SessionID = stringPointer(sessionID)
	row.auditLog.ConversationID = stringPointer(conversationID)
	row.auditLog.MCPTool = stringPointer(mcpTool)
	row.auditLog.DBType = stringPointer(databaseType)
	row.auditLog.SQLRaw = stringPointer(sqlRaw)
	row.auditLog.SQLNorm = stringPointer(sqlNormalized)
	row.auditLog.StmtType = stringPointer(statementType)
	row.auditLog.Objects = stringPointer(objects)
	row.auditLog.RuleHits = stringPointer(ruleHits)
	var err error
	row.auditLog.RiskLevel, err = intPointer(riskLevel)
	if err != nil {
		return backfillVerificationRow{}, err
	}
	row.auditLog.EstRows = int64Pointer(estimatedRows)
	row.auditLog.RowsReturned, err = intPointer(rowsReturned)
	if err != nil {
		return backfillVerificationRow{}, err
	}
	row.auditLog.LatencyMS = int64Pointer(latencyMS)
	row.auditLog.ClientIP = stringPointer(clientIP)
	row.auditLog.ModelName = stringPointer(modelName)
	row.auditLog.ErrorMsg = stringPointer(errorMessage)
	row.auditLog.ErrorCode = stringPointer(errorCode)
	row.auditLog.Action = stringPointer(action)
	row.auditLog.ActorType = stringPointer(actorType)
	row.auditLog.ActorID = stringPointer(actorID)
	row.auditLog.DetailsJSON = stringPointer(detailsJSON)
	row.auditLog.EventUUID = stringPointer(eventUUID)
	row.auditLog.TS, err = timestamp.required("audit_logs.ts")
	if err != nil {
		return backfillVerificationRow{}, err
	}
	return row, nil
}

func validLowerHexHash(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func headMatches(state ChainState, verifiedHead, lastID int64, lastHash string) bool {
	if verifiedHead == 0 {
		return state.HeadID == nil && state.HeadHash == nil
	}
	return state.HeadID != nil && *state.HeadID == lastID &&
		state.HeadHash != nil && *state.HeadHash == lastHash
}
