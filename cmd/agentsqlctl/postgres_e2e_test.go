package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestPostgres18CommandLifecycleE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("postgres:18 agentsqlctl E2E is an integration test")
	}
	ctx := commandDockerTestContext(t)
	const (
		databaseName = "agentsql"
		username     = "agentsql"
		password     = "agentsqlctl-pg-password"
		yamlPassword = "yaml-password-must-not-appear"
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
	dsn := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", username, password, host, port.Port(), databaseName)
	yamlDSN := fmt.Sprintf("postgres://yaml:%s@example.invalid/agentsql?sslmode=disable", yamlPassword)
	t.Setenv("AGENTSQL_STORE_METADATA_DSN", dsn)
	configPath := writeControlConfig(t, fmt.Sprintf(`server:
  http_listen: "127.0.0.1:7780"
  console_enabled: true
  event_stream: true
  event_stream_max_connections: 100
store:
  metadata:
    driver: postgres
    dsn: %q
    max_open_conns: 4
    max_idle_conns: 2
    conn_max_lifetime: 1m
  audit:
    separate: false
defaults:
  statement_timeout_ms: 5000
  row_limit: 1000
  max_conns_per_datasource: 5
  qps_per_agent: 20
theme:
  default: dark
`, yamlDSN))

	var checkOutput, checkError strings.Builder
	require.Equal(t, 0, run([]string{"check-config", "--config", configPath}, &checkOutput, &checkError), checkError.String())
	require.Contains(t, checkOutput.String(), "config ok: metadata_driver=postgres audit_driver=postgres audit_separate=false")
	assertCommandOutputHasNoPostgresSecret(t, checkOutput.String()+checkError.String(), dsn, password, yamlDSN, yamlPassword)

	var migrateOutput, migrateError strings.Builder
	require.Equal(t, 0, run([]string{"migrate", "--config", configPath}, &migrateOutput, &migrateError), migrateError.String())
	require.Contains(t, migrateOutput.String(), "migration driver=postgres current=8 latest=8")
	assertCommandOutputHasNoPostgresSecret(t, migrateOutput.String()+migrateError.String(), dsn, password, yamlDSN, yamlPassword)

	database, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	rows, err := database.QueryContext(ctx, `
SELECT table_name
FROM information_schema.tables
WHERE table_schema = 'public' AND table_type = 'BASE TABLE'
ORDER BY table_name`)
	require.NoError(t, err)
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var table string
		require.NoError(t, rows.Scan(&table))
		tables = append(tables, table)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []string{
		"agents", "approvals", "audit_logs", "chain_state", "chain_verification", "datasources", "management_audit_outbox",
		"mask_rules", "notification_channels", "notification_settings", "policies",
		"redaction_key_versions", "rules", "schema_migrations",
	}, tables)

	var healthOutput, healthError strings.Builder
	require.Equal(t, 0, run([]string{"health", "--config", configPath}, &healthOutput, &healthError), healthError.String())
	require.Contains(t, healthOutput.String(), "health ok: metadata_driver=postgres audit_driver=postgres audit_separate=false")
	assertCommandOutputHasNoPostgresSecret(t, healthOutput.String()+healthError.String(), dsn, password, yamlDSN, yamlPassword)

	var conflictError strings.Builder
	require.Equal(t, 1, run([]string{"health", "--url", "http://127.0.0.1:1/healthz", "--config", configPath}, io.Discard, &conflictError))
	require.Contains(t, conflictError.String(), "health --url and --config are mutually exclusive")
	assertCommandOutputHasNoPostgresSecret(t, conflictError.String(), dsn, password, yamlDSN, yamlPassword)
}

func TestPostgres18SeparatedCommandLifecycleE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("dual postgres:18 agentsqlctl E2E is an integration test")
	}
	ctx := commandDockerTestContext(t)
	metadataDSN := startCommandPostgres18Container(t, ctx, "agentsql_meta", "metadata-command-password")
	auditDSN := startCommandPostgres18Container(t, ctx, "agentsql_audit", "audit-command-password")
	const (
		yamlMetadataPassword = "yaml-metadata-password-must-not-appear"
		yamlAuditPassword    = "yaml-audit-password-must-not-appear"
	)
	yamlMetadataDSN := "postgres://yaml:" + yamlMetadataPassword + "@example.invalid/meta"
	yamlAuditDSN := "postgres://yaml:" + yamlAuditPassword + "@example.invalid/audit"
	t.Setenv("AGENTSQL_STORE_METADATA_DSN", metadataDSN)
	t.Setenv("AGENTSQL_STORE_AUDIT_DSN", auditDSN)
	configPath := writeControlConfig(t, fmt.Sprintf(`server:
  http_listen: "127.0.0.1:7780"
  console_enabled: true
  event_stream: true
  event_stream_max_connections: 100
store:
  auto_migrate: false
  metadata:
    driver: postgres
    dsn: %q
    max_open_conns: 4
    max_idle_conns: 2
    conn_max_lifetime: 1m
  audit:
    separate: true
    driver: postgres
    dsn: %q
    max_open_conns: 3
    max_idle_conns: 1
defaults:
  statement_timeout_ms: 5000
  row_limit: 1000
  max_conns_per_datasource: 5
  qps_per_agent: 20
theme:
  default: dark
`, yamlMetadataDSN, yamlAuditDSN))

	var output, stderr strings.Builder
	require.Equal(t, 0, run([]string{"check-config", "--config", configPath}, &output, &stderr), stderr.String())
	require.Contains(t, output.String(), "config ok: metadata_driver=postgres audit_driver=postgres audit_separate=true")
	assertCommandOutputHasNoPostgresSecret(t, output.String()+stderr.String(), metadataDSN, auditDSN, yamlMetadataDSN, yamlAuditDSN, "metadata-command-password", "audit-command-password", yamlMetadataPassword, yamlAuditPassword)

	output.Reset()
	stderr.Reset()
	require.Equal(t, 0, run([]string{"migrate", "--config", configPath}, &output, &stderr), stderr.String())
	require.Contains(t, output.String(), "metadata migration driver=postgres current=7 latest=7")
	require.Contains(t, output.String(), "audit migration driver=postgres current=5 latest=5")
	assertCommandOutputHasNoPostgresSecret(t, output.String()+stderr.String(), metadataDSN, auditDSN, yamlMetadataDSN, yamlAuditDSN, "metadata-command-password", "audit-command-password", yamlMetadataPassword, yamlAuditPassword)

	metadataDB, err := sql.Open("pgx", metadataDSN)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, metadataDB.Close()) })
	auditDB, err := sql.Open("pgx", auditDSN)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, auditDB.Close()) })
	require.Equal(t, []string{
		"agents", "approvals", "chain_state", "chain_verification", "datasources", "management_audit_outbox", "mask_rules",
		"notification_channels", "notification_settings", "policies",
		"redaction_key_versions", "rules", "schema_migrations",
	}, commandPostgresTableNames(t, ctx, metadataDB))
	require.Equal(t, []string{"audit_logs", "chain_state", "chain_verification", "schema_migrations"}, commandPostgresTableNames(t, ctx, auditDB))
	require.Equal(t, []string{"idx_audit_agent_ts", "idx_audit_decision", "idx_audit_ts", "ux_audit_logs_event_uuid"}, commandPostgresIndexNames(t, ctx, auditDB))
	var approvalForeignKeys int
	require.NoError(t, metadataDB.QueryRowContext(ctx, `
SELECT count(*)
FROM information_schema.table_constraints
WHERE constraint_schema='public' AND table_name='approvals' AND constraint_type='FOREIGN KEY'`).Scan(&approvalForeignKeys))
	require.Zero(t, approvalForeignKeys)

	output.Reset()
	stderr.Reset()
	require.Equal(t, 0, run([]string{"health", "--config", configPath}, &output, &stderr), stderr.String())
	require.Contains(t, output.String(), "health ok: metadata_driver=postgres audit_driver=postgres audit_separate=true")
	assertCommandOutputHasNoPostgresSecret(t, output.String()+stderr.String(), metadataDSN, auditDSN, yamlMetadataDSN, yamlAuditDSN, "metadata-command-password", "audit-command-password", yamlMetadataPassword, yamlAuditPassword)
}

func startCommandPostgres18Container(t *testing.T, ctx context.Context, databaseName, password string) string {
	t.Helper()
	const username = "agentsql"
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
	return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", username, password, host, port.Port(), databaseName)
}

func commandPostgresTableNames(t *testing.T, ctx context.Context, database *sql.DB) []string {
	t.Helper()
	rows, err := database.QueryContext(ctx, `
SELECT table_name
FROM information_schema.tables
WHERE table_schema='public' AND table_type='BASE TABLE'
ORDER BY table_name`)
	require.NoError(t, err)
	return commandScanStrings(t, rows)
}

func commandPostgresIndexNames(t *testing.T, ctx context.Context, database *sql.DB) []string {
	t.Helper()
	rows, err := database.QueryContext(ctx, `
SELECT indexname
FROM pg_indexes
WHERE schemaname='public' AND (indexname LIKE 'idx_%' OR indexname IN ('ux_mask_rules_scope_column','ux_redaction_key_versions_active','ux_audit_logs_event_uuid'))
ORDER BY indexname`)
	require.NoError(t, err)
	return commandScanStrings(t, rows)
}

func commandScanStrings(t *testing.T, rows *sql.Rows) []string {
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

func assertCommandOutputHasNoPostgresSecret(t *testing.T, output string, sensitive ...string) {
	t.Helper()
	for _, value := range sensitive {
		require.NotContains(t, output, value)
	}
}

func commandDockerTestContext(t *testing.T) context.Context {
	t.Helper()
	unavailable, err := probeCommandDockerAvailable()
	if unavailable {
		t.Logf("docker daemon unavailable: %v", err)
		t.Skip("docker daemon unavailable")
	}
	require.NoError(t, err, "Docker probe failed after the client connected")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

func probeCommandDockerAvailable() (unavailable bool, err error) {
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
		return commandDockerDaemonUnavailable(err), err
	}
	return false, dockerClient.Close()
}

func commandDockerDaemonUnavailable(err error) bool {
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
