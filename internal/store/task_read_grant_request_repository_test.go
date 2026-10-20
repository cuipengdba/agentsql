package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func taskGrantRequestFixture(t *testing.T) (*Store, model.TaskReadGrantRequest) {
	t.Helper()
	opened := openTestStore(t)
	agent, datasource := createPolicyDependencies(t, opened)
	for _, id := range []string{"requester-task", "reviewer-task"} {
		_, err := opened.metaDB.ExecContext(t.Context(), `
INSERT INTO users (id, tenant_id, username, display_name, password_hash, status)
VALUES (?, 'tenant_default', ?, ?, 'test-hash', 'active')`, id, id, id)
		require.NoError(t, err)
	}
	sqlDigest := sha256.Sum256([]byte("SELECT id FROM public.orders LIMIT 1"))
	return opened, model.TaskReadGrantRequest{
		ID: "task-request-one", TenantID: DefaultTenantID, RequesterUserID: "requester-task",
		AgentID: agent.ID, DatasourceID: datasource.ID, SQLSHA256: hex.EncodeToString(sqlDigest[:]),
		SQLCiphertext:  "encrypted-sql-test-value",
		IdempotencyKey: "request-key-one", ExpiresAt: time.Now().Add(time.Hour),
	}
}

func TestTaskReadGrantRequestSeparationIdempotencyAndSingleIssue(t *testing.T) {
	opened, input := taskGrantRequestFixture(t)
	two := int64(2)
	input.MaxUses = &two
	ctx := context.Background()
	repository := opened.TaskReadGrantRequests()
	created, err := repository.Create(ctx, input)
	require.NoError(t, err)
	require.Equal(t, "pending", created.Status)
	require.EqualValues(t, 2, *created.MaxUses)
	duplicate := input
	duplicate.ID = "task-request-duplicate"
	resubmitted, err := repository.Create(ctx, duplicate)
	require.NoError(t, err)
	require.Equal(t, created.ID, resubmitted.ID)
	changed := duplicate
	changed.ID = "task-request-changed"
	changed.SQLSHA256 = HashAPIKey("different SQL")
	_, err = repository.Create(ctx, changed)
	require.ErrorIs(t, err, ErrTaskReadGrantRequestConflict)
	changed = duplicate
	three := int64(3)
	changed.MaxUses = &three
	_, err = repository.Create(ctx, changed)
	require.ErrorIs(t, err, ErrTaskReadGrantRequestConflict)
	_, err = repository.Review(ctx, created.ID, input.RequesterUserID, true)
	require.ErrorIs(t, err, ErrTaskReadGrantRequestConflict)
	approved, err := repository.Review(ctx, created.ID, "reviewer-task", true)
	require.NoError(t, err)
	require.Equal(t, "approved", approved.Status)
	_, err = repository.Review(ctx, created.ID, "reviewer-task", true)
	require.ErrorIs(t, err, ErrTaskReadGrantRequestConflict)
	key, hash, err := GenerateAPIKey()
	require.NoError(t, err)
	_, err = repository.Issue(ctx, created.ID, "reviewer-task", "wrong-requester-grant", hash)
	require.ErrorIs(t, err, ErrTaskReadGrantRequestConflict)
	issued, err := repository.Issue(ctx, created.ID, input.RequesterUserID, "grant-from-request", hash)
	require.NoError(t, err)
	require.Equal(t, "issued", issued.Status)
	require.Equal(t, "grant-from-request", *issued.GrantID)
	_, err = repository.Issue(ctx, created.ID, input.RequesterUserID, "second-grant", HashAPIKey("second"))
	require.ErrorIs(t, err, ErrTaskReadGrantRequestConflict)
	grants, err := opened.TaskReadGrants().List(ctx)
	require.NoError(t, err)
	require.Len(t, grants, 1)
	require.EqualValues(t, 2, *grants[0].MaxUses)
	require.Zero(t, grants[0].UsedCount)
	matched, err := opened.Agents().GetByAPIKeyHash(ctx, HashAPIKey(key))
	require.NoError(t, err)
	require.Equal(t, "grant-from-request", matched.TaskGrantID)
	otherTenant, err := WithTenant(ctx, "other-tenant")
	require.NoError(t, err)
	_, err = repository.Get(otherTenant, created.ID)
	require.ErrorIs(t, err, ErrNotFound)
	listed, err := repository.List(ctx, "requester-task", false)
	require.NoError(t, err)
	require.Len(t, listed, 1)
}

func TestTaskReadGrantIssueAuditFailureRollsBackKeyAndState(t *testing.T) {
	opened, input := taskGrantRequestFixture(t)
	ctx := context.Background()
	repository := opened.TaskReadGrantRequests()
	created, err := repository.Create(ctx, input)
	require.NoError(t, err)
	_, err = repository.Review(ctx, created.ID, "reviewer-task", true)
	require.NoError(t, err)
	_, err = opened.metaDB.ExecContext(ctx, `
CREATE TRIGGER deny_task_issue_audit BEFORE INSERT ON audit_logs
WHEN NEW.action = 'task_read_grant_issue'
BEGIN SELECT RAISE(FAIL, 'injected audit failure'); END`)
	require.NoError(t, err)
	_, hash, err := GenerateAPIKey()
	require.NoError(t, err)
	_, err = repository.Issue(ctx, created.ID, input.RequesterUserID, "never-issued", hash)
	require.Error(t, err)
	stored, err := repository.Get(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, "approved", stored.Status)
	require.Nil(t, stored.GrantID)
	_, err = opened.TaskReadGrants().Get(ctx, "never-issued")
	require.True(t, errors.Is(err, ErrNotFound))
}

func TestTaskReadGrantRequestRollbackRevokesIssuedKeys(t *testing.T) {
	opened, input := taskGrantRequestFixture(t)
	ctx := context.Background()
	repository := opened.TaskReadGrantRequests()
	created, err := repository.Create(ctx, input)
	require.NoError(t, err)
	_, err = repository.Review(ctx, created.ID, "reviewer-task", true)
	require.NoError(t, err)
	_, hash, err := GenerateAPIKey()
	require.NoError(t, err)
	_, err = repository.Issue(ctx, created.ID, input.RequesterUserID, "rollback-grant", hash)
	require.NoError(t, err)
	current, latest, err := MetadataMigrationVersions(ctx, opened.metaDB, DialectSQLite, false)
	require.NoError(t, err)
	require.Equal(t, 23, current)
	require.Equal(t, current, latest)
	require.NoError(t, RollbackMetadataMigration(ctx, opened.metaDB, DialectSQLite, false, current))
	require.NoError(t, RollbackMetadataMigration(ctx, opened.metaDB, DialectSQLite, false, current-1))
	require.NoError(t, RollbackMetadataMigration(ctx, opened.metaDB, DialectSQLite, false, current-2))
	require.NoError(t, RollbackMetadataMigration(ctx, opened.metaDB, DialectSQLite, false, current-3))
	require.NoError(t, RollbackMetadataMigration(ctx, opened.metaDB, DialectSQLite, false, current-4))
	var revokedCount int
	require.NoError(t, opened.metaDB.QueryRowContext(ctx,
		`SELECT count(*) FROM task_read_grants WHERE id = ? AND revoked_at IS NOT NULL`,
		"rollback-grant").Scan(&revokedCount))
	require.Equal(t, 1, revokedCount)
	require.NoError(t, MigrateMetadata(ctx, opened.metaDB, DialectSQLite, false))
}

func TestV070SQLiteSchemaUpgradeAndBackupRestorePreserveExistingGrant(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "upgrade.db")
	opened, err := OpenWithSecret(ctx, path, []byte(testSecret))
	require.NoError(t, err)
	agent, datasource := createPolicyDependencies(t, opened)
	_, err = opened.RBAC().CreateUser(ctx, model.User{
		ID: "v070-issuer", TenantID: DefaultTenantID, Username: "v070-issuer",
		DisplayName: "v070-issuer", PasswordHash: "opaque-test-hash", Status: "active",
	})
	require.NoError(t, err)
	_, hash, err := GenerateAPIKey()
	require.NoError(t, err)
	legacy, err := opened.TaskReadGrants().Create(ctx, model.TaskReadGrant{
		ID: "v070-existing-grant", TenantID: DefaultTenantID, IssuerUserID: "v070-issuer",
		AgentID: agent.ID, DatasourceID: datasource.ID, SQLSHA256: HashAPIKey("SELECT id FROM public.orders LIMIT 1"),
		KeyHash: hash, ExpiresAt: time.Now().Add(time.Hour),
	})
	require.NoError(t, err)
	require.NoError(t, opened.Close())

	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	require.NoError(t, RollbackMetadataMigration(ctx, raw, DialectSQLite, false, 23))
	require.NoError(t, RollbackMetadataMigration(ctx, raw, DialectSQLite, false, 22))
	require.NoError(t, RollbackMetadataMigration(ctx, raw, DialectSQLite, false, 21))
	require.NoError(t, RollbackMetadataMigration(ctx, raw, DialectSQLite, false, 20))
	require.NoError(t, RollbackMetadataMigration(ctx, raw, DialectSQLite, false, 19))
	require.NoError(t, raw.Close())
	backup, err := os.ReadFile(path)
	require.NoError(t, err)
	options := MetadataOptions{Driver: DialectSQLite, SQLitePath: path, AutoMigrate: false}
	_, err = OpenMetadata(ctx, options, []byte(testSecret))
	require.ErrorContains(t, err, "current=18 latest=23")
	raw, err = sql.Open("sqlite", path)
	require.NoError(t, err)
	require.NoError(t, MigrateMetadata(ctx, raw, DialectSQLite, false))
	require.NoError(t, raw.Close())
	upgraded, err := OpenMetadata(ctx, options, []byte(testSecret))
	require.NoError(t, err)
	retained, err := upgraded.TaskReadGrants().Get(ctx, legacy.ID)
	require.NoError(t, err)
	require.Nil(t, retained.RevokedAt)
	require.Equal(t, legacy.KeyHash, retained.KeyHash)
	require.NoError(t, upgraded.Close())

	require.NoError(t, os.WriteFile(path, backup, 0o600))
	raw, err = sql.Open("sqlite", path)
	require.NoError(t, err)
	current, latest, err := MetadataMigrationVersions(ctx, raw, DialectSQLite, false)
	require.NoError(t, err)
	require.Equal(t, 18, current)
	require.Equal(t, 23, latest)
	var stillPresent int
	require.NoError(t, raw.QueryRowContext(ctx,
		`SELECT count(*) FROM task_read_grants WHERE id = ? AND revoked_at IS NULL`, legacy.ID).Scan(&stillPresent))
	require.Equal(t, 1, stillPresent)
	require.NoError(t, raw.Close())
}
