package store

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestPostgres18MetadataMigrationE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("postgres:18 metadata migration E2E is an integration test")
	}
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
		AutoMigrate:     true,
	}, []byte(testSecret))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, opened.Close()) })

	assertPostgresMigrationSchema(t, ctx, opened.metaDB)
	assertPostgresAuditErrorCodeColumn(t, ctx, opened.metaDB)

	require.NoError(t, Migrate(ctx, opened.metaDB, DialectPostgres))
	migrationErrors := make(chan error, 2)
	for range 2 {
		go func() {
			migrationErrors <- Migrate(ctx, opened.metaDB, DialectPostgres)
		}()
	}
	for range 2 {
		require.NoError(t, <-migrationErrors)
	}

	var migrationCount int
	require.NoError(t, opened.metaDB.QueryRowContext(
		ctx,
		"SELECT count(*) FROM schema_migrations",
	).Scan(&migrationCount))
	require.Equal(t, 16, migrationCount)
	current, latest, err := MetadataMigrationVersions(ctx, opened.metaDB, DialectPostgres, false)
	require.NoError(t, err)
	require.Equal(t, 16, current)
	require.Equal(t, 16, latest)
	require.NoError(t, opened.Notifications().Replace(ctx, completeNotificationConfig()))
	storedNotifications, err := opened.Notifications().Get(ctx)
	require.NoError(t, err)
	require.Equal(t, completeNotificationConfig(), storedNotifications)

	assertPostgresMigrationBehavior(t, ctx, opened.metaDB)
}

func TestPostgres18SeparatedMetadataAndAuditMigrationE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("dual postgres:18 separated-store E2E is an integration test")
	}
	ctx := dockerTestContext(t)
	metadataDSN := startPostgres18StoreContainer(t, ctx, "agentsql_meta", "metadata-password")
	auditDSN := startPostgres18StoreContainer(t, ctx, "agentsql_audit", "audit-password")
	options := MetadataOptions{
		Driver:          DialectPostgres,
		PostgresDSN:     metadataDSN,
		MaxOpenConns:    6,
		MaxIdleConns:    3,
		ConnMaxLifetime: time.Minute,
		AutoMigrate:     true,
		Audit: AuditOptions{
			Separate:     true,
			Driver:       DialectPostgres,
			PostgresDSN:  auditDSN,
			MaxOpenConns: 4,
			MaxIdleConns: 2,
			AutoMigrate:  true,
		},
	}
	opened, err := OpenMetadata(ctx, options, []byte(testSecret))
	require.NoError(t, err)
	require.NoError(t, MigrateMetadata(ctx, opened.metaDB, DialectPostgres, true))
	require.NoError(t, MigrateMetadata(ctx, opened.metaDB, DialectPostgres, true))
	require.NoError(t, MigrateAudit(ctx, opened.auditDB, DialectPostgres))
	require.NoError(t, MigrateAudit(ctx, opened.auditDB, DialectPostgres))
	require.NotSame(t, opened.metaDB, opened.auditDB)
	require.True(t, opened.auditSeparate)
	require.Equal(t, 6, opened.metaDB.Stats().MaxOpenConnections)
	require.Equal(t, 4, opened.auditDB.Stats().MaxOpenConnections)
	require.Same(t, opened.metaDB, opened.Agents().db)
	require.Same(t, opened.auditDB, opened.AuditLogs().db)

	require.Equal(t, []string{
		"admin_access_revocations", "admin_refresh_families", "admin_refresh_tokens", "agents", "approvals", "auth_identities", "auth_login_challenges", "b5_dml_grants", "b5_result_receipts", "b5_sessions", "b5_transactions", "b5_tx_events", "chain_state", "chain_verification", "control_plane_compat", "datasources", "management_audit_outbox", "mask_rules", "notification_channels",
		"mcp_stream_event_cursors", "mcp_stream_events", "notification_settings", "oidc_auth_requests", "permissions", "policies", "policy_column_permission_staging", "policy_column_permissions", "redaction_key_versions", "relation_policy_bindings", "role_inheritance", "role_permissions", "roles", "rules", "runtime_instances", "schema_migrations", "tenants", "user_mfa", "user_mfa_recovery_codes", "user_roles", "users",
	}, postgresTableNames(t, ctx, opened.metaDB))
	require.Equal(t, []string{"audit_logs", "chain_state", "chain_verification", "schema_migrations"}, postgresTableNames(t, ctx, opened.auditDB))
	metadataCurrent, metadataLatest, err := MetadataMigrationVersions(ctx, opened.metaDB, DialectPostgres, true)
	require.NoError(t, err)
	require.Equal(t, 15, metadataCurrent)
	require.Equal(t, 15, metadataLatest)
	assertPostgresMaskRuleRangeColumns(t, ctx, opened.metaDB)
	auditCurrent, auditLatest, err := AuditMigrationVersions(ctx, opened.auditDB, DialectPostgres)
	require.NoError(t, err)
	require.Equal(t, 5, auditCurrent)
	require.Equal(t, 6, auditLatest)
	assertPostgresAuditErrorCodeColumn(t, ctx, opened.auditDB)
	require.NoError(t, opened.Notifications().Replace(ctx, completeNotificationConfig()))
	storedNotifications, err := opened.Notifications().Get(ctx)
	require.NoError(t, err)
	require.Equal(t, completeNotificationConfig(), storedNotifications)
	require.Equal(t, []string{
		"idx_admin_access_revocations_expiry", "idx_admin_refresh_families_expiry", "idx_admin_refresh_tokens_family",
		"idx_agents_keyhash", "idx_approvals_status", "idx_auth_challenges_expiry", "idx_auth_identities_user", "idx_b5_dml_grants_identity", "idx_b5_dml_grants_lookup", "idx_b5_result_receipts_delivery", "idx_b5_result_receipts_event", "idx_b5_result_receipts_reconcile", "idx_b5_sessions_owner", "idx_b5_sessions_ttl", "idx_b5_transactions_datasource", "idx_b5_transactions_deadline", "idx_b5_transactions_one_live_session", "idx_b5_tx_events_audit", "idx_policies_agent_ds",
		"idx_mcp_stream_event_cursors_updated_at", "idx_mcp_stream_events_created_at", "idx_mcp_stream_events_session",
		"idx_oidc_requests_expiry", "idx_policy_column_permissions_policy", "idx_relation_policy_bindings_datasource", "idx_role_permissions_role", "idx_roles_tenant", "idx_runtime_instances_lease", "idx_user_roles_user", "idx_users_tenant",
		"ux_mask_rules_scope_column", "ux_redaction_key_versions_active",
	}, postgresNamedIndexes(t, ctx, opened.metaDB))
	require.Equal(t, []string{
		"idx_audit_agent_ts", "idx_audit_decision", "idx_audit_ts",
		"ux_audit_logs_event_uuid",
	}, postgresNamedIndexes(t, ctx, opened.auditDB))
	require.Equal(t, []string{
		"admin_refresh_tokens.family_id->admin_refresh_families.family_id",
		"auth_identities.tenant_id->users.id", "auth_identities.tenant_id->users.tenant_id",
		"auth_identities.user_id->users.id", "auth_identities.user_id->users.tenant_id",
		"auth_login_challenges.tenant_id->users.id", "auth_login_challenges.tenant_id->users.tenant_id",
		"auth_login_challenges.user_id->users.id", "auth_login_challenges.user_id->users.tenant_id",
		"b5_dml_grants.datasource_id->datasources.id", "b5_dml_grants.policy_id->policies.id",
		"b5_result_receipts.session_id->b5_sessions.session_id", "b5_sessions.agent_id->agents.id",
		"b5_transactions.datasource_id->datasources.id", "b5_transactions.session_id->b5_sessions.session_id",
		"b5_tx_events.transaction_id->b5_transactions.transaction_id",
		"policies.agent_id->agents.id", "policies.datasource_id->datasources.id",
		"policy_column_permission_staging.policy_id->policies.id", "policy_column_permissions.policy_id->policies.id",
		"relation_policy_bindings.datasource_id->datasources.id", "relation_policy_bindings.policy_id->policies.id",
		"role_inheritance.parent_role_id->roles.id", "role_inheritance.parent_role_id->roles.tenant_id",
		"role_inheritance.role_id->roles.id", "role_inheritance.role_id->roles.tenant_id",
		"role_inheritance.tenant_id->roles.id", "role_inheritance.tenant_id->roles.id",
		"role_inheritance.tenant_id->roles.tenant_id", "role_inheritance.tenant_id->roles.tenant_id",
		"role_permissions.permission_code->permissions.code",
		"role_permissions.role_id->roles.id", "role_permissions.role_id->roles.tenant_id",
		"role_permissions.tenant_id->roles.id", "role_permissions.tenant_id->roles.tenant_id",
		"roles.tenant_id->tenants.id",
		"user_mfa.tenant_id->users.id", "user_mfa.tenant_id->users.tenant_id",
		"user_mfa.user_id->users.id", "user_mfa.user_id->users.tenant_id",
		"user_mfa_recovery_codes.tenant_id->user_mfa.tenant_id", "user_mfa_recovery_codes.tenant_id->user_mfa.user_id",
		"user_mfa_recovery_codes.user_id->user_mfa.tenant_id", "user_mfa_recovery_codes.user_id->user_mfa.user_id",
		"user_roles.role_id->roles.id", "user_roles.role_id->roles.tenant_id",
		"user_roles.tenant_id->roles.id", "user_roles.tenant_id->roles.tenant_id",
		"user_roles.tenant_id->users.id", "user_roles.tenant_id->users.tenant_id",
		"user_roles.user_id->users.id", "user_roles.user_id->users.tenant_id",
		"users.tenant_id->tenants.id",
	}, postgresForeignKeys(t, ctx, opened.metaDB))
	require.NoError(t, opened.Ping(ctx))
	require.NoError(t, opened.Close())

	auditDatabase, err := sql.Open("pgx", auditDSN)
	require.NoError(t, err)
	_, err = auditDatabase.ExecContext(ctx, "UPDATE schema_migrations SET version=6 WHERE version=5")
	require.NoError(t, err)
	require.NoError(t, auditDatabase.Close())
	options.AutoMigrate = false
	options.Audit.AutoMigrate = false
	_, err = OpenMetadata(ctx, options, []byte(testSecret))
	require.ErrorContains(t, err, "current=6 latest=5")
}

func TestPostgres18IndependentMetadataAndAuditOpenE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("dual postgres:18 independent-store E2E is an integration test")
	}
	ctx := dockerTestContext(t)
	metadataDSN := startPostgres18StoreContainer(t, ctx, "agentsql_meta_only", "metadata-only-password")
	auditDSN := startPostgres18StoreContainer(t, ctx, "agentsql_audit_only", "audit-only-password")
	metadata, err := OpenMetadataOnly(ctx, MetadataOptions{
		Driver: DialectPostgres, PostgresDSN: metadataDSN, AutoMigrate: true,
		Audit: AuditOptions{Separate: true, Driver: DialectPostgres, PostgresDSN: "postgres://127.0.0.1:1/unreachable"},
	}, []byte(testSecret))
	require.NoError(t, err)
	require.NoError(t, metadata.RedactionKeys().RegisterStandby(ctx, "2", testCommitment2, "independent", "r1", nil))
	current, latest, err := metadata.MigrationVersions(ctx)
	require.NoError(t, err)
	require.Equal(t, latest, current)
	require.NoError(t, metadata.Close())

	audit, err := OpenAuditOnly(ctx, AuditOptions{Driver: DialectPostgres, PostgresDSN: auditDSN, AutoMigrate: true})
	require.NoError(t, err)
	eventUUID := "audit-only-event"
	_, err = audit.AuditLogs().Insert(ctx, model.AuditLog{Decision: "allow", EventUUID: &eventUUID})
	require.NoError(t, err)
	current, latest, err = audit.MigrationVersions(ctx)
	require.NoError(t, err)
	require.Equal(t, latest, current)
	require.NoError(t, audit.Close())
}

func TestPostgres18SeparatedDashboardAndAuditReadsE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("dual postgres:18 dashboard and audit routing E2E is an integration test")
	}
	ctx := dockerTestContext(t)
	metadataDSN := startPostgres18StoreContainer(t, ctx, "agentsql_dashboard_meta", "metadata-password")
	auditDSN := startPostgres18StoreContainer(t, ctx, "agentsql_dashboard_audit", "audit-password")
	opened, err := OpenMetadata(ctx, MetadataOptions{
		Driver:          DialectPostgres,
		PostgresDSN:     metadataDSN,
		MaxOpenConns:    6,
		MaxIdleConns:    3,
		ConnMaxLifetime: time.Minute,
		AutoMigrate:     true,
		Audit: AuditOptions{
			Separate: true, Driver: DialectPostgres,
			PostgresDSN:  auditDSN + "&TimeZone=Asia%2FShanghai",
			MaxOpenConns: 4, MaxIdleConns: 2, AutoMigrate: true,
		},
	}, []byte(testSecret))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, opened.Close()) })

	_, hash, err := GenerateAPIKey()
	require.NoError(t, err)
	_, err = opened.Agents().Create(ctx, model.Agent{
		ID: "agent-known", Name: "Known Agent", Status: "active", APIKeyHash: hash, Level: "readonly",
	})
	require.NoError(t, err)
	_, err = opened.Datasources().Create(ctx, model.Datasource{
		ID: "dashboard-ds", Name: "Dashboard DB", DBType: "postgres", Host: "127.0.0.1",
		Port: 5432, Database: "app", Username: "gateway", ConnLimit: 5,
		StmtTimeoutMS: 5000, RowLimit: 1000,
	}, "password")
	require.NoError(t, err)
	_, err = opened.Approvals().Create(ctx, model.Approval{ID: "dashboard-pending", Status: "pending"})
	require.NoError(t, err)

	referenceNow := time.Date(2027, time.January, 2, 20, 0, 0, 0, time.UTC)
	entries := []struct {
		timestamp time.Time
		agentID   string
		decision  string
		ruleHits  string
		estRows   int64
		sqlRaw    string
	}{
		{referenceNow.AddDate(0, 0, -1).Truncate(24 * time.Hour).Add(time.Hour), "agent-known", "allow", `[]`, 0, "SELECT allowed"},
		{referenceNow.Truncate(24 * time.Hour).Add(time.Hour), "agent-known", "deny", `[{"RuleID":"R-HIGH","Decision":"deny"}]`, 10, "SELECT blocked known"},
		{referenceNow.Truncate(24 * time.Hour).Add(2 * time.Hour), "agent-removed", "deny", `[{"RuleID":"R-HIGH","Decision":"deny"}]`, 20, "SELECT blocked removed"},
		{referenceNow.Truncate(24 * time.Hour).Add(3 * time.Hour), "agent-known", "approve", `[]`, 0, "UPDATE awaiting approval"},
	}
	insertedIDs := make([]int64, 0, len(entries))
	for _, entry := range entries {
		agentID, ruleHits, estimatedRows, sqlRaw := entry.agentID, entry.ruleHits, entry.estRows, entry.sqlRaw
		inserted, insertErr := opened.AuditLogs().Insert(ctx, model.AuditLog{
			AgentID: &agentID, Decision: entry.decision, RuleHits: &ruleHits,
			EstRows: &estimatedRows, SQLRaw: &sqlRaw,
		})
		require.NoError(t, insertErr)
		insertedIDs = append(insertedIDs, inserted.ID)
		_, updateErr := opened.auditDB.ExecContext(
			ctx, "UPDATE audit_logs SET ts = $1 WHERE id = $2", entry.timestamp, inserted.ID,
		)
		require.NoError(t, updateErr)
	}

	var metadataAuditTable sql.NullString
	require.NoError(t, opened.metaDB.QueryRowContext(ctx, "SELECT to_regclass('public.audit_logs')").Scan(&metadataAuditTable))
	require.False(t, metadataAuditTable.Valid)
	var auditCount int64
	require.NoError(t, opened.auditDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_logs").Scan(&auditCount))
	require.Equal(t, int64(len(entries)), auditCount)

	dashboard := opened.Dashboard()
	dashboard.now = func() time.Time { return referenceNow }
	summary, err := dashboard.Summary(ctx, 2)
	require.NoError(t, err)
	require.Equal(t, DashboardKPI{
		TotalRequests: 4, Blocked: 2, PendingApprovals: 1, ActiveAgents: 1, DatasourcesTotal: 1,
	}, summary.KPI)
	require.Equal(t, []TrendDay{
		{Date: "2027-01-01", Total: 1, Allow: 1},
		{Date: "2027-01-02", Total: 3, Deny: 2, Approve: 1},
	}, summary.Trend14D)
	require.Equal(t, []DecisionCount{
		{Decision: "allow", Count: 1},
		{Decision: "deny", Count: 2},
		{Decision: "approve", Count: 1},
		{Decision: "warn", Count: 0},
	}, summary.DecisionDistribution)
	require.Equal(t, []RiskTopEntry{{RuleID: "R-HIGH", Count: 2}}, summary.RiskTop)
	require.Equal(t, []AgentRankingItem{
		{AgentID: "agent-known", Name: "Known Agent", BlockedCount: 1},
		{AgentID: "agent-removed", Name: "", BlockedCount: 1},
	}, summary.AgentRanking)
	require.Equal(t, BattleReport{BlockedCount: 2, EstRowsSaved: 30}, summary.BattleReport)

	denies := model.AuditFilter{Decisions: []string{"deny"}, Keyword: "blocked"}
	firstPage, err := opened.AuditLogs().FilteredPage(ctx, denies, 1, 1)
	require.NoError(t, err)
	secondPage, err := opened.AuditLogs().FilteredPage(ctx, denies, 2, 1)
	require.NoError(t, err)
	require.Equal(t, int64(2), firstPage.Total)
	require.Equal(t, int64(2), secondPage.Total)
	require.Len(t, firstPage.List, 1)
	require.Len(t, secondPage.List, 1)
	require.NotEqual(t, firstPage.List[0].ID, secondPage.List[0].ID)

	// The audit export service consumes this same Reader contract page by page.
	var exportReader interface {
		FilteredPage(context.Context, model.AuditFilter, int, int) (AuditPage, error)
	} = opened.AuditLogs()
	exportedIDs := make([]int64, 0, len(entries))
	for page := 1; ; page++ {
		listed, pageErr := exportReader.FilteredPage(ctx, model.AuditFilter{}, page, 2)
		require.NoError(t, pageErr)
		for _, auditLog := range listed.List {
			exportedIDs = append(exportedIDs, auditLog.ID)
		}
		if len(listed.List) < 2 || int64(len(exportedIDs)) >= listed.Total {
			break
		}
	}
	require.ElementsMatch(t, insertedIDs, exportedIDs)

	require.NoError(t, opened.Ping(ctx))
	require.NoError(t, opened.auditDB.Close())
	require.ErrorContains(t, opened.Ping(ctx), "ping audit store")
}

func startPostgres18StoreContainer(t *testing.T, ctx context.Context, databaseName, password string) string {
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
	return fmt.Sprintf(
		"postgres://%s:%s@%s:%s/%s?sslmode=disable",
		username, password, host, port.Port(), databaseName,
	)
}

func postgresTableNames(t *testing.T, ctx context.Context, database *sql.DB) []string {
	t.Helper()
	rows, err := database.QueryContext(ctx, `
SELECT table_name
FROM information_schema.tables
WHERE table_schema = 'public' AND table_type = 'BASE TABLE'
ORDER BY table_name`)
	require.NoError(t, err)
	return scanSingleStringColumn(t, rows)
}

func postgresNamedIndexes(t *testing.T, ctx context.Context, database *sql.DB) []string {
	t.Helper()
	rows, err := database.QueryContext(ctx, `
SELECT indexname
FROM pg_indexes
WHERE schemaname = 'public'
  AND (indexname LIKE 'idx_%' OR indexname IN (
    'ux_mask_rules_scope_column',
    'ux_redaction_key_versions_active',
    'ux_audit_logs_event_uuid'
  ))
ORDER BY indexname`)
	require.NoError(t, err)
	return scanSingleStringColumn(t, rows)
}

func postgresForeignKeys(t *testing.T, ctx context.Context, database *sql.DB) []string {
	t.Helper()
	rows, err := database.QueryContext(ctx, `
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
WHERE tc.constraint_schema = 'public' AND tc.constraint_type = 'FOREIGN KEY'`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var foreignKeys []string
	for rows.Next() {
		var tableName, columnName, referencedTable, referencedColumn string
		require.NoError(t, rows.Scan(&tableName, &columnName, &referencedTable, &referencedColumn))
		foreignKeys = append(foreignKeys, fmt.Sprintf(
			"%s.%s->%s.%s", tableName, columnName, referencedTable, referencedColumn,
		))
	}
	require.NoError(t, rows.Err())
	sort.Strings(foreignKeys)
	return foreignKeys
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
		"admin_access_revocations",
		"admin_refresh_families",
		"admin_refresh_tokens",
		"agents",
		"approvals",
		"audit_logs",
		"auth_identities",
		"auth_login_challenges",
		"b5_dml_grants",
		"b5_result_receipts",
		"b5_sessions",
		"b5_transactions",
		"b5_tx_events",
		"chain_state",
		"chain_verification",
		"control_plane_compat",
		"datasources",
		"management_audit_outbox",
		"mask_rules",
		"mcp_stream_event_cursors",
		"mcp_stream_events",
		"notification_channels",
		"notification_settings",
		"oidc_auth_requests",
		"permissions",
		"policies",
		"policy_column_permission_staging",
		"policy_column_permissions",
		"redaction_key_versions",
		"relation_policy_bindings",
		"role_inheritance",
		"role_permissions",
		"roles",
		"rules",
		"runtime_instances",
		"schema_migrations",
		"tenants",
		"user_mfa",
		"user_mfa_recovery_codes",
		"user_roles",
		"users",
	}, scanSingleStringColumn(t, rows))
	assertPostgresMaskRuleRangeColumns(t, ctx, database)

	rows, err = database.QueryContext(ctx, `
SELECT indexname
FROM pg_indexes
WHERE schemaname = 'public'
  AND indexname IN (
	'idx_admin_access_revocations_expiry',
	'idx_admin_refresh_families_expiry',
	'idx_admin_refresh_tokens_family',
	'idx_auth_challenges_expiry',
	'idx_auth_identities_user',
    'idx_agents_keyhash',
    'idx_policies_agent_ds',
    'idx_audit_ts',
    'idx_audit_agent_ts',
    'idx_audit_decision',
    'idx_approvals_status',
	'idx_policy_column_permissions_policy',
	'idx_relation_policy_bindings_datasource',
	'idx_runtime_instances_lease',
	'idx_role_permissions_role',
	'idx_roles_tenant',
	'idx_user_roles_user',
	'idx_users_tenant',
	'idx_b5_dml_grants_identity',
	'idx_b5_dml_grants_lookup',
	'idx_b5_result_receipts_delivery',
	'idx_b5_result_receipts_event',
	'idx_b5_result_receipts_reconcile',
	'idx_b5_sessions_owner',
	'idx_b5_sessions_ttl',
	'idx_b5_transactions_datasource',
	'idx_b5_transactions_deadline',
	'idx_b5_transactions_one_live_session',
	'idx_b5_tx_events_audit',
	'idx_mcp_stream_event_cursors_updated_at',
	'idx_mcp_stream_events_created_at',
	'idx_mcp_stream_events_session',
	'idx_oidc_requests_expiry',
    'ux_mask_rules_scope_column',
    'ux_redaction_key_versions_active',
    'ux_audit_logs_event_uuid'
  )
ORDER BY indexname`)
	require.NoError(t, err)
	require.Equal(t, []string{
		"idx_admin_access_revocations_expiry",
		"idx_admin_refresh_families_expiry",
		"idx_admin_refresh_tokens_family",
		"idx_agents_keyhash",
		"idx_approvals_status",
		"idx_audit_agent_ts",
		"idx_audit_decision",
		"idx_audit_ts",
		"idx_auth_challenges_expiry",
		"idx_auth_identities_user",
		"idx_b5_dml_grants_identity",
		"idx_b5_dml_grants_lookup",
		"idx_b5_result_receipts_delivery",
		"idx_b5_result_receipts_event",
		"idx_b5_result_receipts_reconcile",
		"idx_b5_sessions_owner",
		"idx_b5_sessions_ttl",
		"idx_b5_transactions_datasource",
		"idx_b5_transactions_deadline",
		"idx_b5_transactions_one_live_session",
		"idx_b5_tx_events_audit",
		"idx_mcp_stream_event_cursors_updated_at",
		"idx_mcp_stream_events_created_at",
		"idx_mcp_stream_events_session",
		"idx_oidc_requests_expiry",
		"idx_policies_agent_ds",
		"idx_policy_column_permissions_policy",
		"idx_relation_policy_bindings_datasource",
		"idx_role_permissions_role",
		"idx_roles_tenant",
		"idx_runtime_instances_lease",
		"idx_user_roles_user",
		"idx_users_tenant",
		"ux_audit_logs_event_uuid",
		"ux_mask_rules_scope_column",
		"ux_redaction_key_versions_active",
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
		"admin_refresh_tokens.family_id->admin_refresh_families.family_id",
		"approvals.audit_id->audit_logs.id",
		"auth_identities.tenant_id->users.id",
		"auth_identities.tenant_id->users.tenant_id",
		"auth_identities.user_id->users.id",
		"auth_identities.user_id->users.tenant_id",
		"auth_login_challenges.tenant_id->users.id",
		"auth_login_challenges.tenant_id->users.tenant_id",
		"auth_login_challenges.user_id->users.id",
		"auth_login_challenges.user_id->users.tenant_id",
		"b5_dml_grants.datasource_id->datasources.id",
		"b5_dml_grants.policy_id->policies.id",
		"b5_result_receipts.session_id->b5_sessions.session_id",
		"b5_sessions.agent_id->agents.id",
		"b5_transactions.datasource_id->datasources.id",
		"b5_transactions.session_id->b5_sessions.session_id",
		"b5_tx_events.transaction_id->b5_transactions.transaction_id",
		"policies.agent_id->agents.id",
		"policies.datasource_id->datasources.id",
		"policy_column_permission_staging.policy_id->policies.id",
		"policy_column_permissions.policy_id->policies.id",
		"relation_policy_bindings.datasource_id->datasources.id",
		"relation_policy_bindings.policy_id->policies.id",
		"role_inheritance.parent_role_id->roles.id",
		"role_inheritance.parent_role_id->roles.tenant_id",
		"role_inheritance.role_id->roles.id",
		"role_inheritance.role_id->roles.tenant_id",
		"role_inheritance.tenant_id->roles.id",
		"role_inheritance.tenant_id->roles.id",
		"role_inheritance.tenant_id->roles.tenant_id",
		"role_inheritance.tenant_id->roles.tenant_id",
		"role_permissions.permission_code->permissions.code",
		"role_permissions.role_id->roles.id",
		"role_permissions.role_id->roles.tenant_id",
		"role_permissions.tenant_id->roles.id",
		"role_permissions.tenant_id->roles.tenant_id",
		"roles.tenant_id->tenants.id",
		"user_mfa.tenant_id->users.id",
		"user_mfa.tenant_id->users.tenant_id",
		"user_mfa.user_id->users.id",
		"user_mfa.user_id->users.tenant_id",
		"user_mfa_recovery_codes.tenant_id->user_mfa.tenant_id",
		"user_mfa_recovery_codes.tenant_id->user_mfa.user_id",
		"user_mfa_recovery_codes.user_id->user_mfa.tenant_id",
		"user_mfa_recovery_codes.user_id->user_mfa.user_id",
		"user_roles.role_id->roles.id",
		"user_roles.role_id->roles.tenant_id",
		"user_roles.tenant_id->roles.id",
		"user_roles.tenant_id->roles.tenant_id",
		"user_roles.tenant_id->users.id",
		"user_roles.tenant_id->users.tenant_id",
		"user_roles.user_id->users.id",
		"user_roles.user_id->users.tenant_id",
		"users.tenant_id->tenants.id",
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

func assertPostgresMaskRuleRangeColumns(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	rows, err := database.QueryContext(ctx, `
SELECT column_name
FROM information_schema.columns
WHERE table_schema = 'public' AND table_name = 'mask_rules'
ORDER BY ordinal_position`)
	require.NoError(t, err)
	require.Equal(t, []string{
		"id", "datasource_id", "table_name", "column_name", "sensitive_type", "algo",
		"created_at", "updated_at", "enabled", "range_bucket_width", "range_bucket_offset",
		"range_granularity", "schema_name",
	}, scanSingleStringColumn(t, rows))

	rows, err = database.QueryContext(ctx, `
SELECT column_name, data_type, is_nullable, column_default
FROM information_schema.columns
WHERE table_schema = 'public' AND table_name = 'mask_rules'
  AND column_name IN ('range_bucket_width', 'range_bucket_offset', 'range_granularity', 'schema_name')
ORDER BY ordinal_position`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	wantTypes := []string{"integer", "integer", "text", "text"}
	index := 0
	for rows.Next() {
		var name, dataType, nullable string
		var defaultValue sql.NullString
		require.NoError(t, rows.Scan(&name, &dataType, &nullable, &defaultValue))
		require.Less(t, index, len(wantTypes))
		require.Equal(t, wantTypes[index], dataType, name)
		require.Equal(t, "YES", nullable, name)
		require.False(t, defaultValue.Valid, name)
		index++
	}
	require.NoError(t, rows.Err())
	require.Equal(t, len(wantTypes), index)
}

func assertPostgresAuditErrorCodeColumn(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	var errorCodeType, nullable string
	var defaultValue sql.NullString
	require.NoError(t, database.QueryRowContext(ctx, `
SELECT data_type, is_nullable, column_default
FROM information_schema.columns
WHERE table_schema='public' AND table_name='audit_logs' AND column_name='error_code'`).Scan(
		&errorCodeType, &nullable, &defaultValue,
	))
	require.Equal(t, "text", errorCodeType)
	require.Equal(t, "YES", nullable)
	require.False(t, defaultValue.Valid)
}

func TestPostgres18AuditErrorCodeMigrationsE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("postgres:18 audit error-code migrations are integration tests")
	}
	ctx := dockerTestContext(t)
	for _, test := range []struct {
		name       string
		directory  string
		oldVersion int
		migrate    func(context.Context, *sql.DB) error
		versions   func(context.Context, *sql.DB) (int, int, error)
		latest     int
	}{
		{
			name: "combined", directory: "migrations/postgres", oldVersion: 5, latest: 15,
			migrate: func(ctx context.Context, db *sql.DB) error { return Migrate(ctx, db, DialectPostgres) },
			versions: func(ctx context.Context, db *sql.DB) (int, int, error) {
				return MetadataMigrationVersions(ctx, db, DialectPostgres, false)
			},
		},
		{
			name: "audit only", directory: "migrations/audit/postgres", oldVersion: 2, latest: 6,
			migrate: func(ctx context.Context, db *sql.DB) error { return MigrateAudit(ctx, db, DialectPostgres) },
			versions: func(ctx context.Context, db *sql.DB) (int, int, error) {
				return AuditMigrationVersions(ctx, db, DialectPostgres)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			dsn := startPostgres18StoreContainer(t, ctx, "agentsql_audit_code_"+strings.ReplaceAll(test.name, " ", "_"), "audit-code-password")
			database, err := sql.Open("pgx", dsn)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, database.Close()) })
			migratePostgresThroughVersion(t, ctx, database, test.directory, test.oldVersion)
			_, err = database.ExecContext(ctx, `INSERT INTO audit_logs(decision, error_msg) VALUES('error', 'safe legacy message')`)
			require.NoError(t, err)

			require.NoError(t, test.migrate(ctx, database))
			current, latest, err := test.versions(ctx, database)
			require.NoError(t, err)
			require.Equal(t, test.latest, current)
			require.Equal(t, test.latest, latest)
			assertPostgresAuditErrorCodeColumn(t, ctx, database)
			var legacyCode sql.NullString
			require.NoError(t, database.QueryRowContext(ctx, `SELECT error_code FROM audit_logs ORDER BY id LIMIT 1`).Scan(&legacyCode))
			require.False(t, legacyCode.Valid)

			repository := &AuditLogRepository{repositoryBase: repositoryBase{db: database, dialect: DialectPostgres}}
			inserted, err := repository.Insert(ctx, model.AuditLog{
				Decision: "error", ErrorCode: stringPointerStoreTest("DB_DATA_EXCEPTION"),
			})
			require.NoError(t, err)
			require.Equal(t, "DB_DATA_EXCEPTION", *inserted.ErrorCode)
			page, err := repository.Page(ctx, 1, 10)
			require.NoError(t, err)
			require.Len(t, page.List, 2)
			require.Equal(t, "DB_DATA_EXCEPTION", *page.List[0].ErrorCode)
			require.Nil(t, page.List[1].ErrorCode)
		})
	}
}

func TestPostgres18RangeMigrationFromV3E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("postgres:18 range v3-to-v4 migration E2E is an integration test")
	}
	ctx := dockerTestContext(t)
	for _, testCase := range []struct {
		name      string
		directory string
		separated bool
	}{
		{name: "combined", directory: "migrations/postgres"},
		{name: "separated metadata", directory: "migrations/metadata/postgres", separated: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			dsn := startPostgres18StoreContainer(t, ctx, "agentsql_range_v3_"+strings.ReplaceAll(testCase.name, " ", "_"), "range-password")
			database, err := sql.Open("pgx", dsn)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, database.Close()) })
			migratePostgresThroughVersion(t, ctx, database, testCase.directory, 3)
			_, err = database.ExecContext(ctx, `
INSERT INTO mask_rules(id,datasource_id,table_name,column_name,sensitive_type,algo,enabled)
VALUES('legacy-v3','ds-1','users','phone','phone','mask',TRUE)`)
			require.NoError(t, err)

			require.NoError(t, MigrateMetadata(ctx, database, DialectPostgres, testCase.separated))
			current, latest, err := MetadataMigrationVersions(ctx, database, DialectPostgres, testCase.separated)
			require.NoError(t, err)
			if testCase.separated {
				require.Equal(t, 15, current)
				require.Equal(t, 15, latest)
			} else {
				require.Equal(t, 16, current)
				require.Equal(t, 16, latest)
			}
			assertPostgresMaskRuleRangeColumns(t, ctx, database)
			repository := &MaskRuleRepository{repositoryBase: repositoryBase{db: database, dialect: DialectPostgres}}
			stored, err := repository.Get(ctx, "legacy-v3")
			require.NoError(t, err)
			require.Nil(t, stored.RangeBucketWidth)
			require.Nil(t, stored.RangeBucketOffset)
			require.Nil(t, stored.RangeGranularity)
			require.Empty(t, stored.SchemaName)
			require.Empty(t, stored.TableName)
		})
	}
}

func TestPostgres18VersionOneMetadataUpgradeE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("postgres:18 version-one metadata upgrade E2E is an integration test")
	}
	ctx := dockerTestContext(t)
	dsn := startPostgres18StoreContainer(t, ctx, "agentsql_upgrade_v1", "upgrade-password")
	database, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	_, err = database.ExecContext(ctx, postgresSchemaMigrationsDDL)
	require.NoError(t, err)
	versionOne, err := fs.ReadFile(migrationFiles, "migrations/postgres/0001_init.sql")
	require.NoError(t, err)
	require.NoError(t, applyMigration(ctx, database, DialectPostgres, 1, string(versionOne)))
	var before int
	require.NoError(t, database.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&before))
	require.Equal(t, 1, before)

	require.NoError(t, Migrate(ctx, database, DialectPostgres))
	current, latest, err := MetadataMigrationVersions(ctx, database, DialectPostgres, false)
	require.NoError(t, err)
	require.Equal(t, 16, current)
	require.Equal(t, 16, latest)
	require.Contains(t, postgresTableNames(t, ctx, database), "notification_settings")
	require.Contains(t, postgresTableNames(t, ctx, database), "notification_channels")
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

	_, err = database.ExecContext(ctx, `
INSERT INTO mask_rules(id,datasource_id,schema_name,table_name,column_name,sensitive_type,algo)
VALUES
  ('scope-users','ds_pg_migration','','users','email','email','mask'),
  ('scope-customers','ds_pg_migration','','customers','email','email','mask')`)
	require.NoError(t, err, "different tables may use the same normalized column")
	_, err = database.ExecContext(ctx, `
INSERT INTO mask_rules(id,datasource_id,schema_name,table_name,column_name,sensitive_type,algo)
VALUES('scope-duplicate',' ds_pg_migration ','','users',' Email ','email','mask')`)
	require.Error(t, err, "the full datasource/schema/table/column key remains unique")
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
