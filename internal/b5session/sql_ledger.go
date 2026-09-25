package b5session

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/cuipengdba/agentsql/internal/b5"
	"github.com/cuipengdba/agentsql/internal/store"
)

// PostgreSQLSchema is intentionally package-owned and feature-off. It is not
// registered in the application's production migration stream. S10 can move
// the reviewed DDL into that stream without changing this ledger contract.
const PostgreSQLSchema = `
CREATE TABLE IF NOT EXISTS b5_s3_budgets (
  datasource_id TEXT PRIMARY KEY,
  hard_limit INTEGER NOT NULL CHECK (hard_limit > 0),
  admin_reserve INTEGER NOT NULL CHECK (admin_reserve >= 0 AND admin_reserve < hard_limit),
  connection_used INTEGER NOT NULL DEFAULT 0 CHECK (connection_used >= 0),
  plan_bytes_limit BIGINT NOT NULL CHECK (plan_bytes_limit > 0),
  plan_bytes_used BIGINT NOT NULL DEFAULT 0 CHECK (plan_bytes_used >= 0),
  revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
  CHECK (connection_used <= hard_limit - admin_reserve),
  CHECK (plan_bytes_used <= plan_bytes_limit)
);
CREATE TABLE IF NOT EXISTS b5_s3_claims (
  claim_id TEXT PRIMARY KEY,
  datasource_id TEXT NOT NULL REFERENCES b5_s3_budgets(datasource_id),
  kind TEXT NOT NULL CHECK (kind IN ('POOL_ENVELOPE','DIRECT_PINNED','MIGRATION','HEALTH','ADMIN')),
  owner_instance_id TEXT NOT NULL,
  owner_incarnation TEXT NOT NULL,
  generation BIGINT NOT NULL DEFAULT 1 CHECK (generation > 0),
  claimed_capacity INTEGER NOT NULL CHECK (claimed_capacity >= 0),
  max_open_conns INTEGER NOT NULL CHECK (max_open_conns >= 0 AND max_open_conns <= claimed_capacity),
  state TEXT NOT NULL CHECK (state IN ('ACTIVE','FENCED','RELEASED')),
  revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0)
);
CREATE INDEX IF NOT EXISTS idx_b5_s3_claims_owner ON b5_s3_claims(owner_instance_id,owner_incarnation,state);
CREATE TABLE IF NOT EXISTS b5_s3_leases (
  lease_id TEXT PRIMARY KEY,
  claim_id TEXT NOT NULL REFERENCES b5_s3_claims(claim_id),
  datasource_id TEXT NOT NULL,
  owner_instance_id TEXT NOT NULL,
  owner_incarnation TEXT NOT NULL,
  claim_generation BIGINT NOT NULL CHECK (claim_generation > 0),
  generation BIGINT NOT NULL DEFAULT 1 CHECK (generation > 0),
  dial_permit_id TEXT NOT NULL UNIQUE,
  dial_token TEXT NOT NULL UNIQUE,
  dial_attempted BOOLEAN NOT NULL DEFAULT FALSE,
  state TEXT NOT NULL CHECK (state IN ('DIAL_PERMIT_OUTSTANDING','IN_USE','TERMINATING','QUARANTINED','FREE','NEVER_ALLOCATED','BACKEND_ABSENT_CONFIRMED')),
  server_id TEXT,
  database_name TEXT,
  backend_pid INTEGER,
  backend_started_at TIMESTAMPTZ,
  disposition TEXT NOT NULL DEFAULT 'UNKNOWN' CHECK (disposition IN ('UNKNOWN','RELEASED','DISCARDED','DISCARD_UNCONFIRMED')),
  proof_schema_id TEXT,
  proof_schema_version INTEGER,
  proof_digest BYTEA,
  inventory_epoch BIGINT NOT NULL DEFAULT 0 CHECK (inventory_epoch >= 0),
  charged BOOLEAN NOT NULL DEFAULT TRUE,
  revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
  CHECK (proof_digest IS NULL OR length(proof_digest)=32),
  CHECK ((backend_pid IS NULL AND backend_started_at IS NULL) OR (backend_pid > 0 AND backend_started_at IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS idx_b5_s3_leases_claim ON b5_s3_leases(claim_id,charged,state);
CREATE TABLE IF NOT EXISTS b5_s3_plan_leases (
  lease_id TEXT PRIMARY KEY,
  datasource_id TEXT NOT NULL REFERENCES b5_s3_budgets(datasource_id),
  owner_instance_id TEXT NOT NULL,
  owner_incarnation TEXT NOT NULL,
  charged_bytes BIGINT NOT NULL CHECK (charged_bytes > 0)
);
CREATE INDEX IF NOT EXISTS idx_b5_s3_plan_owner ON b5_s3_plan_leases(owner_instance_id,owner_incarnation);
CREATE TABLE IF NOT EXISTS b5_s3_tombstone_budget (
  singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
  max_entries INTEGER NOT NULL CHECK (max_entries > 0),
  max_bytes BIGINT NOT NULL CHECK (max_bytes > 0),
  max_churn_per_second INTEGER NOT NULL CHECK (max_churn_per_second > 0),
  used_entries INTEGER NOT NULL DEFAULT 0 CHECK (used_entries >= 0),
  used_bytes BIGINT NOT NULL DEFAULT 0 CHECK (used_bytes >= 0),
  churn_window TIMESTAMPTZ NOT NULL,
  churn_count INTEGER NOT NULL DEFAULT 0 CHECK (churn_count >= 0),
  CHECK (used_entries <= max_entries),
  CHECK (used_bytes <= max_bytes)
);
CREATE TABLE IF NOT EXISTS b5_s3_tombstones (
  tombstone_id TEXT PRIMARY KEY,
  session_id_digest BYTEA NOT NULL CHECK (length(session_id_digest)=32),
  terminal_code TEXT NOT NULL,
  final_seq BIGINT NOT NULL CHECK (final_seq >= 0),
  owner_epoch BIGINT NOT NULL CHECK (owner_epoch > 0),
  event_digest BYTEA NOT NULL CHECK (length(event_digest)=32),
  expires_at TIMESTAMPTZ NOT NULL,
  charged_bytes INTEGER NOT NULL CHECK (charged_bytes > 0)
);
CREATE INDEX IF NOT EXISTS idx_b5_s3_tombstones_expiry ON b5_s3_tombstones(expires_at,tombstone_id);
`

type SQLLedger struct{ db *sql.DB }

func NewPostgresLedger(db *sql.DB) (*SQLLedger, error) {
	if db == nil {
		return nil, errors.New("b5session: postgres ledger database is required")
	}
	return &SQLLedger{db: db}, nil
}

func InstallPostgresSchema(ctx context.Context, db *sql.DB) error {
	if ctx == nil || db == nil {
		return errors.New("b5session: install postgres schema requires context and database")
	}
	_, err := db.ExecContext(ctx, PostgreSQLSchema)
	return err
}

func (l *SQLLedger) ConfigureBudget(ctx context.Context, input Budget) (Budget, error) {
	if input.DatasourceID == "" || input.HardLimit <= 0 || input.AdminEmergencyReserve < 0 || input.AdminEmergencyReserve >= input.HardLimit || input.PlanBytesLimit <= 0 {
		return Budget{}, ErrAdmissionDenied
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return Budget{}, err
	}
	defer tx.Rollback()
	var used int
	var planUsed int64
	var revision int64
	err = tx.QueryRowContext(ctx, `SELECT connection_used,plan_bytes_used,revision FROM b5_s3_budgets WHERE datasource_id=$1 FOR UPDATE`, input.DatasourceID).Scan(&used, &planUsed, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.ExecContext(ctx, `INSERT INTO b5_s3_budgets(datasource_id,hard_limit,admin_reserve,plan_bytes_limit) VALUES($1,$2,$3,$4)`, input.DatasourceID, input.HardLimit, input.AdminEmergencyReserve, input.PlanBytesLimit)
	} else if err == nil {
		if used > input.HardLimit-input.AdminEmergencyReserve || planUsed > input.PlanBytesLimit {
			return Budget{}, ErrAdmissionDenied
		}
		_, err = tx.ExecContext(ctx, `UPDATE b5_s3_budgets SET hard_limit=$2,admin_reserve=$3,plan_bytes_limit=$4,revision=revision+1 WHERE datasource_id=$1`, input.DatasourceID, input.HardLimit, input.AdminEmergencyReserve, input.PlanBytesLimit)
	} else {
		return Budget{}, err
	}
	if err != nil {
		return Budget{}, err
	}
	if err = tx.Commit(); err != nil {
		return Budget{}, err
	}
	return l.Budget(ctx, input.DatasourceID)
}

func (l *SQLLedger) Budget(ctx context.Context, datasource string) (Budget, error) {
	var value Budget
	err := l.db.QueryRowContext(ctx, `SELECT datasource_id,hard_limit,admin_reserve,connection_used,plan_bytes_limit,plan_bytes_used,revision FROM b5_s3_budgets WHERE datasource_id=$1`, datasource).Scan(&value.DatasourceID, &value.HardLimit, &value.AdminEmergencyReserve, &value.ConnectionUnitsUsed, &value.PlanBytesLimit, &value.PlanBytesUsed, &value.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return Budget{}, store.ErrNotFound
	}
	return value, err
}

func (l *SQLLedger) AcquireClaim(ctx context.Context, request ClaimRequest) (Claim, error) {
	if err := validateClaimRequest(request); err != nil {
		return Claim{}, err
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return Claim{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE b5_s3_budgets SET connection_used=connection_used+$2,revision=revision+1 WHERE datasource_id=$1 AND connection_used+$2<=hard_limit-admin_reserve`, request.DatasourceID, request.Capacity)
	if err != nil {
		return Claim{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return Claim{}, ErrAdmissionDenied
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO b5_s3_claims(claim_id,datasource_id,kind,owner_instance_id,owner_incarnation,claimed_capacity,max_open_conns,state) VALUES($1,$2,$3,$4,$5,$6,$7,'ACTIVE')`, request.ClaimID, request.DatasourceID, request.Kind, request.OwnerInstanceID, request.OwnerIncarnation, request.Capacity, request.MaxOpenConns)
	if err != nil {
		return Claim{}, fmt.Errorf("insert claim: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return Claim{}, err
	}
	return l.Claim(ctx, request.ClaimID)
}

func (l *SQLLedger) Claim(ctx context.Context, id string) (Claim, error) {
	return scanClaim(l.db.QueryRowContext(ctx, claimSelect+` WHERE claim_id=$1`, id))
}

const claimSelect = `SELECT claim_id,datasource_id,kind,owner_instance_id,owner_incarnation,generation,claimed_capacity,max_open_conns,state,revision FROM b5_s3_claims`

func scanClaim(row interface{ Scan(...any) error }) (Claim, error) {
	var value Claim
	var generation int64
	err := row.Scan(&value.ID, &value.DatasourceID, &value.Kind, &value.OwnerInstanceID, &value.OwnerIncarnation, &generation, &value.ClaimedCapacity, &value.MaxOpenConns, &value.State, &value.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return Claim{}, store.ErrNotFound
	}
	if err != nil {
		return Claim{}, err
	}
	value.Generation = uint64(generation)
	return value, nil
}

func (l *SQLLedger) ReservePlan(ctx context.Context, lease PlanLease) error {
	if lease.ID == "" || lease.DatasourceID == "" || lease.OwnerInstanceID == "" || lease.OwnerIncarnation == "" || lease.Bytes <= 0 {
		return ErrAdmissionDenied
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE b5_s3_budgets SET plan_bytes_used=plan_bytes_used+$2,revision=revision+1 WHERE datasource_id=$1 AND plan_bytes_used+$2<=plan_bytes_limit`, lease.DatasourceID, lease.Bytes)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return ErrAdmissionDenied
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO b5_s3_plan_leases(lease_id,datasource_id,owner_instance_id,owner_incarnation,charged_bytes) VALUES($1,$2,$3,$4,$5)`, lease.ID, lease.DatasourceID, lease.OwnerInstanceID, lease.OwnerIncarnation, lease.Bytes)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (l *SQLLedger) ReleasePlan(ctx context.Context, id string) error {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var datasource string
	var bytes int64
	err = tx.QueryRowContext(ctx, `DELETE FROM b5_s3_plan_leases WHERE lease_id=$1 RETURNING datasource_id,charged_bytes`, id).Scan(&datasource, &bytes)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE b5_s3_budgets SET plan_bytes_used=plan_bytes_used-$2,revision=revision+1 WHERE datasource_id=$1 AND plan_bytes_used>=$2`, datasource, bytes)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return ErrInvalidTransition
	}
	return tx.Commit()
}

func (l *SQLLedger) IssueDialPermit(ctx context.Context, claimID string, generation uint64, permitID, token string) (Lease, error) {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return Lease{}, err
	}
	defer tx.Rollback()
	claim, err := scanClaim(tx.QueryRowContext(ctx, claimSelect+` WHERE claim_id=$1 FOR UPDATE`, claimID))
	if err != nil {
		return Lease{}, err
	}
	if claim.State != ClaimActive || claim.Generation != generation || permitID == "" || token == "" {
		return Lease{}, ErrCASConflict
	}
	var charged int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM b5_s3_leases WHERE claim_id=$1 AND charged`, claimID).Scan(&charged); err != nil {
		return Lease{}, err
	}
	if charged >= claim.MaxOpenConns || charged >= claim.ClaimedCapacity {
		return Lease{}, ErrAdmissionDenied
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO b5_s3_leases(lease_id,claim_id,datasource_id,owner_instance_id,owner_incarnation,claim_generation,dial_permit_id,dial_token,state) VALUES($1,$2,$3,$4,$5,$6,$1,$7,'DIAL_PERMIT_OUTSTANDING')`, permitID, claim.ID, claim.DatasourceID, claim.OwnerInstanceID, claim.OwnerIncarnation, claim.Generation, token)
	if err != nil {
		return Lease{}, err
	}
	if err = tx.Commit(); err != nil {
		return Lease{}, err
	}
	return l.lease(ctx, permitID)
}

func (l *SQLLedger) MarkDialStarted(ctx context.Context, id string, generation uint64) (Lease, error) {
	return l.updateLease(ctx, id, generation, `UPDATE b5_s3_leases AS l SET dial_attempted=TRUE,generation=l.generation+1,revision=l.revision+1 WHERE l.lease_id=$1 AND l.generation=$2 AND l.state='DIAL_PERMIT_OUTSTANDING' AND EXISTS (SELECT 1 FROM b5_s3_claims c WHERE c.claim_id=l.claim_id AND c.state='ACTIVE' AND c.generation=l.claim_generation AND c.owner_instance_id=l.owner_instance_id AND c.owner_incarnation=l.owner_incarnation)`, id, generation)
}

func (l *SQLLedger) BindBackend(ctx context.Context, id string, generation uint64, backend BackendIdentity) (Lease, error) {
	if !backend.Complete() {
		return Lease{}, ErrInvalidTransition
	}
	return l.updateLease(ctx, id, generation, `UPDATE b5_s3_leases AS l SET state='IN_USE',server_id=$3,database_name=$4,backend_pid=$5,backend_started_at=$6,generation=l.generation+1,revision=l.revision+1 WHERE l.lease_id=$1 AND l.generation=$2 AND l.state='DIAL_PERMIT_OUTSTANDING' AND l.dial_attempted AND l.dial_token=$7 AND EXISTS (SELECT 1 FROM b5_s3_claims c WHERE c.claim_id=l.claim_id AND c.state='ACTIVE' AND c.generation=l.claim_generation AND c.owner_instance_id=l.owner_instance_id AND c.owner_incarnation=l.owner_incarnation)`, id, generation, backend.ServerID, backend.Database, backend.PID, backend.BackendStarted, backend.DialToken)
}

func (l *SQLLedger) AcquireFree(ctx context.Context, id string, generation uint64, owner, incarnation string) (Lease, error) {
	return l.updateLease(ctx, id, generation, `UPDATE b5_s3_leases AS l SET state='IN_USE',generation=l.generation+1,revision=l.revision+1 FROM b5_s3_claims AS c WHERE l.lease_id=$1 AND l.generation=$2 AND l.state='FREE' AND c.claim_id=l.claim_id AND c.state='ACTIVE' AND c.generation=l.claim_generation AND c.owner_instance_id=$3 AND c.owner_incarnation=$4`, id, generation, owner, incarnation)
}

func (l *SQLLedger) BeginTermination(ctx context.Context, id string, generation uint64) (Lease, error) {
	return l.updateLease(ctx, id, generation, `UPDATE b5_s3_leases AS l SET state='TERMINATING',generation=l.generation+1,revision=l.revision+1 WHERE l.lease_id=$1 AND l.generation=$2 AND l.state IN ('IN_USE','FREE','QUARANTINED') AND EXISTS (SELECT 1 FROM b5_s3_claims c WHERE c.claim_id=l.claim_id AND c.state='ACTIVE' AND c.generation=l.claim_generation AND c.owner_instance_id=l.owner_instance_id AND c.owner_incarnation=l.owner_incarnation)`, id, generation)
}

func (l *SQLLedger) ApplyDisposition(ctx context.Context, id string, generation uint64, proof DispositionProof) (Lease, error) {
	current, err := l.lease(ctx, id)
	if err != nil {
		return Lease{}, err
	}
	if current.Generation != generation {
		return Lease{}, ErrCASConflict
	}
	next, charged, err := validateDisposition(current.State, proof)
	if err != nil {
		return Lease{}, err
	}
	return l.updateLease(ctx, id, generation, `UPDATE b5_s3_leases AS l SET state=$3,charged=$4,disposition=$5,proof_schema_id=$6,proof_schema_version=$7,proof_digest=$8,inventory_epoch=$9,generation=l.generation+1,revision=l.revision+1 WHERE l.lease_id=$1 AND l.generation=$2 AND EXISTS (SELECT 1 FROM b5_s3_claims c WHERE c.claim_id=l.claim_id AND c.state='ACTIVE' AND c.generation=l.claim_generation AND c.owner_instance_id=l.owner_instance_id AND c.owner_incarnation=l.owner_incarnation)`, id, generation, next, charged, proof.Disposition, proof.SchemaID, proof.SchemaVersion, proof.Digest[:], proof.InventoryEpoch)
}

func (l *SQLLedger) FenceOwner(ctx context.Context, owner, incarnation string) ([]Claim, error) {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `UPDATE b5_s3_claims SET state='FENCED',generation=generation+1,revision=revision+1 WHERE owner_instance_id=$1 AND owner_incarnation=$2 AND state='ACTIVE'`, owner, incarnation)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, claimSelect+` WHERE owner_instance_id=$1 AND owner_incarnation=$2 AND state='FENCED' ORDER BY claim_id FOR UPDATE`, owner, incarnation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]Claim, 0)
	for rows.Next() {
		value, scanErr := scanClaim(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return values, nil
}

func (l *SQLLedger) ListClaimLeases(ctx context.Context, claimID string) ([]Lease, error) {
	rows, err := l.db.QueryContext(ctx, leaseSelect+` WHERE claim_id=$1 ORDER BY lease_id`, claimID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]Lease, 0)
	for rows.Next() {
		value, scanErr := scanLease(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (l *SQLLedger) AbandonUndialed(ctx context.Context, id string, generation uint64, digest [32]byte) (Lease, error) {
	if digest == ([32]byte{}) {
		return Lease{}, ErrUnknownProofSchema
	}
	return l.updateLease(ctx, id, generation, `UPDATE b5_s3_leases SET state='NEVER_ALLOCATED',charged=FALSE,proof_schema_id=$3,proof_schema_version=$4,proof_digest=$5,generation=generation+1,revision=revision+1 WHERE lease_id=$1 AND generation=$2 AND state='DIAL_PERMIT_OUTSTANDING' AND NOT dial_attempted`, id, generation, b5.PoolChildProofSchemaID, b5.PoolChildProofVersion, digest[:])
}

func (l *SQLLedger) ReconcileAbsence(ctx context.Context, id string, generation uint64, proof AbsenceProof) (Lease, error) {
	if proof.SchemaID != b5.PoolChildProofSchemaID || proof.SchemaVersion != b5.PoolChildProofVersion || proof.Digest == ([32]byte{}) || proof.InventoryEpoch == 0 {
		return Lease{}, ErrUnknownProofSchema
	}
	return l.updateLease(ctx, id, generation, `UPDATE b5_s3_leases SET state=CASE WHEN state='DIAL_PERMIT_OUTSTANDING' AND NOT dial_attempted THEN 'NEVER_ALLOCATED' ELSE 'BACKEND_ABSENT_CONFIRMED' END,charged=FALSE,proof_schema_id=$3,proof_schema_version=$4,proof_digest=$5,inventory_epoch=$6,generation=generation+1,revision=revision+1 WHERE lease_id=$1 AND generation=$2 AND inventory_epoch<$6 AND state IN ('DIAL_PERMIT_OUTSTANDING','IN_USE','FREE','TERMINATING','QUARANTINED')`, id, generation, proof.SchemaID, proof.SchemaVersion, proof.Digest[:], proof.InventoryEpoch)
}

func (l *SQLLedger) ShrinkFencedClaim(ctx context.Context, id string, generation uint64) (Claim, error) {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return Claim{}, err
	}
	defer tx.Rollback()
	claim, err := scanClaim(tx.QueryRowContext(ctx, claimSelect+` WHERE claim_id=$1 FOR UPDATE`, id))
	if err != nil {
		return Claim{}, err
	}
	if claim.State != ClaimFenced || claim.Generation != generation {
		return Claim{}, ErrCASConflict
	}
	var charged int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM b5_s3_leases WHERE claim_id=$1 AND charged`, id).Scan(&charged); err != nil {
		return Claim{}, err
	}
	if charged > claim.ClaimedCapacity {
		return Claim{}, ErrInvalidTransition
	}
	delta := claim.ClaimedCapacity - charged
	if delta > 0 {
		result, updateErr := tx.ExecContext(ctx, `UPDATE b5_s3_budgets SET connection_used=connection_used-$2,revision=revision+1 WHERE datasource_id=$1 AND connection_used>=$2`, claim.DatasourceID, delta)
		if updateErr != nil {
			return Claim{}, updateErr
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			return Claim{}, ErrInvalidTransition
		}
	}
	state := ClaimFenced
	if charged == 0 {
		state = ClaimReleased
	}
	_, err = tx.ExecContext(ctx, `UPDATE b5_s3_claims SET claimed_capacity=$2,max_open_conns=$2,state=$3,generation=generation+1,revision=revision+1 WHERE claim_id=$1`, id, charged, state)
	if err != nil {
		return Claim{}, err
	}
	if err = tx.Commit(); err != nil {
		return Claim{}, err
	}
	return l.Claim(ctx, id)
}

func (l *SQLLedger) updateLease(ctx context.Context, id string, generation uint64, statement string, args ...any) (Lease, error) {
	result, err := l.db.ExecContext(ctx, statement, args...)
	if err != nil {
		return Lease{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return Lease{}, ErrCASConflict
	}
	return l.lease(ctx, id)
}

func (l *SQLLedger) lease(ctx context.Context, id string) (Lease, error) {
	return scanLease(l.db.QueryRowContext(ctx, leaseSelect+` WHERE lease_id=$1`, id))
}

const leaseSelect = `SELECT lease_id,claim_id,datasource_id,owner_instance_id,owner_incarnation,claim_generation,generation,dial_permit_id,dial_token,dial_attempted,state,server_id,database_name,backend_pid,backend_started_at,disposition,proof_schema_id,proof_schema_version,proof_digest,inventory_epoch,charged,revision FROM b5_s3_leases`

func scanLease(row interface{ Scan(...any) error }) (Lease, error) {
	var value Lease
	var claimGeneration, generation, inventoryEpoch int64
	var serverID, databaseName, proofSchema sql.NullString
	var pid, proofVersion sql.NullInt64
	var started sql.NullTime
	var proofDigest []byte
	err := row.Scan(&value.ID, &value.ClaimID, &value.DatasourceID, &value.OwnerInstanceID, &value.OwnerIncarnation, &claimGeneration, &generation, &value.DialPermitID, &value.DialToken, &value.DialAttempted, &value.State, &serverID, &databaseName, &pid, &started, &value.Disposition, &proofSchema, &proofVersion, &proofDigest, &inventoryEpoch, &value.Charged, &value.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return Lease{}, store.ErrNotFound
	}
	if err != nil {
		return Lease{}, err
	}
	value.ClaimGeneration, value.Generation, value.InventoryEpoch = uint64(claimGeneration), uint64(generation), uint64(inventoryEpoch)
	if serverID.Valid {
		value.Backend.ServerID = serverID.String
	}
	if databaseName.Valid {
		value.Backend.Database = databaseName.String
	}
	if pid.Valid {
		value.Backend.PID = int32(pid.Int64)
	}
	if started.Valid {
		value.Backend.BackendStarted = started.Time
	}
	value.Backend.DialToken = value.DialToken
	if proofSchema.Valid {
		value.ProofSchemaID = proofSchema.String
	}
	if proofVersion.Valid {
		value.ProofSchemaVersion = uint16(proofVersion.Int64)
	}
	copy(value.ProofDigest[:], proofDigest)
	return value, nil
}

var _ LedgerStore = (*SQLLedger)(nil)

// Prevent driver-dependent loss of sub-microsecond precision in backend
// identity comparisons.
func normalizeBackendTime(value time.Time) time.Time { return value.UTC().Truncate(time.Microsecond) }
