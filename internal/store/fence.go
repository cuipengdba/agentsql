package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/lockrank"
	"github.com/cuipengdba/agentsql/internal/model"
)

var (
	ErrFenceLost       = errors.New("control-plane fence lost")
	ErrProtocolBlocked = errors.New("runtime protocol is not permitted")
	ErrActivationGate  = errors.New("protocol-3 activation gate is not ready")
)

const controlFenceAdvisoryKey int64 = 0x4153514c46454e43

type ControlFence struct {
	FenceKey          string
	MinReaderProtocol int
	MaxWriterProtocol int
	FenceEpoch        int64
	State             string
	Revision          int64
}

type RuntimeInstance struct {
	InstanceID      string
	ProtocolVersion int
	ArtifactDigest  string
	Status          string
	LastHeartbeatAt time.Time
	LeaseExpiresAt  time.Time
	Revision        int64
}

// Protocol3MetadataReadiness is the metadata half of the B2 activation gate.
// Business-database binder attestation is deliberately performed before this
// transaction so activation never creates a business -> control lock edge.
type Protocol3MetadataReadiness struct {
	StagingRows         int64
	WildcardPolicies    int64
	IncompleteBindings  int64
	UnhealthyBindings   int64
	OrphanedPermissions int64
	// ETag binds the phase-one metadata snapshot and control fence. Activation
	// must present it again while holding the exclusive fence.
	ETag string
}

func (readiness Protocol3MetadataReadiness) Ready() bool {
	return readiness.StagingRows == 0 && readiness.WildcardPolicies == 0 &&
		readiness.IncompleteBindings == 0 && readiness.UnhealthyBindings == 0 &&
		readiness.OrphanedPermissions == 0
}

// Protocol3Activation carries attestations that were produced without a
// control transaction. ActivateProtocol3 rechecks every metadata fact while
// holding the exclusive fence and atomically establishes the fence/runtime.
type Protocol3Activation struct {
	InstanceID       string
	ArtifactDigest   string
	ExpectedETag     string
	BinderReady      bool
	CatalogReady     bool
	ReservationReady bool
	Now              time.Time
	Lease            time.Duration
}

type FenceRepository struct{ repositoryBase }

// ControlSnapshot holds the reader fence and policy snapshot transaction.
// Business resources may only be acquired after this object exists.
type ControlSnapshot struct {
	tx       *sql.Tx
	dialect  Dialect
	fence    ControlFence
	instance RuntimeInstance
	closed   bool
	rank     *lockrank.Lease
}

// ColumnAuthorizationState is read entirely through the original control
// snapshot transaction. It contains no database handle and can be passed to
// the S4 execution coordinator without permitting a second metadata read.
type ColumnAuthorizationState struct {
	Policies       []model.Policy
	MaskRules      []model.MaskRule
	RevisionDigest string
}

func (snapshot *ControlSnapshot) ColumnAuthorizationState(ctx context.Context, agentID, datasourceID string) (ColumnAuthorizationState, error) {
	if snapshot == nil || snapshot.closed || ctx == nil || agentID == "" || datasourceID == "" {
		return ColumnAuthorizationState{}, ErrFenceLost
	}
	repository := &PolicyRepository{repositoryBase: repositoryBase{dialect: snapshot.dialect}}
	policies, err := repository.listWith(ctx, snapshot.tx, ` WHERE agent_id=? AND datasource_id=? ORDER BY created_at ASC,id ASC`, agentID, datasourceID)
	if err != nil {
		return ColumnAuthorizationState{}, errors.Join(ErrFenceLost, err)
	}
	rules, err := snapshot.listEnabledMaskRules(ctx, datasourceID)
	if err != nil {
		return ColumnAuthorizationState{}, errors.Join(ErrFenceLost, err)
	}
	h := sha256.New()
	_, _ = h.Write([]byte("agentsql-column-control-v2\x00"))
	_, _ = h.Write([]byte(strconv.FormatInt(snapshot.fence.Revision, 10) + "\x00" + strconv.FormatInt(snapshot.instance.Revision, 10)))
	for _, policy := range policies {
		_, _ = h.Write([]byte("\x00p:" + policy.ID + ":" + strconv.FormatInt(policy.Revision, 10)))
		if policy.RelationBinding != nil {
			_, _ = h.Write([]byte(":" + policy.RelationBinding.ID + ":" + strconv.FormatInt(policy.RelationBinding.Revision, 10)))
		}
	}
	for _, rule := range rules {
		_, _ = h.Write([]byte("\x00m:" + rule.ID + ":" + rule.UpdatedAt.UTC().Format(time.RFC3339Nano)))
	}
	return ColumnAuthorizationState{Policies: policies, MaskRules: rules, RevisionDigest: hex.EncodeToString(h.Sum(nil))}, nil
}

func (snapshot *ControlSnapshot) listEnabledMaskRules(ctx context.Context, datasourceID string) ([]model.MaskRule, error) {
	query := `SELECT id,datasource_id,COALESCE(schema_name,''),COALESCE(table_name,''),column_name,sensitive_type,algo,
 created_at,updated_at,enabled,range_bucket_width,range_bucket_offset,range_granularity
 FROM mask_rules WHERE (datasource_id=`
	if snapshot.dialect == DialectPostgres {
		query += `$1 OR datasource_id IS NULL OR BTRIM(datasource_id)='') AND enabled=$2`
	} else {
		query += `? OR datasource_id IS NULL OR TRIM(datasource_id)='') AND enabled=?`
	}
	query += ` ORDER BY schema_name,table_name,column_name,id`
	rows, err := snapshot.tx.QueryContext(ctx, query, datasourceID, true)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []model.MaskRule
	for rows.Next() {
		rule, err := scanMaskRule(rows)
		if err != nil {
			return nil, err
		}
		if rule.Enabled && (rule.DatasourceID == nil || strings.TrimSpace(*rule.DatasourceID) == "" || *rule.DatasourceID == datasourceID) {
			result = append(result, rule)
		}
	}
	return result, rows.Err()
}

func (repository *FenceRepository) BeginRead(ctx context.Context, protocol int, instanceID string, now time.Time) (*ControlSnapshot, error) {
	if ctx == nil {
		return nil, fmt.Errorf("begin control snapshot: %w", ErrNilContext)
	}
	rank, err := lockrank.Acquire(ctx, lockrank.Control)
	if err != nil {
		return nil, fmt.Errorf("begin control snapshot: %w", err)
	}
	rankTransferred := false
	defer func() {
		if !rankTransferred {
			rank.Release()
		}
	}()
	tx, err := repository.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: repository.dialect == DialectPostgres, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, fmt.Errorf("begin control snapshot: %w", err)
	}
	if repository.dialect == DialectPostgres {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock_shared($1)`, controlFenceAdvisoryKey); err != nil {
			return nil, rollbackFenceTx(tx, err)
		}
	}
	var fence ControlFence
	query := `SELECT fence_key,min_reader_protocol,max_writer_protocol,fence_epoch,state,revision FROM control_plane_compat WHERE fence_key=`
	if repository.dialect == DialectPostgres {
		query += `$1`
	} else {
		query += `?`
	}
	if err := tx.QueryRowContext(ctx, query, "global").Scan(&fence.FenceKey, &fence.MinReaderProtocol, &fence.MaxWriterProtocol, &fence.FenceEpoch, &fence.State, &fence.Revision); err != nil {
		return nil, rollbackFenceTx(tx, errors.Join(ErrFenceLost, err))
	}
	if fence.State == "frozen" || protocol < fence.MinReaderProtocol || protocol > fence.MaxWriterProtocol ||
		(protocol == 3 && fence.State != "protocol3") {
		return nil, rollbackFenceTx(tx, ErrProtocolBlocked)
	}
	var instance RuntimeInstance
	instanceQuery := `SELECT instance_id,protocol_version,artifact_digest,status,last_heartbeat_at,lease_expires_at,revision FROM runtime_instances WHERE instance_id=`
	if repository.dialect == DialectPostgres {
		instanceQuery += `$1`
	} else {
		instanceQuery += `?`
	}
	if err := tx.QueryRowContext(ctx, instanceQuery, instanceID).Scan(&instance.InstanceID, &instance.ProtocolVersion, &instance.ArtifactDigest, &instance.Status, &instance.LastHeartbeatAt, &instance.LeaseExpiresAt, &instance.Revision); err != nil {
		return nil, rollbackFenceTx(tx, errors.Join(ErrFenceLost, err))
	}
	if instance.Status != "active" || instance.ProtocolVersion != protocol || !instance.LeaseExpiresAt.After(now) {
		return nil, rollbackFenceTx(tx, ErrFenceLost)
	}
	rankTransferred = true
	return &ControlSnapshot{tx: tx, dialect: repository.dialect, fence: fence, instance: instance, rank: rank}, nil
}

type protocol3Queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// Protocol3Readiness reports the durable enrollment/catalog portion of the
// activation gate without mutating the fence.
func (repository *FenceRepository) Protocol3Readiness(ctx context.Context) (Protocol3MetadataReadiness, error) {
	if repository == nil || repository.db == nil || ctx == nil {
		return Protocol3MetadataReadiness{}, ErrActivationGate
	}
	return repository.protocol3ReadinessWith(ctx, repository.db)
}

// PrepareProtocol3Activation performs phase one of activation in one strong,
// read-only snapshot. The returned ETag is single-use in the sense that any
// intervening fence or readiness change makes phase two fail closed.
func (repository *FenceRepository) PrepareProtocol3Activation(ctx context.Context) (Protocol3MetadataReadiness, error) {
	if repository == nil || repository.db == nil || ctx == nil {
		return Protocol3MetadataReadiness{}, ErrActivationGate
	}
	tx, err := repository.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: repository.dialect == DialectPostgres, Isolation: sql.LevelSerializable})
	if err != nil {
		return Protocol3MetadataReadiness{}, errors.Join(ErrActivationGate, err)
	}
	readiness, err := repository.protocol3ReadinessWith(ctx, tx)
	if err != nil {
		return Protocol3MetadataReadiness{}, rollbackFenceTx(tx, err)
	}
	if err := tx.Commit(); err != nil {
		return Protocol3MetadataReadiness{}, errors.Join(ErrActivationGate, err)
	}
	return readiness, nil
}

func (repository *FenceRepository) protocol3ReadinessWith(ctx context.Context, queryer protocol3Queryer) (Protocol3MetadataReadiness, error) {
	var result Protocol3MetadataReadiness
	var fence ControlFence
	if err := queryer.QueryRowContext(ctx, repository.bind(`SELECT fence_key,min_reader_protocol,max_writer_protocol,fence_epoch,state,revision
FROM control_plane_compat WHERE fence_key=?`), "global").Scan(&fence.FenceKey, &fence.MinReaderProtocol,
		&fence.MaxWriterProtocol, &fence.FenceEpoch, &fence.State, &fence.Revision); err != nil {
		return Protocol3MetadataReadiness{}, errors.Join(ErrActivationGate, err)
	}
	counts := []struct {
		target *int64
		query  string
	}{
		{&result.StagingRows, `SELECT COUNT(*) FROM policy_column_permission_staging s
JOIN policies p ON p.id=s.policy_id JOIN datasources d ON d.id=p.datasource_id WHERE d.db_type='postgres'`},
		{&result.IncompleteBindings, `SELECT COUNT(*) FROM policies p
JOIN datasources d ON d.id=p.datasource_id
WHERE d.db_type='postgres' AND p.object_type='column' AND (p.relation_binding_id IS NULL OR NOT EXISTS (
 SELECT 1 FROM relation_policy_bindings b WHERE b.id=p.relation_binding_id AND b.policy_id=p.id
 AND b.stable_object_id IS NOT NULL AND b.stable_object_id<>''
 AND b.catalog_fingerprint IS NOT NULL AND b.catalog_fingerprint<>''))`},
		{&result.UnhealthyBindings, `SELECT COUNT(*) FROM relation_policy_bindings b
JOIN policies p ON p.id=b.policy_id JOIN datasources d ON d.id=p.datasource_id
WHERE d.db_type='postgres' AND b.status<>'healthy'`},
		{&result.OrphanedPermissions, `SELECT COUNT(*) FROM policy_column_permissions pc
JOIN policies p ON p.id=pc.policy_id JOIN datasources d ON d.id=p.datasource_id
WHERE d.db_type='postgres' AND NOT EXISTS (SELECT 1 FROM relation_policy_bindings b
 WHERE b.id=pc.relation_enrollment_id AND b.policy_id=pc.policy_id AND b.status='healthy')`},
	}
	for _, count := range counts {
		if err := queryer.QueryRowContext(ctx, count.query).Scan(count.target); err != nil {
			return Protocol3MetadataReadiness{}, errors.Join(ErrActivationGate, err)
		}
	}
	rows, err := queryer.QueryContext(ctx, `SELECT COALESCE(p.columns,'') FROM policies p
JOIN datasources d ON d.id=p.datasource_id WHERE d.db_type='postgres' AND p.object_type='column'`)
	if err != nil {
		return Protocol3MetadataReadiness{}, errors.Join(ErrActivationGate, err)
	}
	defer rows.Close()
	for rows.Next() {
		var legacy string
		if err := rows.Scan(&legacy); err != nil {
			return Protocol3MetadataReadiness{}, errors.Join(ErrActivationGate, err)
		}
		for _, token := range strings.Split(legacy, ",") {
			if strings.TrimSpace(token) == "*" {
				result.WildcardPolicies++
			}
		}
	}
	if err := rows.Err(); err != nil {
		return Protocol3MetadataReadiness{}, errors.Join(ErrActivationGate, err)
	}
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "agentsql-protocol3-activation-v1\x00%s\x00%d\x00%d\x00%d\x00%s\x00%d\x00%d\x00%d\x00%d\x00%d\x00%d",
		fence.FenceKey, fence.MinReaderProtocol, fence.MaxWriterProtocol, fence.FenceEpoch, fence.State, fence.Revision,
		result.StagingRows, result.WildcardPolicies, result.IncompleteBindings, result.UnhealthyBindings, result.OrphanedPermissions)
	result.ETag = `"p3-` + hex.EncodeToString(hash.Sum(nil)) + `"`
	return result, nil
}

// ActivateProtocol3 atomically establishes the protocol-3 fence and the
// first live runtime lease. It never weakens the protocol-2 defaults unless
// every supplied and durable readiness fact is true.
func (repository *FenceRepository) ActivateProtocol3(ctx context.Context, activation Protocol3Activation) (RuntimeInstance, error) {
	if repository == nil || repository.db == nil || ctx == nil || activation.InstanceID == "" ||
		activation.ArtifactDigest == "" || activation.ExpectedETag == "" || activation.Lease <= 0 || activation.Now.IsZero() ||
		!activation.BinderReady || !activation.CatalogReady || !activation.ReservationReady {
		return RuntimeInstance{}, ErrActivationGate
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return RuntimeInstance{}, errors.Join(ErrActivationGate, err)
	}
	if repository.dialect == DialectPostgres {
		if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, controlFenceAdvisoryKey); err != nil {
			return RuntimeInstance{}, rollbackFenceTx(tx, errors.Join(ErrActivationGate, err))
		}
	} else if _, err = tx.ExecContext(ctx, `UPDATE control_plane_compat SET revision=revision WHERE fence_key='global'`); err != nil {
		return RuntimeInstance{}, rollbackFenceTx(tx, errors.Join(ErrActivationGate, err))
	}
	readiness, err := repository.protocol3ReadinessWith(ctx, tx)
	if err != nil || !readiness.Ready() || readiness.ETag != activation.ExpectedETag {
		return RuntimeInstance{}, rollbackFenceTx(tx, errors.Join(ErrActivationGate, err))
	}
	var state string
	if err := tx.QueryRowContext(ctx, repository.bind(`SELECT state FROM control_plane_compat WHERE fence_key=?`), "global").Scan(&state); err != nil || state == "frozen" {
		return RuntimeInstance{}, rollbackFenceTx(tx, errors.Join(ErrActivationGate, err))
	}
	var incompatible int64
	if err := tx.QueryRowContext(ctx, repository.bind(`SELECT COUNT(*) FROM runtime_instances
WHERE status='active' AND lease_expires_at>?
AND (protocol_version<>3 OR artifact_digest<>?)`), activation.Now, activation.ArtifactDigest).Scan(&incompatible); err != nil || incompatible != 0 {
		return RuntimeInstance{}, rollbackFenceTx(tx, errors.Join(ErrActivationGate, err))
	}
	if _, err := tx.ExecContext(ctx, repository.bind(`UPDATE control_plane_compat
SET min_reader_protocol=3,max_writer_protocol=3,state='protocol3',fence_epoch=fence_epoch+1,revision=revision+1,updated_at=?
WHERE fence_key='global' AND state<>'frozen'`), activation.Now); err != nil {
		return RuntimeInstance{}, rollbackFenceTx(tx, errors.Join(ErrActivationGate, err))
	}
	expires := activation.Now.Add(activation.Lease)
	if _, err := tx.ExecContext(ctx, repository.bind(`INSERT INTO runtime_instances(
instance_id,protocol_version,artifact_digest,status,last_heartbeat_at,lease_expires_at,revision)
VALUES(?,3,?,'active',?,?,1)
ON CONFLICT(instance_id) DO UPDATE SET protocol_version=3,artifact_digest=excluded.artifact_digest,status='active',
last_heartbeat_at=excluded.last_heartbeat_at,lease_expires_at=excluded.lease_expires_at,revision=runtime_instances.revision+1`),
		activation.InstanceID, activation.ArtifactDigest, activation.Now, expires); err != nil {
		return RuntimeInstance{}, rollbackFenceTx(tx, errors.Join(ErrActivationGate, err))
	}
	var instance RuntimeInstance
	if err := tx.QueryRowContext(ctx, repository.bind(`SELECT instance_id,protocol_version,artifact_digest,status,last_heartbeat_at,lease_expires_at,revision
FROM runtime_instances WHERE instance_id=?`), activation.InstanceID).Scan(&instance.InstanceID, &instance.ProtocolVersion, &instance.ArtifactDigest, &instance.Status, &instance.LastHeartbeatAt, &instance.LeaseExpiresAt, &instance.Revision); err != nil {
		return RuntimeInstance{}, rollbackFenceTx(tx, errors.Join(ErrActivationGate, err))
	}
	if err := tx.Commit(); err != nil {
		return RuntimeInstance{}, errors.Join(ErrActivationGate, err)
	}
	return instance, nil
}

// FinalCheck runs in the original snapshot immediately before sealing.
func (snapshot *ControlSnapshot) FinalCheck(ctx context.Context, now time.Time) error {
	if snapshot == nil || snapshot.closed {
		return ErrFenceLost
	}
	var fenceRevision, fenceEpoch int64
	var state string
	query := `SELECT revision,fence_epoch,state FROM control_plane_compat WHERE fence_key=`
	if snapshot.dialect == DialectPostgres {
		query += `$1`
	} else {
		query += `?`
	}
	if err := snapshot.tx.QueryRowContext(ctx, query, snapshot.fence.FenceKey).Scan(&fenceRevision, &fenceEpoch, &state); err != nil {
		return errors.Join(ErrFenceLost, err)
	}
	var instanceRevision int64
	var status string
	var expiry time.Time
	instanceQuery := `SELECT revision,status,lease_expires_at FROM runtime_instances WHERE instance_id=`
	if snapshot.dialect == DialectPostgres {
		instanceQuery += `$1`
	} else {
		instanceQuery += `?`
	}
	if err := snapshot.tx.QueryRowContext(ctx, instanceQuery, snapshot.instance.InstanceID).Scan(&instanceRevision, &status, &expiry); err != nil {
		return errors.Join(ErrFenceLost, err)
	}
	if fenceRevision != snapshot.fence.Revision || fenceEpoch != snapshot.fence.FenceEpoch || state == "frozen" || instanceRevision != snapshot.instance.Revision || status != "active" || !expiry.After(now) {
		return ErrFenceLost
	}
	return nil
}

func (snapshot *ControlSnapshot) Close() error {
	if snapshot == nil || snapshot.closed {
		return nil
	}
	snapshot.closed = true
	err := snapshot.tx.Rollback()
	snapshot.rank.Release()
	return err
}

// Heartbeat inserts a new runtime or renews a still-authorized live runtime.
// An expired or revoked identity cannot resurrect itself.
func (repository *FenceRepository) Heartbeat(ctx context.Context, instance RuntimeInstance, now time.Time, lease time.Duration) (RuntimeInstance, error) {
	if lease <= 0 || instance.InstanceID == "" || instance.ArtifactDigest == "" || instance.ProtocolVersion < 1 {
		return RuntimeInstance{}, fmt.Errorf("invalid runtime heartbeat")
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return RuntimeInstance{}, err
	}
	if repository.dialect == DialectPostgres {
		if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, controlFenceAdvisoryKey); err != nil {
			return RuntimeInstance{}, rollbackFenceTx(tx, err)
		}
	} else if _, err = tx.ExecContext(ctx, `UPDATE control_plane_compat SET revision=revision WHERE fence_key='global'`); err != nil {
		return RuntimeInstance{}, rollbackFenceTx(tx, err)
	}
	var fenceState string
	var minReader, maxWriter int
	if err = tx.QueryRowContext(ctx, repository.bind(`SELECT state,min_reader_protocol,max_writer_protocol FROM control_plane_compat WHERE fence_key=?`), "global").Scan(&fenceState, &minReader, &maxWriter); err != nil {
		return RuntimeInstance{}, rollbackFenceTx(tx, errors.Join(ErrFenceLost, err))
	}
	if fenceState == "frozen" || instance.ProtocolVersion < minReader || instance.ProtocolVersion > maxWriter ||
		(instance.ProtocolVersion == 3 && fenceState != "protocol3") {
		return RuntimeInstance{}, rollbackFenceTx(tx, ErrProtocolBlocked)
	}
	if instance.ProtocolVersion == 3 {
		var incompatible int64
		if err = tx.QueryRowContext(ctx, repository.bind(`SELECT COUNT(*) FROM runtime_instances
WHERE status='active' AND lease_expires_at>? AND instance_id<>? AND artifact_digest<>?`),
			now, instance.InstanceID, instance.ArtifactDigest).Scan(&incompatible); err != nil || incompatible != 0 {
			return RuntimeInstance{}, rollbackFenceTx(tx, errors.Join(ErrFenceLost, err))
		}
	}
	var status string
	var expiry time.Time
	var revision int64
	var storedProtocol int
	var storedArtifact string
	selectQuery := `SELECT status,lease_expires_at,revision,protocol_version,artifact_digest FROM runtime_instances WHERE instance_id=`
	if repository.dialect == DialectPostgres {
		selectQuery += `$1 FOR UPDATE`
	} else {
		selectQuery += `?`
	}
	err = tx.QueryRowContext(ctx, selectQuery, instance.InstanceID).Scan(&status, &expiry, &revision, &storedProtocol, &storedArtifact)
	newExpiry := now.Add(lease)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if instance.ProtocolVersion == 3 {
			return RuntimeInstance{}, rollbackFenceTx(tx, ErrFenceLost)
		}
		query := `INSERT INTO runtime_instances(instance_id,protocol_version,artifact_digest,status,last_heartbeat_at,lease_expires_at,revision) VALUES(?,?,?,?,?,?,1)`
		if repository.dialect == DialectPostgres {
			query = `INSERT INTO runtime_instances(instance_id,protocol_version,artifact_digest,status,last_heartbeat_at,lease_expires_at,revision) VALUES($1,$2,$3,$4,$5,$6,1)`
		}
		_, err = tx.ExecContext(ctx, query, instance.InstanceID, instance.ProtocolVersion, instance.ArtifactDigest, "active", now, newExpiry)
		instance.Revision = 1
	case err != nil:
		return RuntimeInstance{}, rollbackFenceTx(tx, err)
	case status != "active" || !expiry.After(now) || storedProtocol != instance.ProtocolVersion || storedArtifact != instance.ArtifactDigest:
		return RuntimeInstance{}, rollbackFenceTx(tx, ErrFenceLost)
	default:
		query := `UPDATE runtime_instances SET protocol_version=?,artifact_digest=?,last_heartbeat_at=?,lease_expires_at=?,revision=revision+1 WHERE instance_id=? AND revision=?`
		if repository.dialect == DialectPostgres {
			query = `UPDATE runtime_instances SET protocol_version=$1,artifact_digest=$2,last_heartbeat_at=$3,lease_expires_at=$4,revision=revision+1 WHERE instance_id=$5 AND revision=$6`
		}
		var update sql.Result
		update, err = tx.ExecContext(ctx, query, instance.ProtocolVersion, instance.ArtifactDigest, now, newExpiry, instance.InstanceID, revision)
		if err == nil {
			if affected, affectedErr := update.RowsAffected(); affectedErr != nil || affected != 1 {
				err = errors.Join(ErrFenceLost, affectedErr)
			}
		}
		instance.Revision = revision + 1
	}
	if err != nil {
		return RuntimeInstance{}, rollbackFenceTx(tx, err)
	}
	if err := tx.Commit(); err != nil {
		return RuntimeInstance{}, err
	}
	instance.Status, instance.LastHeartbeatAt, instance.LeaseExpiresAt = "active", now, newExpiry
	return instance, nil
}

// DrainRuntime prevents new snapshots from using this instance. It is a
// best-effort shutdown operation; lease expiry remains the authoritative
// fail-closed boundary if the control plane is unavailable.
func (repository *FenceRepository) DrainRuntime(ctx context.Context, instanceID string) error {
	if repository == nil || repository.db == nil || ctx == nil || instanceID == "" {
		return ErrFenceLost
	}
	result, err := repository.db.ExecContext(ctx, repository.bind(`UPDATE runtime_instances
SET status='draining',revision=revision+1 WHERE instance_id=? AND status='active'`), instanceID)
	if err != nil {
		return errors.Join(ErrFenceLost, err)
	}
	if affected, affectedErr := result.RowsAffected(); affectedErr != nil || affected > 1 {
		return errors.Join(ErrFenceLost, affectedErr)
	}
	return nil
}

func rollbackFenceTx(tx *sql.Tx, cause error) error {
	if err := tx.Rollback(); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}
