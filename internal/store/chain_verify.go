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
)

const (
	verificationValid              = "VALID_AT_OBSERVED_HEAD"
	verificationDisabled           = "disabled"
	verificationFailed             = "failed"
	verificationCoverageIncomplete = "coverage_incomplete"
	verificationRuntimeError       = "runtime_error"
)

// ChainVerificationOutcome is one consistent-snapshot observation of an
// audit chain. BreakSeq and BreakID are zero when the result is not tied to a
// particular stored row.
type ChainVerificationOutcome struct {
	ChainID            string
	Status             string
	ObservedInstanceID string
	Valid              bool
	Result             string
	HeadSeq            int64
	TotalRows          int64
	Unchained          int64
	BreakSeq           int64
	BreakID            int64
	BreakReason        string
	VerifiedAt         time.Time

	// These fields retain the remainder of the F4 observation tuple for the
	// second-transaction persistence CAS without expanding the public result.
	observed         bool
	observedInstance *string
	observedHeadHash *string
	observedMode     *string
	observedEpoch    int
}

// ChainVerifier validates any persisted chain state without acquiring a build
// lease or a chain_state write lock.
type ChainVerifier struct {
	repositoryBase
	chainID  string
	manifest ChainManifest
}

// NewChainVerifier constructs an independent, read-only chain verifier.
func NewChainVerifier(db *sql.DB, dialect Dialect, chainID string, manifest ChainManifest) *ChainVerifier {
	return &ChainVerifier{
		repositoryBase: repositoryBase{db: db, dialect: dialect},
		chainID:        chainID,
		manifest:       manifest,
	}
}

// Verify checks the complete linked chain and its coverage in one stable,
// read-only database snapshot.
func (verifier *ChainVerifier) Verify(ctx context.Context) (outcome ChainVerificationOutcome, resultErr error) {
	outcome.ChainID = verifierChainID(verifier)
	outcome.VerifiedAt = time.Now().UTC()
	outcome.Result = verificationRuntimeError
	outcome.BreakReason = verificationRuntimeError

	if ctx == nil {
		return outcome, fmt.Errorf("verify audit chain: %w", ErrNilContext)
	}
	if verifier == nil || verifier.db == nil {
		return outcome, fmt.Errorf("verify audit chain: verifier is not initialized")
	}
	if strings.TrimSpace(verifier.chainID) == "" {
		return outcome, fmt.Errorf("verify audit chain: chain ID is required")
	}
	if verifier.manifest == nil {
		return outcome, fmt.Errorf("verify audit chain %q: trusted chain manifest is required", verifier.chainID)
	}
	if verifier.dialect != DialectSQLite && verifier.dialect != DialectPostgres {
		return outcome, fmt.Errorf("verify audit chain %q: unsupported dialect %q", verifier.chainID, verifier.dialect)
	}

	options := &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelSerializable}
	if verifier.dialect == DialectPostgres {
		options.Isolation = sql.LevelRepeatableRead
	}
	transaction, err := verifier.db.BeginTx(ctx, options)
	if err != nil {
		return outcome, fmt.Errorf("begin audit chain verification snapshot: %w", err)
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, rollbackSQLTx(transaction))
		}
	}()

	state, err := verifier.readState(ctx, transaction)
	if err != nil {
		return outcome, err
	}
	verifier.observeState(&outcome, state)
	if err := verifier.readCoverageCounts(ctx, transaction, &outcome); err != nil {
		return outcome, err
	}

	switch state.Status {
	case "DISABLED":
		outcome.Result = verificationDisabled
		outcome.BreakReason = ""
		outcome.Unchained = outcome.TotalRows
		return outcome, commitVerificationSnapshot(transaction)
	case "FAILED":
		outcome.Result = verificationFailed
		outcome.BreakReason = ""
		return outcome, commitVerificationSnapshot(transaction)
	case "BUILDING", "ACTIVE":
		// These states are independently verifiable.
	default:
		return outcome, fmt.Errorf("verify audit chain %q: unknown status %q", verifier.chainID, state.Status)
	}

	expectedMode, currentKeyVersion, authorityReason, authorityErr := verifier.readManifestAuthority(ctx, state)
	if authorityReason != "" {
		setVerificationBreak(&outcome, authorityReason, 0, 0)
		if err := commitVerificationSnapshot(transaction); err != nil {
			return outcome, err
		}
		return outcome, authorityErr
	}
	if state.ChainInstanceID == nil {
		setVerificationBreak(&outcome, backfillReasonBadFormat, 0, 0)
		return outcome, commitVerificationSnapshot(transaction)
	}

	halfSequence, halfID, found, err := verifier.firstHalfChained(ctx, transaction)
	if err != nil {
		return outcome, err
	}
	if found {
		setVerificationBreak(&outcome, backfillReasonHalfChained, halfSequence, halfID)
		return outcome, commitVerificationSnapshot(transaction)
	}

	algorithm := auditchain.AlgorithmSHA256
	var hmacKey []byte
	if expectedMode == "hmac" {
		algorithm = auditchain.AlgorithmHMACSHA256
		hmacKey, err = verifier.manifest.ChainKeyForVersion(ctx, currentKeyVersion)
		if err != nil || len(hmacKey) == 0 {
			setVerificationBreak(&outcome, backfillReasonKeyUnavailable, 0, 0)
			if commitErr := commitVerificationSnapshot(transaction); commitErr != nil {
				return outcome, commitErr
			}
			if err != nil {
				return outcome, fmt.Errorf("load trusted chain key version %d: %w", currentKeyVersion, err)
			}
			return outcome, fmt.Errorf("trusted chain key version %d is empty", currentKeyVersion)
		}
	}

	service := &BackfillService{repositoryBase: verifier.repositoryBase, chainID: verifier.chainID}
	appendRepository := service.appendRepository()
	expectedSequence := int64(1)
	previousHash := auditchain.GenesisPrevHex
	lastID := int64(0)
	linkedRows := int64(0)
	cursorSequence := int64(math.MinInt64)
	cursorID := int64(0)
	wrappedTransaction := postgresChainTransaction{Tx: transaction}
	for {
		page, pageErr := service.selectLinkedVerificationPage(ctx, wrappedTransaction, cursorSequence, cursorID)
		if pageErr != nil {
			return outcome, pageErr
		}
		if len(page) == 0 {
			break
		}
		for _, row := range page {
			cursorSequence, cursorID = row.sequence, row.auditLog.ID
			linkedRows++
			if row.sequence < expectedSequence {
				setVerificationBreak(&outcome, backfillReasonDuplicate, row.sequence, row.auditLog.ID)
				return outcome, commitVerificationSnapshot(transaction)
			}
			if row.sequence > expectedSequence {
				setVerificationBreak(&outcome, backfillReasonSequenceGap, expectedSequence, row.auditLog.ID)
				return outcome, commitVerificationSnapshot(transaction)
			}
			if row.formatVersion != auditchain.ChainFormatVersionV1 ||
				!validLowerHexHash(row.previousHash) || !validLowerHexHash(row.selfHash) {
				setVerificationBreak(&outcome, backfillReasonBadFormat, row.sequence, row.auditLog.ID)
				return outcome, commitVerificationSnapshot(transaction)
			}
			if row.previousHash != previousHash {
				setVerificationBreak(&outcome, backfillReasonPrevMismatch, row.sequence, row.auditLog.ID)
				return outcome, commitVerificationSnapshot(transaction)
			}
			if expectedMode == "keyless" && row.keyVersion != 0 ||
				expectedMode == "hmac" && row.keyVersion != currentKeyVersion {
				setVerificationBreak(&outcome, activationModeDowngrade, row.sequence, row.auditLog.ID)
				return outcome, commitVerificationSnapshot(transaction)
			}
			computed, hashErr := appendRepository.hashAuditLog(
				state, row.auditLog, algorithm, row.keyVersion, row.sequence, row.previousHash, hmacKey,
			)
			if hashErr != nil {
				setVerificationBreak(&outcome, backfillReasonBadFormat, row.sequence, row.auditLog.ID)
				return outcome, commitVerificationSnapshot(transaction)
			}
			if computed != row.selfHash {
				setVerificationBreak(&outcome, backfillReasonSelfMismatch, row.sequence, row.auditLog.ID)
				return outcome, commitVerificationSnapshot(transaction)
			}
			previousHash = row.selfHash
			lastID = row.auditLog.ID
			expectedSequence++
		}
	}

	verifiedHead := expectedSequence - 1
	if verifiedHead != state.HeadSeq || linkedRows != state.HeadSeq ||
		!headMatches(state, verifiedHead, lastID, previousHash) {
		breakID := lastID
		if state.HeadID != nil {
			breakID = *state.HeadID
		}
		setVerificationBreak(&outcome, backfillReasonHeadMismatch, state.HeadSeq, breakID)
		return outcome, commitVerificationSnapshot(transaction)
	}
	if outcome.Unchained != 0 || linkedRows != outcome.TotalRows {
		outcome.Result = verificationCoverageIncomplete
		outcome.BreakReason = ""
		return outcome, commitVerificationSnapshot(transaction)
	}
	if state.Status != "ACTIVE" {
		outcome.Result = verificationCoverageIncomplete
		outcome.BreakReason = ""
		return outcome, commitVerificationSnapshot(transaction)
	}

	outcome.Valid = true
	outcome.Result = verificationValid
	outcome.BreakReason = ""
	return outcome, commitVerificationSnapshot(transaction)
}

// VerifyAndPersist verifies first, then conditionally persists all nine
// verification columns in a separate write transaction. A newer persisted
// observation or a changed F4 state tuple makes the write a harmless no-op.
func (verifier *ChainVerifier) VerifyAndPersist(ctx context.Context) (ChainVerificationOutcome, error) {
	outcome, verifyErr := verifier.Verify(ctx)
	if verifier == nil || verifier.db == nil || ctx == nil {
		return outcome, verifyErr
	}
	if persistErr := verifier.persist(ctx, outcome); persistErr != nil {
		return outcome, errors.Join(verifyErr, persistErr)
	}
	return outcome, verifyErr
}

// ExitCodeForVerification maps a verification observation to the stable CLI
// process contract without making the verifier itself depend on a CLI.
func ExitCodeForVerification(outcome ChainVerificationOutcome, verifyErr error) int {
	if outcome.Valid {
		return 0
	}
	reason := outcome.BreakReason
	if reason == "" {
		reason = outcome.Result
	}
	if reason == backfillReasonKeyUnavailable {
		return 3
	}
	if verifyErr != nil {
		return 5
	}
	switch reason {
	case backfillReasonHalfChained,
		backfillReasonSequenceGap,
		backfillReasonDuplicate,
		backfillReasonPrevMismatch,
		backfillReasonSelfMismatch,
		backfillReasonHeadMismatch,
		backfillReasonBadFormat,
		activationModeDowngrade:
		return 2
	case verificationDisabled, verificationCoverageIncomplete:
		return 4
	}
	if outcome.Status == "BUILDING" || outcome.Status == "FAILED" || outcome.Status == "DISABLED" {
		return 4
	}
	return 5
}

func verifierChainID(verifier *ChainVerifier) string {
	if verifier == nil {
		return ""
	}
	return verifier.chainID
}

func rollbackSQLTx(transaction *sql.Tx) error {
	err := transaction.Rollback()
	if errors.Is(err, sql.ErrTxDone) {
		return nil
	}
	return err
}

func commitVerificationSnapshot(transaction *sql.Tx) error {
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit audit chain verification snapshot: %w", err)
	}
	return nil
}

func (verifier *ChainVerifier) readState(ctx context.Context, transaction *sql.Tx) (ChainState, error) {
	tenantID, tenantErr := verifier.requireTenant(ctx, "verify audit chain state")
	if tenantErr != nil {
		return ChainState{}, tenantErr
	}
	state, err := scanChainState(transaction.QueryRowContext(ctx, verifier.bind(`
SELECT chain_id, chain_instance_id, status, mode, head_seq, head_id, head_hash,
       genesis_at, protected_since_id, build_owner, build_lease_until, build_epoch,
       last_built_id, last_built_seq, last_built_hash, updated_at
FROM chain_state
WHERE chain_id = ? AND tenant_id = ?`), verifier.chainID, tenantID))
	if errors.Is(err, sql.ErrNoRows) {
		return ChainState{}, fmt.Errorf("verify audit chain state %q: %w", verifier.chainID, errors.Join(ErrNotFound, err))
	}
	if err != nil {
		return ChainState{}, fmt.Errorf("verify audit chain state %q: %w", verifier.chainID, err)
	}
	return state, nil
}

func (verifier *ChainVerifier) observeState(outcome *ChainVerificationOutcome, state ChainState) {
	outcome.Status = state.Status
	outcome.HeadSeq = state.HeadSeq
	outcome.observed = true
	outcome.observedInstance = state.ChainInstanceID
	outcome.observedHeadHash = state.HeadHash
	outcome.observedMode = state.Mode
	outcome.observedEpoch = state.BuildEpoch
	if state.ChainInstanceID != nil {
		outcome.ObservedInstanceID = *state.ChainInstanceID
	}
}

func (verifier *ChainVerifier) readCoverageCounts(
	ctx context.Context,
	transaction *sql.Tx,
	outcome *ChainVerificationOutcome,
) error {
	tenantID, tenantErr := verifier.requireTenant(ctx, "read audit chain coverage")
	if tenantErr != nil {
		return tenantErr
	}
	if err := transaction.QueryRowContext(ctx, verifier.bind(`
SELECT COUNT(*), COALESCE(SUM(CASE WHEN chain_seq IS NULL THEN 1 ELSE 0 END), 0)
FROM audit_logs WHERE tenant_id = ?`), tenantID).Scan(&outcome.TotalRows, &outcome.Unchained); err != nil {
		return fmt.Errorf("read audit chain coverage counts: %w", err)
	}
	return nil
}

func (verifier *ChainVerifier) readManifestAuthority(
	ctx context.Context,
	state ChainState,
) (mode string, currentVersion int, reason string, err error) {
	mode, err = verifier.manifest.ExpectedMode(ctx)
	if err != nil {
		return "", 0, verificationRuntimeError, fmt.Errorf("read trusted chain mode: %w", err)
	}
	currentVersion, err = verifier.manifest.CurrentKeyVersion(ctx)
	if err != nil {
		return "", 0, verificationRuntimeError, fmt.Errorf("read trusted current key version: %w", err)
	}
	if mode != "keyless" && mode != "hmac" {
		return "", 0, verificationRuntimeError, fmt.Errorf("trusted audit chain mode %q is invalid", mode)
	}
	if state.Mode == nil || *state.Mode != mode {
		return mode, currentVersion, activationModeDowngrade, nil
	}
	if mode == "keyless" && currentVersion != 0 || mode == "hmac" && currentVersion < 1 {
		return mode, currentVersion, activationModeDowngrade, nil
	}
	return mode, currentVersion, "", nil
}

func (verifier *ChainVerifier) firstHalfChained(
	ctx context.Context,
	transaction *sql.Tx,
) (sequence, id int64, found bool, err error) {
	tenantID, tenantErr := verifier.requireTenant(ctx, "detect half-chained audit row")
	if tenantErr != nil {
		return 0, 0, false, tenantErr
	}
	var nullableSequence sql.NullInt64
	err = transaction.QueryRowContext(ctx, verifier.bind(`
SELECT chain_seq, id
FROM audit_logs
WHERE tenant_id = ? AND NOT (
        chain_seq IS NULL AND prev_hash IS NULL AND self_hash IS NULL
        AND chain_key_version IS NULL AND chain_format_version IS NULL
      )
  AND NOT (
        chain_seq IS NOT NULL AND prev_hash IS NOT NULL AND self_hash IS NOT NULL
        AND chain_key_version IS NOT NULL AND chain_format_version IS NOT NULL
      )
ORDER BY CASE WHEN chain_seq IS NULL THEN 0 ELSE chain_seq END, id
LIMIT 1`), tenantID).Scan(&nullableSequence, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, false, fmt.Errorf("detect half-chained audit row: %w", err)
	}
	if nullableSequence.Valid {
		sequence = nullableSequence.Int64
	}
	return sequence, id, true, nil
}

func setVerificationBreak(outcome *ChainVerificationOutcome, reason string, sequence, id int64) {
	outcome.Valid = false
	outcome.Result = reason
	outcome.BreakReason = reason
	outcome.BreakSeq = sequence
	outcome.BreakID = id
}

func nullableVerificationString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableVerificationInt64(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}

func (verifier *ChainVerifier) persist(ctx context.Context, outcome ChainVerificationOutcome) error {
	tenantID, tenantErr := verifier.requireTenant(ctx, "persist audit chain verification")
	if tenantErr != nil {
		return tenantErr
	}
	query := `
INSERT INTO chain_verification (
  tenant_id, chain_id, observed_instance_id, observed_head_hash, result,
  last_verified_head_seq, last_verified_at, break_seq, break_id, break_reason
)
SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?`
	arguments := []any{
		tenantID,
		outcome.ChainID,
		outcome.observedInstance,
		outcome.observedHeadHash,
		outcome.Result,
		outcome.HeadSeq,
		outcome.VerifiedAt,
		nullableVerificationInt64(outcome.BreakSeq),
		nullableVerificationInt64(outcome.BreakID),
		nullableVerificationString(outcome.BreakReason),
	}
	if outcome.observed {
		predicate, predicateArguments := verifier.verificationObservationPredicate(tenantID, outcome)
		query += "\nWHERE EXISTS (" + predicate + ")"
		arguments = append(arguments, predicateArguments...)
	} else {
		// The SELECT WHERE also disambiguates SQLite's UPSERT grammar.
		query += "\nWHERE 1 = 1"
	}
	query += `
ON CONFLICT(tenant_id,chain_id) DO UPDATE SET
  observed_instance_id = excluded.observed_instance_id,
  observed_head_hash = excluded.observed_head_hash,
  result = excluded.result,
  last_verified_head_seq = excluded.last_verified_head_seq,
  last_verified_at = excluded.last_verified_at,
  break_seq = excluded.break_seq,
  break_id = excluded.break_id,
  break_reason = excluded.break_reason
WHERE (chain_verification.last_verified_at IS NULL
   OR chain_verification.last_verified_at <= excluded.last_verified_at)`
	if outcome.observed {
		predicate, predicateArguments := verifier.verificationObservationPredicate(tenantID, outcome)
		query += "\n  AND EXISTS (" + predicate + ")"
		arguments = append(arguments, predicateArguments...)
	}
	if _, err := verifier.db.ExecContext(ctx, verifier.bind(query), arguments...); err != nil {
		return fmt.Errorf("persist audit chain verification %q: %w", outcome.ChainID, err)
	}
	return nil
}

func (verifier *ChainVerifier) verificationObservationPredicate(tenantID string, outcome ChainVerificationOutcome) (string, []any) {
	nullSafeComparison := func(column string) string {
		if verifier.dialect == DialectPostgres {
			return column + " IS NOT DISTINCT FROM ?"
		}
		return column + " IS ?"
	}
	predicate := `
SELECT 1 FROM chain_state
WHERE tenant_id = ? AND chain_id = ?
  AND status = ?
  AND head_seq = ?
  AND build_epoch = ?
  AND ` + nullSafeComparison("chain_instance_id") + `
  AND ` + nullSafeComparison("head_hash") + `
  AND ` + nullSafeComparison("mode")
	arguments := []any{
		tenantID,
		outcome.ChainID,
		outcome.Status,
		outcome.HeadSeq,
		outcome.observedEpoch,
		outcome.observedInstance,
		outcome.observedHeadHash,
		outcome.observedMode,
	}
	return predicate, arguments
}
