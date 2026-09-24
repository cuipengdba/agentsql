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
	if fence.State == "frozen" || protocol < fence.MinReaderProtocol || protocol > fence.MaxWriterProtocol {
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
	var status string
	var expiry time.Time
	var revision int64
	selectQuery := `SELECT status,lease_expires_at,revision FROM runtime_instances WHERE instance_id=`
	if repository.dialect == DialectPostgres {
		selectQuery += `$1 FOR UPDATE`
	} else {
		selectQuery += `?`
	}
	err = tx.QueryRowContext(ctx, selectQuery, instance.InstanceID).Scan(&status, &expiry, &revision)
	newExpiry := now.Add(lease)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		query := `INSERT INTO runtime_instances(instance_id,protocol_version,artifact_digest,status,last_heartbeat_at,lease_expires_at,revision) VALUES(?,?,?,?,?,?,1)`
		if repository.dialect == DialectPostgres {
			query = `INSERT INTO runtime_instances(instance_id,protocol_version,artifact_digest,status,last_heartbeat_at,lease_expires_at,revision) VALUES($1,$2,$3,$4,$5,$6,1)`
		}
		_, err = tx.ExecContext(ctx, query, instance.InstanceID, instance.ProtocolVersion, instance.ArtifactDigest, "active", now, newExpiry)
		instance.Revision = 1
	case err != nil:
		return RuntimeInstance{}, rollbackFenceTx(tx, err)
	case status != "active" || !expiry.After(now):
		return RuntimeInstance{}, rollbackFenceTx(tx, ErrFenceLost)
	default:
		query := `UPDATE runtime_instances SET protocol_version=?,artifact_digest=?,last_heartbeat_at=?,lease_expires_at=?,revision=revision+1 WHERE instance_id=? AND revision=?`
		if repository.dialect == DialectPostgres {
			query = `UPDATE runtime_instances SET protocol_version=$1,artifact_digest=$2,last_heartbeat_at=$3,lease_expires_at=$4,revision=revision+1 WHERE instance_id=$5 AND revision=$6`
		}
		_, err = tx.ExecContext(ctx, query, instance.ProtocolVersion, instance.ArtifactDigest, now, newExpiry, instance.InstanceID, revision)
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

func rollbackFenceTx(tx *sql.Tx, cause error) error {
	if err := tx.Rollback(); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}
