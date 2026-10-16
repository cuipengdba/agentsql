package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
)

// ErrTaskReadGrantInvalid is returned when a temporary grant violates its
// bounded, tenant-scoped storage contract.
var ErrTaskReadGrantInvalid = errors.New("invalid task read grant")

const MaxTaskReadGrantDuration = 24 * time.Hour
const MaxTaskReadGrantUses int64 = 10

type TaskReadGrantRepository struct{ repositoryBase }

// Create stores only hashes. The INSERT's tenant predicates are the final
// boundary even if an API caller provides a foreign Agent or datasource ID.
func (repository *TaskReadGrantRepository) Create(ctx context.Context, grant model.TaskReadGrant) (model.TaskReadGrant, error) {
	tenantID, err := repository.requireTenant(ctx, "create task read grant")
	if err != nil {
		return model.TaskReadGrant{}, err
	}
	now := time.Now().UTC()
	if grant.TenantID != tenantID || strings.TrimSpace(grant.ID) == "" ||
		strings.TrimSpace(grant.IssuerUserID) == "" || strings.TrimSpace(grant.AgentID) == "" ||
		strings.TrimSpace(grant.DatasourceID) == "" || validateAPIKeyHash(grant.KeyHash) != nil ||
		validateAPIKeyHash(grant.SQLSHA256) != nil || !grant.ExpiresAt.After(now) ||
		grant.ExpiresAt.After(now.Add(MaxTaskReadGrantDuration)) || grant.RevokedAt != nil ||
		grant.UsedCount != 0 || (grant.MaxUses != nil && (*grant.MaxUses < 1 || *grant.MaxUses > MaxTaskReadGrantUses)) {
		return model.TaskReadGrant{}, ErrTaskReadGrantInvalid
	}
	result, err := repository.db.ExecContext(ctx, repository.bind(`
INSERT INTO task_read_grants
  (id, tenant_id, issuer_user_id, agent_id, datasource_id, sql_sha256, key_hash, expires_at, max_uses)
SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?
WHERE EXISTS (SELECT 1 FROM users WHERE id = ? AND tenant_id = ? AND status = 'active')
  AND EXISTS (SELECT 1 FROM tenants WHERE id = ? AND status = 'active')
  AND EXISTS (SELECT 1 FROM agents WHERE id = ? AND tenant_id = ? AND status = 'active'
              AND (expires_at IS NULL OR expires_at > ?))
  AND EXISTS (SELECT 1 FROM datasources WHERE id = ? AND tenant_id = ?)`),
		grant.ID, tenantID, grant.IssuerUserID, grant.AgentID, grant.DatasourceID,
		grant.SQLSHA256, grant.KeyHash, grant.ExpiresAt.UTC(), grant.MaxUses,
		grant.IssuerUserID, tenantID, tenantID, grant.AgentID, tenantID, now,
		grant.DatasourceID, tenantID,
	)
	if err != nil {
		return model.TaskReadGrant{}, fmt.Errorf("create task read grant: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return model.TaskReadGrant{}, fmt.Errorf("create task read grant: %w", err)
	}
	if affected != 1 {
		return model.TaskReadGrant{}, ErrTaskReadGrantInvalid
	}
	return repository.Get(ctx, grant.ID)
}

func (repository *TaskReadGrantRepository) Get(ctx context.Context, id string) (model.TaskReadGrant, error) {
	tenantID, err := repository.requireTenant(ctx, "get task read grant")
	if err != nil {
		return model.TaskReadGrant{}, err
	}
	grant, err := scanTaskReadGrant(repository.db.QueryRowContext(ctx, repository.bind(`
SELECT id, tenant_id, issuer_user_id, agent_id, datasource_id, sql_sha256,
       key_hash, expires_at, revoked_at, created_at, max_uses, used_count
FROM task_read_grants WHERE id = ? AND tenant_id = ?`), id, tenantID))
	if errors.Is(err, sql.ErrNoRows) {
		return model.TaskReadGrant{}, ErrNotFound
	}
	if err != nil {
		return model.TaskReadGrant{}, fmt.Errorf("get task read grant: %w", err)
	}
	return grant, nil
}

// Validate performs a fresh storage read for every execution gate. The tenant
// and Agent IDs come from authentication, never from SQL parameters or headers.
func (repository *TaskReadGrantRepository) Validate(
	ctx context.Context, id, tenantID, agentID, datasourceID, sqlSHA256 string, now time.Time,
) (model.TaskReadGrant, error) {
	if ctx == nil || strings.TrimSpace(id) == "" || strings.TrimSpace(tenantID) == "" ||
		strings.TrimSpace(agentID) == "" || strings.TrimSpace(datasourceID) == "" ||
		validateAPIKeyHash(sqlSHA256) != nil || now.IsZero() {
		return model.TaskReadGrant{}, ErrTaskReadGrantInvalid
	}
	grant, err := scanTaskReadGrant(repository.db.QueryRowContext(ctx, repository.bind(`
SELECT g.id, g.tenant_id, g.issuer_user_id, g.agent_id, g.datasource_id, g.sql_sha256,
       g.key_hash, g.expires_at, g.revoked_at, g.created_at, g.max_uses, g.used_count
FROM task_read_grants AS g
JOIN agents AS a ON a.id = g.agent_id AND a.tenant_id = g.tenant_id
JOIN datasources AS d ON d.id = g.datasource_id AND d.tenant_id = g.tenant_id
JOIN users AS u ON u.id = g.issuer_user_id AND u.tenant_id = g.tenant_id
JOIN tenants AS t ON t.id = g.tenant_id
WHERE g.id = ? AND g.tenant_id = ? AND a.status = 'active' AND u.status = 'active'
  AND t.status = 'active'
  AND (a.expires_at IS NULL OR a.expires_at > ?)`), id, tenantID, now))
	if errors.Is(err, sql.ErrNoRows) {
		return model.TaskReadGrant{}, ErrTaskReadGrantInvalid
	}
	if err != nil {
		return model.TaskReadGrant{}, fmt.Errorf("validate task read grant: %w", err)
	}
	if grant.AgentID != agentID || grant.DatasourceID != datasourceID ||
		grant.SQLSHA256 != sqlSHA256 || grant.RevokedAt != nil || !now.Before(grant.ExpiresAt) ||
		(grant.MaxUses != nil && grant.UsedCount >= *grant.MaxUses) {
		return model.TaskReadGrant{}, ErrTaskReadGrantInvalid
	}
	return grant, nil
}

// Consume claims one bounded result before the gateway returns it. The
// conditional UPDATE is the cross-instance fence: concurrent requests may
// execute, but no more than max_uses can return a result. A consumed slot is
// deliberately not refunded after a later transport failure.
func (repository *TaskReadGrantRepository) Consume(
	ctx context.Context, id, tenantID, agentID, datasourceID, sqlSHA256 string, now time.Time,
) error {
	if ctx == nil || strings.TrimSpace(id) == "" || strings.TrimSpace(tenantID) == "" ||
		strings.TrimSpace(agentID) == "" || strings.TrimSpace(datasourceID) == "" ||
		validateAPIKeyHash(sqlSHA256) != nil || now.IsZero() {
		return ErrTaskReadGrantInvalid
	}
	result, err := repository.db.ExecContext(ctx, repository.bind(`
UPDATE task_read_grants SET used_count = used_count + 1
WHERE id = ? AND tenant_id = ? AND agent_id = ? AND datasource_id = ? AND sql_sha256 = ?
  AND revoked_at IS NULL AND expires_at > ? AND max_uses IS NOT NULL AND used_count < max_uses
  AND EXISTS (SELECT 1 FROM tenants WHERE id = ? AND status = 'active')
  AND EXISTS (SELECT 1 FROM users WHERE id = issuer_user_id AND tenant_id = ? AND status = 'active')
  AND EXISTS (SELECT 1 FROM agents WHERE id = ? AND tenant_id = ? AND status = 'active'
              AND (expires_at IS NULL OR expires_at > ?))
  AND EXISTS (SELECT 1 FROM datasources WHERE id = ? AND tenant_id = ?)`),
		id, tenantID, agentID, datasourceID, sqlSHA256, now.UTC(),
		tenantID, tenantID, agentID, tenantID, now.UTC(), datasourceID, tenantID)
	if err != nil {
		return fmt.Errorf("consume task read grant: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("consume task read grant: %w", err)
	}
	if affected != 1 {
		return ErrTaskReadGrantInvalid
	}
	return nil
}

func (repository *TaskReadGrantRepository) List(ctx context.Context) ([]model.TaskReadGrant, error) {
	tenantID, err := repository.requireTenant(ctx, "list task read grants")
	if err != nil {
		return nil, err
	}
	rows, err := repository.db.QueryContext(ctx, repository.bind(`
SELECT id, tenant_id, issuer_user_id, agent_id, datasource_id, sql_sha256,
       key_hash, expires_at, revoked_at, created_at, max_uses, used_count
FROM task_read_grants WHERE tenant_id = ? ORDER BY created_at DESC, id DESC`), tenantID)
	if err != nil {
		return nil, fmt.Errorf("list task read grants: %w", err)
	}
	defer rows.Close()
	grants := make([]model.TaskReadGrant, 0)
	for rows.Next() {
		grant, scanErr := scanTaskReadGrant(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan task read grants: %w", scanErr)
		}
		grants = append(grants, grant)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate task read grants: %w", err)
	}
	return grants, nil
}

// Revoke is idempotent and visible to subsequent reads immediately after the
// update commits. It never reactivates an expired or already revoked grant.
func (repository *TaskReadGrantRepository) Revoke(ctx context.Context, id string) (model.TaskReadGrant, error) {
	tenantID, err := repository.requireTenant(ctx, "revoke task read grant")
	if err != nil {
		return model.TaskReadGrant{}, err
	}
	result, err := repository.db.ExecContext(ctx, repository.bind(`
UPDATE task_read_grants SET revoked_at = COALESCE(revoked_at, ?)
WHERE id = ? AND tenant_id = ?`), time.Now().UTC(), id, tenantID)
	if err != nil {
		return model.TaskReadGrant{}, fmt.Errorf("revoke task read grant: %w", err)
	}
	if err := checkRowsAffected(result, "task read grant", id); err != nil {
		return model.TaskReadGrant{}, err
	}
	return repository.Get(ctx, id)
}

func scanTaskReadGrant(scanner rowScanner) (model.TaskReadGrant, error) {
	var grant model.TaskReadGrant
	var maxUses sql.NullInt64
	var expiresAt, revokedAt, createdAt databaseTimestamp
	if err := scanner.Scan(&grant.ID, &grant.TenantID, &grant.IssuerUserID,
		&grant.AgentID, &grant.DatasourceID, &grant.SQLSHA256, &grant.KeyHash,
		&expiresAt, &revokedAt, &createdAt, &maxUses, &grant.UsedCount); err != nil {
		return model.TaskReadGrant{}, err
	}
	var err error
	grant.ExpiresAt, err = expiresAt.required("task_read_grants.expires_at")
	if err != nil {
		return model.TaskReadGrant{}, err
	}
	grant.CreatedAt, err = createdAt.required("task_read_grants.created_at")
	if err != nil {
		return model.TaskReadGrant{}, err
	}
	grant.RevokedAt = revokedAt.pointer()
	if maxUses.Valid {
		grant.MaxUses = &maxUses.Int64
	}
	return grant, nil
}
