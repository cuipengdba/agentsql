package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestPostgres18MetadataMigrationE2E(t *testing.T) {
	ctx := dockerTestContext(t)
	const (
		databaseName = "agentsql"
		username     = "agentsql"
		password     = "agentsql-password"
	)

	container, err := postgrescontainer.Run(
		ctx,
		"postgres:18",
		postgrescontainer.WithDatabase(databaseName),
		postgrescontainer.WithUsername(username),
		postgrescontainer.WithPassword(password),
		postgrescontainer.BasicWaitStrategies(),
	)
	if err != nil {
		if container != nil {
			testcontainers.CleanupContainer(t, container)
		}
		require.NoError(t, err, "start postgres:18 (Docker daemon probe already succeeded)")
	}
	testcontainers.CleanupContainer(t, container)

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)
	dsn := fmt.Sprintf(
		"postgres://%s:%s@%s:%s/%s?sslmode=disable",
		username,
		password,
		host,
		port.Port(),
		databaseName,
	)

	opened, err := OpenMetadata(ctx, MetadataOptions{
		Driver:          DialectPostgres,
		PostgresDSN:     dsn,
		MaxOpenConns:    4,
		MaxIdleConns:    2,
		ConnMaxLifetime: time.Minute,
	}, []byte(testSecret))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, opened.Close()) })

	assertPostgresMigrationSchema(t, ctx, opened.db)

	require.NoError(t, Migrate(ctx, opened.db, DialectPostgres))
	migrationErrors := make(chan error, 2)
	for range 2 {
		go func() {
			migrationErrors <- Migrate(ctx, opened.db, DialectPostgres)
		}()
	}
	for range 2 {
		require.NoError(t, <-migrationErrors)
	}

	var migrationCount int
	require.NoError(t, opened.db.QueryRowContext(
		ctx,
		"SELECT count(*) FROM schema_migrations",
	).Scan(&migrationCount))
	require.Equal(t, 1, migrationCount)

	assertPostgresMigrationBehavior(t, ctx, opened.db)
}

func assertPostgresMigrationSchema(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()

	rows, err := database.QueryContext(ctx, `
SELECT table_name
FROM information_schema.tables
WHERE table_schema = 'public'
  AND table_type = 'BASE TABLE'
ORDER BY table_name`)
	require.NoError(t, err)
	require.Equal(t, []string{
		"agents",
		"approvals",
		"audit_logs",
		"datasources",
		"mask_rules",
		"policies",
		"rules",
		"schema_migrations",
	}, scanSingleStringColumn(t, rows))

	rows, err = database.QueryContext(ctx, `
SELECT indexname
FROM pg_indexes
WHERE schemaname = 'public'
  AND indexname IN (
    'idx_agents_keyhash',
    'idx_policies_agent_ds',
    'idx_audit_ts',
    'idx_audit_agent_ts',
    'idx_audit_decision',
    'idx_approvals_status'
  )
ORDER BY indexname`)
	require.NoError(t, err)
	require.Equal(t, []string{
		"idx_agents_keyhash",
		"idx_approvals_status",
		"idx_audit_agent_ts",
		"idx_audit_decision",
		"idx_audit_ts",
		"idx_policies_agent_ds",
	}, scanSingleStringColumn(t, rows))

	rows, err = database.QueryContext(ctx, `
SELECT tc.table_name, kcu.column_name, ccu.table_name, ccu.column_name
FROM information_schema.table_constraints AS tc
JOIN information_schema.key_column_usage AS kcu
  ON tc.constraint_catalog = kcu.constraint_catalog
 AND tc.constraint_schema = kcu.constraint_schema
 AND tc.constraint_name = kcu.constraint_name
JOIN information_schema.constraint_column_usage AS ccu
  ON tc.constraint_catalog = ccu.constraint_catalog
 AND tc.constraint_schema = ccu.constraint_schema
 AND tc.constraint_name = ccu.constraint_name
WHERE tc.constraint_schema = 'public'
  AND tc.constraint_type = 'FOREIGN KEY'`)
	require.NoError(t, err)
	var foreignKeys []string
	for rows.Next() {
		var tableName, columnName, referencedTable, referencedColumn string
		require.NoError(t, rows.Scan(&tableName, &columnName, &referencedTable, &referencedColumn))
		foreignKeys = append(
			foreignKeys,
			fmt.Sprintf("%s.%s->%s.%s", tableName, columnName, referencedTable, referencedColumn),
		)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	sort.Strings(foreignKeys)
	require.Equal(t, []string{
		"approvals.audit_id->audit_logs.id",
		"policies.agent_id->agents.id",
		"policies.datasource_id->datasources.id",
	}, foreignKeys)

	rows, err = database.QueryContext(ctx, `
SELECT table_name, column_name, data_type, is_nullable, column_default
FROM information_schema.columns
WHERE table_schema = 'public'
  AND table_name IN ('agents', 'datasources', 'policies', 'rules', 'mask_rules', 'audit_logs', 'approvals')
  AND data_type LIKE 'timestamp%'
ORDER BY table_name, ordinal_position`)
	require.NoError(t, err)
	timestampColumns := make(map[string]sql.NullString)
	for rows.Next() {
		var tableName, columnName, dataType, nullable string
		var defaultValue sql.NullString
		require.NoError(t, rows.Scan(&tableName, &columnName, &dataType, &nullable, &defaultValue))
		require.Equal(t, "timestamp with time zone", dataType)
		require.Equal(t, "YES", nullable)
		timestampColumns[tableName+"."+columnName] = defaultValue
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.Len(t, timestampColumns, 15)
	for name, defaultValue := range timestampColumns {
		if name == "agents.expires_at" || name == "approvals.decided_at" {
			require.False(t, defaultValue.Valid, name)
			continue
		}
		require.True(t, defaultValue.Valid, name)
		require.Contains(t, strings.ToLower(defaultValue.String), "now()", name)
	}
}

func assertPostgresMigrationBehavior(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()

	_, err := database.ExecContext(ctx, `
INSERT INTO datasources (
  id, name, db_type, host, port, database, username, password_enc
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		"ds_pg_migration",
		"Migration Database",
		"postgres",
		"127.0.0.1",
		5432,
		"application",
		"agentsql",
		"ciphertext",
	)
	require.NoError(t, err)

	_, err = database.ExecContext(ctx, `
INSERT INTO policies (
  id, agent_id, datasource_id, object_type, object_name, action
) VALUES ($1, $2, $3, $4, $5, $6)`,
		"policy_invalid_agent",
		"ag_missing",
		"ds_pg_migration",
		"table",
		"public.orders",
		"allow",
	)
	require.Error(t, err)

	_, err = database.ExecContext(
		ctx,
		"INSERT INTO approvals(id, audit_id) VALUES($1, $2)",
		"approval_invalid_audit",
		int64(9_999_999),
	)
	require.Error(t, err)

	var firstAuditID, secondAuditID int64
	var firstAuditTimestamp time.Time
	require.NoError(t, database.QueryRowContext(
		ctx,
		"INSERT INTO audit_logs(decision) VALUES($1) RETURNING id, ts",
		"allow",
	).Scan(&firstAuditID, &firstAuditTimestamp))
	require.Positive(t, firstAuditID)
	require.False(t, firstAuditTimestamp.IsZero())
	require.NoError(t, database.QueryRowContext(
		ctx,
		"INSERT INTO audit_logs(decision) VALUES($1) RETURNING id",
		"deny",
	).Scan(&secondAuditID))
	require.Greater(t, secondAuditID, firstAuditID)

	_, err = database.ExecContext(
		ctx,
		"INSERT INTO approvals(id, audit_id) VALUES($1, $2)",
		"approval_valid_audit",
		firstAuditID,
	)
	require.NoError(t, err)

	var enabled, builtin bool
	require.NoError(t, database.QueryRowContext(ctx, `
INSERT INTO rules(id, db_type, title, risk_level, pattern_type, definition)
VALUES($1, $2, $3, $4, $5, $6)
RETURNING enabled, builtin`,
		"RPG001",
		"postgres",
		"PostgreSQL defaults",
		3,
		"ast_match",
		"{}",
	).Scan(&enabled, &builtin))
	require.True(t, enabled)
	require.False(t, builtin)

	var createdAt, updatedAt time.Time
	require.NoError(t, database.QueryRowContext(ctx, `
INSERT INTO agents(id, name, api_key_hash)
VALUES($1, $2, $3)
RETURNING created_at, updated_at`,
		"ag_pg_migration",
		"Migration Agent",
		"hash",
	).Scan(&createdAt, &updatedAt))
	require.False(t, createdAt.IsZero())
	require.False(t, updatedAt.IsZero())
}

func scanSingleStringColumn(t *testing.T, rows *sql.Rows) []string {
	t.Helper()
	defer func() { require.NoError(t, rows.Close()) }()

	var values []string
	for rows.Next() {
		var value string
		require.NoError(t, rows.Scan(&value))
		values = append(values, value)
	}
	require.NoError(t, rows.Err())
	return values
}

func dockerTestContext(t *testing.T) context.Context {
	t.Helper()
	unavailable, err := probeDockerAvailable()
	if unavailable {
		t.Logf("docker daemon unavailable: %v", err)
		t.Skip("docker daemon unavailable")
	}
	require.NoError(t, err, "Docker probe failed after the client connected")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

// probeDockerAvailable only classifies client construction failures and an
// unmistakably unreachable daemon/socket as skippable. Once Ping succeeds,
// cleanup and every subsequent testcontainers error are test failures.
func probeDockerAvailable() (unavailable bool, err error) {
	clientConstructed := false
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("docker probe panic: %v", recovered)
			unavailable = !clientConstructed
		}
	}()
	probeContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dockerClient, err := testcontainers.NewDockerClient()
	if err != nil {
		return true, err
	}
	clientConstructed = true
	if _, err := dockerClient.Ping(probeContext); err != nil {
		_ = dockerClient.Close()
		return isDockerDaemonUnavailable(err), err
	}
	return false, dockerClient.Close()
}

func isDockerDaemonUnavailable(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"cannot connect to the docker daemon",
		"docker daemon is not running",
		"connection refused",
		"no such file or directory",
		"the system cannot find the file specified",
		"open //./pipe/docker_engine",
		"open \\\\.\\pipe\\docker_engine",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}
