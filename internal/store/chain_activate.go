package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/auditchain"
	"github.com/cuipengdba/agentsql/internal/model"
)

const (
	initialHMACChainKeyVersion = 1
	activeSelfHashPlaceholder  = "0000000000000000000000000000000000000000000000000000000000000000"
	activationModeDowngrade    = "mode_downgrade"

	sqliteChainContractInsertTrigger = "trg_audit_logs_chain_contract_insert"
	sqliteChainContractUpdateTrigger = "trg_audit_logs_chain_contract_update"
	chainSequenceUniqueIndex         = "ux_audit_logs_chain_seq"
)

var (
	// ErrChainCoverageIncomplete indicates that not every audit row is fully linked to the state head.
	ErrChainCoverageIncomplete = errors.New("audit chain coverage is incomplete")
	// ErrChainModeDowngrade indicates that database state disagrees with the trusted manifest.
	ErrChainModeDowngrade = errors.New("audit chain mode or key version disagrees with trusted manifest: mode_downgrade")
)

// ChainManifest is the trusted, database-external authority for chain mode and keys.
// Keyless mode is explicit and uses key version zero.
type ChainManifest interface {
	ChainKeyProvider
	ExpectedMode(ctx context.Context) (string, error)
	CurrentKeyVersion(ctx context.Context) (int, error)
}

// ChainProvisioner runs the complete DISABLED/BUILDING to ACTIVE lifecycle.
type ChainProvisioner struct {
	repositoryBase
	chainID  string
	manifest ChainManifest
	cfg      BackfillConfig
}

// NewChainProvisioner constructs an end-to-end chain provisioner.
func NewChainProvisioner(
	db *sql.DB,
	dialect Dialect,
	chainID string,
	manifest ChainManifest,
	cfg BackfillConfig,
) *ChainProvisioner {
	return &ChainProvisioner{
		repositoryBase: repositoryBase{db: db, dialect: dialect},
		chainID:        chainID,
		manifest:       manifest,
		cfg:            normalizeBackfillConfig(dialect, cfg),
	}
}

// Status returns the persisted state for this provisioner's chain.
func (provisioner *ChainProvisioner) Status(ctx context.Context) (ChainState, error) {
	if provisioner == nil || provisioner.db == nil {
		return ChainState{}, fmt.Errorf("read audit chain status: provisioner is not initialized")
	}
	return (&ChainStateRepository{repositoryBase: provisioner.repositoryBase}).Get(ctx, provisioner.chainID)
}

// Provision resumes or starts a build, completes its backfill, and atomically
// installs the database contract while switching the chain to ACTIVE.
func (provisioner *ChainProvisioner) Provision(ctx context.Context, owner string) error {
	if ctx == nil {
		return fmt.Errorf("provision audit chain: %w", ErrNilContext)
	}
	if provisioner == nil || provisioner.db == nil {
		return fmt.Errorf("provision audit chain: provisioner is not initialized")
	}
	if provisioner.manifest == nil {
		return fmt.Errorf("provision audit chain %q: trusted chain manifest is required", provisioner.chainID)
	}
	if strings.TrimSpace(owner) == "" {
		return fmt.Errorf("provision audit chain %q: owner is required", provisioner.chainID)
	}

	state, err := provisioner.Status(ctx)
	if err != nil {
		return err
	}
	if state.Status == "ACTIVE" {
		return nil
	}
	if state.Status != "DISABLED" && state.Status != "BUILDING" {
		return fmt.Errorf("provision audit chain %q from status %q: %w", provisioner.chainID, state.Status, ErrBuildNotInProgress)
	}

	mode, err := provisioner.manifest.ExpectedMode(ctx)
	if err != nil {
		return fmt.Errorf("read trusted audit chain mode: %w", err)
	}
	if mode != "keyless" && mode != "hmac" {
		return fmt.Errorf("trusted audit chain mode %q: %w", mode, ErrInvalidBuildMode)
	}
	buildRepository := &ChainBuildRepository{repositoryBase: provisioner.repositoryBase}
	lease, err := buildRepository.BeginBuild(ctx, provisioner.chainID, BuildRequest{
		Mode: mode, Owner: owner, Lease: provisioner.cfg.Lease,
	})
	if err != nil {
		// A concurrent provisioner may have completed between Status and BeginBuild.
		if errors.Is(err, ErrChainAlreadyActive) {
			latest, statusErr := provisioner.Status(ctx)
			if statusErr == nil && latest.Status == "ACTIVE" {
				return nil
			}
		}
		return err
	}

	service := NewBackfillService(
		provisioner.db, provisioner.dialect, provisioner.chainID, provisioner.manifest, provisioner.cfg,
	)
	if _, err := service.Run(ctx, lease.Owner, lease.Epoch); err != nil {
		return fmt.Errorf("provision audit chain %q backfill: %w", provisioner.chainID, err)
	}
	return service.Activate(ctx, lease.Owner, lease.Epoch, provisioner.manifest)
}

// Status returns the persisted state for this backfill service's chain.
func (service *BackfillService) Status(ctx context.Context) (ChainState, error) {
	if ctx == nil {
		return ChainState{}, fmt.Errorf("read audit chain status: %w", ErrNilContext)
	}
	if service == nil || service.db == nil {
		return ChainState{}, fmt.Errorf("read audit chain status: service is not initialized")
	}
	return (&ChainStateRepository{repositoryBase: service.repositoryBase}).Get(ctx, service.chainID)
}

// Activate validates a stable BUILDING snapshot, then installs the database
// contract and performs the fenced ACTIVE CAS in one short transaction.
func (service *BackfillService) Activate(
	ctx context.Context,
	owner string,
	epoch int,
	manifest ChainManifest,
) error {
	if err := service.validate(ctx, owner, "activate"); err != nil {
		return err
	}
	if manifest == nil {
		return fmt.Errorf("activate audit chain %q: trusted chain manifest is required", service.chainID)
	}
	expectedMode, currentKeyVersion, err := loadManifestAuthority(ctx, manifest)
	if err != nil {
		return fmt.Errorf("activate audit chain %q: %w", service.chainID, err)
	}

	if err := service.validateActivationSnapshot(ctx, owner, epoch, manifest, expectedMode, currentKeyVersion); err != nil {
		return err
	}
	if err := service.commitActivation(ctx, owner, epoch, expectedMode, currentKeyVersion); err != nil {
		return err
	}
	return nil
}

func loadManifestAuthority(ctx context.Context, manifest ChainManifest) (string, int, error) {
	mode, err := manifest.ExpectedMode(ctx)
	if err != nil {
		return "", 0, fmt.Errorf("read trusted chain mode: %w", err)
	}
	version, err := manifest.CurrentKeyVersion(ctx)
	if err != nil {
		return "", 0, fmt.Errorf("read trusted current key version: %w", err)
	}
	switch mode {
	case "keyless":
		if version != 0 {
			return "", 0, ErrChainModeDowngrade
		}
	case "hmac":
		if version < 1 {
			return "", 0, ErrChainModeDowngrade
		}
		key, err := manifest.ChainKeyForVersion(ctx, version)
		if err != nil {
			return "", 0, fmt.Errorf("load trusted chain key version %d: %w", version, err)
		}
		if len(key) == 0 {
			return "", 0, fmt.Errorf("trusted chain key version %d is empty", version)
		}
	default:
		return "", 0, fmt.Errorf("trusted chain mode %q: %w", mode, ErrInvalidBuildMode)
	}
	return mode, version, nil
}

func (service *BackfillService) validateActivationSnapshot(
	ctx context.Context,
	owner string,
	epoch int,
	manifest ChainManifest,
	expectedMode string,
	currentKeyVersion int,
) (resultErr error) {
	transaction, err := service.beginActivationSnapshot(ctx)
	if err != nil {
		return fmt.Errorf("begin audit chain activation snapshot: %w", err)
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, transaction.Rollback(ctx))
		}
	}()

	state, err := service.readActivationState(ctx, transaction, false)
	if err != nil {
		return err
	}
	if err := verifyActivationFencing(state, owner, epoch); err != nil {
		return err
	}
	if err := verifyManifestState(state, expectedMode); err != nil {
		return err
	}
	reason, err := service.verifySnapshotPrefix(ctx, transaction, state, manifest)
	if err != nil {
		return err
	}
	if reason != "" {
		return fmt.Errorf("audit chain %q prefix verification failed: %s", service.chainID, reason)
	}
	coverage, err := service.readActivationCoverage(ctx, transaction, state, expectedMode, currentKeyVersion)
	if err != nil {
		return err
	}
	if !coverage.complete(state.HeadSeq) {
		return fmt.Errorf("activate audit chain %q: %w", service.chainID, ErrChainCoverageIncomplete)
	}
	if !coverage.authoritativeVersion {
		return fmt.Errorf("activate audit chain %q: %w", service.chainID, ErrChainModeDowngrade)
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit audit chain activation snapshot: %w", err)
	}
	return nil
}

func (service *BackfillService) beginActivationSnapshot(ctx context.Context) (chainTransaction, error) {
	options := &sql.TxOptions{ReadOnly: true}
	if service.dialect == DialectPostgres {
		options.Isolation = sql.LevelRepeatableRead
	} else {
		options.Isolation = sql.LevelSerializable
	}
	transaction, err := service.db.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return postgresChainTransaction{Tx: transaction}, nil
}

func (service *BackfillService) readActivationState(
	ctx context.Context,
	executor sqlExecutor,
	forUpdate bool,
) (ChainState, error) {
	query := `
SELECT chain_id, chain_instance_id, status, mode, head_seq, head_id, head_hash,
       genesis_at, protected_since_id, build_owner, build_lease_until, build_epoch,
       last_built_id, last_built_seq, last_built_hash, updated_at
FROM chain_state
WHERE chain_id = ?`
	if forUpdate && service.dialect == DialectPostgres {
		query += " FOR UPDATE"
	}
	state, err := scanChainState(executor.QueryRowContext(ctx, service.bind(query), service.chainID))
	if errors.Is(err, sql.ErrNoRows) {
		return ChainState{}, fmt.Errorf("read activation state %q: %w", service.chainID, errors.Join(ErrNotFound, err))
	}
	if err != nil {
		return ChainState{}, fmt.Errorf("read activation state %q: %w", service.chainID, err)
	}
	return state, nil
}

func verifyActivationFencing(state ChainState, owner string, epoch int) error {
	if err := VerifyFencing(state, owner, epoch); err != nil {
		return fmt.Errorf("activate audit chain %q: %w", state.ChainID, err)
	}
	if state.BuildLeaseUntil == nil || !state.BuildLeaseUntil.After(time.Now().UTC()) {
		return fmt.Errorf("activate audit chain %q: %w", state.ChainID, ErrStaleBuildFencing)
	}
	return nil
}

func verifyManifestState(state ChainState, expectedMode string) error {
	if state.Mode == nil || *state.Mode != expectedMode {
		return fmt.Errorf("activate audit chain %q: %w", state.ChainID, ErrChainModeDowngrade)
	}
	if state.ChainInstanceID == nil {
		return fmt.Errorf("activate audit chain %q: %s", state.ChainID, backfillReasonBadFormat)
	}
	return nil
}

func (service *BackfillService) verifySnapshotPrefix(
	ctx context.Context,
	transaction chainTransaction,
	state ChainState,
	manifest ChainManifest,
) (string, error) {
	halfChained, err := service.hasHalfChainedRow(ctx, transaction)
	if err != nil {
		return "", err
	}
	if halfChained {
		return backfillReasonHalfChained, nil
	}

	algorithm := auditchain.AlgorithmSHA256
	if *state.Mode == "hmac" {
		algorithm = auditchain.AlgorithmHMACSHA256
	}
	expectedSequence := int64(1)
	previousHash := auditchain.GenesisPrevHex
	lastID := int64(0)
	cursorSequence := int64(math.MinInt64)
	cursorID := int64(0)
	appendRepository := service.appendRepository()
	for {
		rows, err := service.selectLinkedVerificationPage(ctx, transaction, cursorSequence, cursorID)
		if err != nil {
			return "", err
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			cursorSequence, cursorID = row.sequence, row.auditLog.ID
			if row.sequence < expectedSequence {
				return backfillReasonDuplicate, nil
			}
			if row.sequence > expectedSequence {
				return backfillReasonSequenceGap, nil
			}
			if row.formatVersion != auditchain.ChainFormatVersionV1 ||
				!validLowerHexHash(row.previousHash) || !validLowerHexHash(row.selfHash) {
				return backfillReasonBadFormat, nil
			}
			if row.previousHash != previousHash {
				return backfillReasonPrevMismatch, nil
			}

			var key []byte
			switch algorithm {
			case auditchain.AlgorithmSHA256:
				if row.keyVersion != 0 {
					return backfillReasonBadFormat, nil
				}
			case auditchain.AlgorithmHMACSHA256:
				if row.keyVersion < 1 {
					return activationModeDowngrade, nil
				}
				key, err = manifest.ChainKeyForVersion(ctx, row.keyVersion)
				if err != nil || len(key) == 0 {
					return backfillReasonKeyUnavailable, nil
				}
			}
			computed, err := appendRepository.hashAuditLog(
				state, row.auditLog, algorithm, row.keyVersion, row.sequence, row.previousHash, key,
			)
			if err != nil {
				return backfillReasonBadFormat, nil
			}
			if computed != row.selfHash {
				return backfillReasonSelfMismatch, nil
			}
			previousHash = row.selfHash
			lastID = row.auditLog.ID
			expectedSequence++
		}
	}
	verifiedHead := expectedSequence - 1
	if verifiedHead != state.HeadSeq || !headMatches(state, verifiedHead, lastID, previousHash) {
		return backfillReasonHeadMismatch, nil
	}
	return "", nil
}

type activationCoverage struct {
	unchained            int64
	halfChained          bool
	linked               int64
	minimumLinkedID      sql.NullInt64
	authoritativeVersion bool
}

func (coverage activationCoverage) complete(headSequence int64) bool {
	return coverage.unchained == 0 && !coverage.halfChained && coverage.linked == headSequence
}

func (service *BackfillService) readActivationCoverage(
	ctx context.Context,
	executor chainTransaction,
	state ChainState,
	expectedMode string,
	currentKeyVersion int,
) (activationCoverage, error) {
	var coverage activationCoverage
	if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_logs WHERE chain_seq IS NULL`).Scan(&coverage.unchained); err != nil {
		return coverage, fmt.Errorf("count unchained audit rows for activation: %w", err)
	}
	halfChained, err := service.hasHalfChainedRow(ctx, executor)
	if err != nil {
		return coverage, err
	}
	coverage.halfChained = halfChained
	if err := executor.QueryRowContext(ctx, `
SELECT COUNT(*), MIN(id)
FROM audit_logs
WHERE chain_seq IS NOT NULL`).Scan(&coverage.linked, &coverage.minimumLinkedID); err != nil {
		return coverage, fmt.Errorf("read linked audit coverage for activation: %w", err)
	}

	if expectedMode == "keyless" {
		var mismatched int64
		if err := executor.QueryRowContext(ctx, `
SELECT COUNT(*) FROM audit_logs
WHERE chain_seq IS NOT NULL AND chain_key_version <> 0`).Scan(&mismatched); err != nil {
			return coverage, fmt.Errorf("verify keyless row versions for activation: %w", err)
		}
		coverage.authoritativeVersion = currentKeyVersion == 0 && mismatched == 0
		return coverage, nil
	}

	if state.HeadSeq == 0 {
		// S2 freezes the initial HMAC build version at one; without a head row,
		// this is the only persisted build-version authority available.
		coverage.authoritativeVersion = currentKeyVersion == initialHMACChainKeyVersion
		return coverage, nil
	}
	var headVersion int
	err = executor.QueryRowContext(ctx, service.bind(`
SELECT chain_key_version
FROM audit_logs
WHERE chain_seq = ?`), state.HeadSeq).Scan(&headVersion)
	if errors.Is(err, sql.ErrNoRows) {
		coverage.authoritativeVersion = false
		return coverage, nil
	}
	if err != nil {
		return coverage, fmt.Errorf("read audit chain head key version: %w", err)
	}
	coverage.authoritativeVersion = headVersion == currentKeyVersion
	return coverage, nil
}

func (service *BackfillService) commitActivation(
	ctx context.Context,
	owner string,
	epoch int,
	expectedMode string,
	currentKeyVersion int,
) (resultErr error) {
	transaction, err := service.appendRepository().beginChainTransaction(ctx)
	if err != nil {
		return fmt.Errorf("begin audit chain activation transaction: %w", err)
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, transaction.Rollback(ctx))
		}
	}()

	state, err := service.readActivationState(ctx, transaction, true)
	if err != nil {
		return err
	}
	if err := verifyActivationFencing(state, owner, epoch); err != nil {
		return err
	}
	if err := verifyManifestState(state, expectedMode); err != nil {
		return err
	}
	coverage, err := service.readActivationCoverage(ctx, transaction, state, expectedMode, currentKeyVersion)
	if err != nil {
		return err
	}
	if !coverage.complete(state.HeadSeq) {
		return fmt.Errorf("activate audit chain %q: %w", service.chainID, ErrChainCoverageIncomplete)
	}
	if !coverage.authoritativeVersion {
		return fmt.Errorf("activate audit chain %q: %w", service.chainID, ErrChainModeDowngrade)
	}
	if err := service.installChainContract(ctx, transaction); err != nil {
		return err
	}

	now := time.Now().UTC()
	var protectedSince any
	if coverage.minimumLinkedID.Valid {
		protectedSince = coverage.minimumLinkedID.Int64
	}
	result, err := transaction.ExecContext(ctx, service.bind(`
UPDATE chain_state
SET status = 'ACTIVE', protected_since_id = ?, build_owner = NULL,
    build_lease_until = NULL, updated_at = ?
WHERE chain_id = ? AND status = 'BUILDING' AND build_epoch = ?`),
		protectedSince, now, service.chainID, epoch)
	if err != nil {
		return fmt.Errorf("activate audit chain %q: %w", service.chainID, err)
	}
	if err := requireSingleBuildRow(result, "activate audit chain"); err != nil {
		return fmt.Errorf("activate audit chain %q: %w", service.chainID, err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit audit chain activation: %w", err)
	}
	return nil
}

func (service *BackfillService) installChainContract(ctx context.Context, transaction chainTransaction) error {
	switch service.dialect {
	case DialectPostgres:
		for _, column := range []string{
			"chain_seq", "prev_hash", "self_hash", "chain_key_version", "chain_format_version",
		} {
			if _, err := transaction.ExecContext(ctx, "ALTER TABLE audit_logs ALTER COLUMN "+column+" SET NOT NULL"); err != nil {
				return fmt.Errorf("install audit chain NOT NULL contract on %s: %w", column, err)
			}
		}
		if _, err := transaction.ExecContext(ctx, `
CREATE UNIQUE INDEX ux_audit_logs_chain_seq ON audit_logs(chain_seq)`); err != nil {
			return fmt.Errorf("install audit chain sequence contract: %w", err)
		}
	case DialectSQLite:
		statements := []string{
			`CREATE TRIGGER trg_audit_logs_chain_contract_insert
BEFORE INSERT ON audit_logs
WHEN NEW.chain_seq IS NULL OR NEW.prev_hash IS NULL OR NEW.self_hash IS NULL
  OR NEW.chain_key_version IS NULL OR NEW.chain_format_version IS NULL
BEGIN
  SELECT RAISE(ABORT, 'audit chain contract: chain columns required');
END`,
			`CREATE TRIGGER trg_audit_logs_chain_contract_update
BEFORE UPDATE ON audit_logs
WHEN NEW.chain_seq IS NULL OR NEW.prev_hash IS NULL OR NEW.self_hash IS NULL
  OR NEW.chain_key_version IS NULL OR NEW.chain_format_version IS NULL
BEGIN
  SELECT RAISE(ABORT, 'audit chain contract: chain columns required');
END`,
			`CREATE UNIQUE INDEX ux_audit_logs_chain_seq ON audit_logs(chain_seq)`,
		}
		for _, statement := range statements {
			if _, err := transaction.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("install SQLite audit chain contract: %w", err)
			}
		}
	default:
		return fmt.Errorf("install audit chain contract: unsupported dialect %q", service.dialect)
	}
	return nil
}

// insertActiveAuditLog implements the post-contract half of F1: all contract
// columns are populated at INSERT time and the placeholder hash is replaced in
// the same transaction after the database-final business row is read back.
func insertActiveAuditLog(
	ctx context.Context,
	executor sqlExecutor,
	dialect Dialect,
	auditLog model.AuditLog,
	sequence int64,
	previousHash string,
	keyVersion int,
) (model.AuditLog, error) {
	id, err := insertReturningID(ctx, executor, dialect, `
INSERT INTO audit_logs (
  agent_id, datasource_id, session_id, conversation_id, mcp_tool, db_type,
  sql_raw, sql_norm, stmt_type, objects, decision, rule_hits, risk_level,
  est_rows, rows_returned, latency_ms, client_ip, model_name, error_msg, error_code,
  action, actor_type, actor_id, details_json, event_uuid,
  chain_seq, prev_hash, self_hash, chain_key_version, chain_format_version
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		optionalString(auditLog.AgentID), optionalString(auditLog.DatasourceID),
		optionalString(auditLog.SessionID), optionalString(auditLog.ConversationID),
		optionalString(auditLog.MCPTool), optionalString(auditLog.DBType),
		optionalString(auditLog.SQLRaw), optionalString(auditLog.SQLNorm),
		optionalString(auditLog.StmtType), optionalString(auditLog.Objects), auditLog.Decision,
		optionalString(auditLog.RuleHits), optionalInt(auditLog.RiskLevel), optionalInt64(auditLog.EstRows),
		optionalInt(auditLog.RowsReturned), optionalInt64(auditLog.LatencyMS),
		optionalString(auditLog.ClientIP), optionalString(auditLog.ModelName),
		optionalString(auditLog.ErrorMsg), optionalString(auditLog.ErrorCode),
		optionalString(auditLog.Action), optionalString(auditLog.ActorType), optionalString(auditLog.ActorID),
		optionalString(auditLog.DetailsJSON), optionalString(auditLog.EventUUID),
		sequence, previousHash, activeSelfHashPlaceholder, keyVersion, auditchain.ChainFormatVersionV1,
	)
	if err != nil {
		if isNamedUniqueViolation(err, "ux_audit_logs_event_uuid", "audit_logs.event_uuid") {
			return model.AuditLog{}, fmt.Errorf("insert audit log: %w", ErrAuditEventAlreadyDelivered)
		}
		return model.AuditLog{}, fmt.Errorf("insert active audit log: %w", err)
	}
	if id <= 0 {
		return model.AuditLog{}, fmt.Errorf("read inserted active audit log ID: invalid ID %d", id)
	}
	inserted, err := getInsertedAuditLog(ctx, executor, dialect, id)
	if err != nil {
		return model.AuditLog{}, fmt.Errorf("read inserted active audit log %d: %w", id, err)
	}
	return inserted, nil
}
