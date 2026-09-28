package authorizedexecute_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/authorizedexecute/internal/businessdb"
	"github.com/cuipengdba/agentsql/internal/b5"
	"github.com/cuipengdba/agentsql/internal/b5coordinator"
	"github.com/cuipengdba/agentsql/internal/bootstrap"
	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/pipeline"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestB2ClosedOnlyNoExtensionProductionE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("B2 closed-only production path requires a real PostgreSQL container")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	const (
		databaseName = "agentsql"
		username     = "agentsql"
		password     = "closed-only-password"
		datasourceID = "closed-only-pg16"
		positiveSQL  = `SELECT a.label,b.note FROM closed_e2e.accounts a INNER JOIN closed_e2e.notes b ON a.id=b.id WHERE b.id=1`
		matviewSQL   = `SELECT id,label,pg_catalog.nextval('closed_e2e.execution_probe') AS execution_marker FROM closed_e2e.account_snapshot`
	)
	container, err := postgrescontainer.Run(
		ctx,
		"postgres:16",
		postgrescontainer.WithDatabase(databaseName),
		postgrescontainer.WithUsername(username),
		postgrescontainer.WithPassword(password),
		postgrescontainer.BasicWaitStrategies(),
	)
	if err != nil {
		if container != nil {
			testcontainers.CleanupContainer(t, container)
		}
		require.NoError(t, err, "start ordinary postgres:16")
	}
	testcontainers.CleanupContainer(t, container)

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)
	dsn := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", username, password, host, port.Port(), databaseName)
	setup, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, setup.Close(context.Background())) })
	_, err = setup.Exec(ctx, `
CREATE SCHEMA closed_e2e;
CREATE TABLE closed_e2e.accounts(id integer, label text);
CREATE TABLE closed_e2e.notes(id integer, note text);
INSERT INTO closed_e2e.accounts VALUES (1,'alpha'),(2,'beta');
INSERT INTO closed_e2e.notes VALUES (1,'first'),(2,'second');
CREATE SEQUENCE closed_e2e.execution_probe START WITH 100;
CREATE MATERIALIZED VIEW closed_e2e.account_snapshot AS
SELECT id,label FROM closed_e2e.accounts WITH DATA;
`)
	require.NoError(t, err)
	var extensionAvailable, extensionInstalled bool
	require.NoError(t, setup.QueryRow(ctx, `SELECT
EXISTS(SELECT 1 FROM pg_catalog.pg_available_extensions WHERE name='agentsql_binder'),
EXISTS(SELECT 1 FROM pg_catalog.pg_extension WHERE extname='agentsql_binder')`).Scan(&extensionAvailable, &extensionInstalled))
	require.False(t, extensionAvailable, "the stock image must not contain agentsql_binder files")
	require.False(t, extensionInstalled, "the test must not install agentsql_binder")

	datasource := model.Datasource{
		ID: datasourceID, Name: "Closed-only PostgreSQL 16", DBType: "postgres",
		Host: host, Port: port.Int(), Database: databaseName, Username: username,
		ConnLimit: 4, StmtTimeoutMS: 5_000, RowLimit: 100,
	}
	facts := bindClosedFacts(t, ctx, datasource, password, positiveSQL)
	secret := []byte("0123456789abcdef0123456789abcdef")

	defaultConfig := loadClosedOnlyConfig(t, false)
	require.True(t, defaultConfig.ColumnAuthorization.Enabled, "omitted column_authorization must default on")
	require.True(t, defaultConfig.MCP.Sessions.Enabled, "omitted mcp.sessions must default on")
	require.True(t, defaultConfig.MCP.Transactions.Postgres, "omitted mcp.transactions.postgres must default on")
	apiKey, storedDatasource := seedClosedOnlyMetadata(t, ctx, defaultConfig.Store.SQLitePath, secret, datasource, password, facts, true)
	metadata, err := store.OpenWithSecret(ctx, defaultConfig.Store.SQLitePath, secret)
	require.NoError(t, err)
	_, err = metadata.Datasources().Create(ctx, model.Datasource{ID: "closed-only-mysql-neighbor", Name: "MySQL neighbor", DBType: "mysql",
		Host: "127.0.0.1", Port: 3306, Database: "app", Username: "agentsql", ConnLimit: 1, StmtTimeoutMS: 500, RowLimit: 10}, "not-used")
	require.NoError(t, err)
	_, err = metadata.Datasources().Create(ctx, model.Datasource{ID: "closed-only-broken-pg", Name: "Broken PostgreSQL", DBType: "postgres",
		Host: "127.0.0.1", Port: 1, Database: "app", Username: "agentsql", ConnLimit: 1, StmtTimeoutMS: 500, RowLimit: 10}, "not-used")
	require.NoError(t, err)
	require.NoError(t, metadata.Close())
	runtime, err := bootstrap.Assemble(ctx, defaultConfig, secret)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })

	status := runtime.B2Status()
	require.Equal(t, bootstrap.B2StateActive, status.State)
	require.Equal(t, 3, status.Protocol)
	require.Equal(t, "CATALOG_CLOSED_V1", status.DatasourceModes[datasourceID])
	require.NotContains(t, status.UnsupportedDatasources, datasourceID)
	require.Equal(t, bootstrap.B2ReasonDatasourceUnsupported, status.UnsupportedDatasources["closed-only-mysql-neighbor"])
	require.Equal(t, bootstrap.B2ReasonBinderProbeFailed, status.UnsupportedDatasources["closed-only-broken-pg"])
	require.NotNil(t, runtime.B5)
	require.True(t, runtime.B5.PostgresEnabled())
	b5Status, available, err := runtime.B5Status(ctx)
	require.NoError(t, err)
	require.True(t, available)
	require.True(t, b5Status.Enabled)
	require.True(t, b5Status.Ready)
	require.Equal(t, "READY_WITH_DATASOURCE_ERRORS", b5Status.State)
	authority, err := runtime.B5.ResolveDatasource(ctx, datasourceID)
	require.NoError(t, err)
	require.Equal(t, "CATALOG_CLOSED_V1", authority.Mode)
	_, err = runtime.B5.ResolveDatasource(ctx, "closed-only-mysql-neighbor")
	require.Error(t, err)
	require.Equal(t, b5.ErrorDialectTransactionUnsupported, b5coordinator.ErrorCode(err))
	_, err = runtime.B5.ResolveDatasource(ctx, "closed-only-broken-pg")
	require.Error(t, err)
	require.Equal(t, b5.ErrorPostgresCapabilityUnavailable, b5coordinator.ErrorCode(err))
	_, err = runtime.B5.ResolveDatasource(ctx, datasourceID)
	require.NoError(t, err, "unsupported neighbors must not revoke the ready PostgreSQL datasource")
	probe := executor.NewGateway(false)
	t.Cleanup(func() { require.NoError(t, probe.CloseAll()) })
	capability, err := probe.ProbePostgresB2Modes(ctx, storedDatasource, secret)
	require.NoError(t, err)
	require.Equal(t, "CATALOG_CLOSED_V1", capability.Mode)
	require.False(t, capability.NativeAvailable)

	positive, err := runtime.Pipeline.Process(ctx, pipeline.Request{
		APIKey: apiKey, DatasourceID: datasourceID, SQL: positiveSQL, MCPTool: "query",
	})
	require.NoError(t, err)
	require.Equal(t, model.DecisionAllow, positive.Decision)
	require.NotNil(t, positive.ColumnAuth, "the production pipeline must use B2, not table-level execution")
	require.NotNil(t, positive.Result)
	require.Equal(t, [][]string{{"alpha", "first"}}, positive.Result.Rows)

	_, err = setup.Exec(ctx, `ALTER SEQUENCE closed_e2e.execution_probe RESTART WITH 100`)
	require.NoError(t, err)
	negative, err := runtime.Pipeline.Process(ctx, pipeline.Request{
		APIKey: apiKey, DatasourceID: datasourceID, SQL: matviewSQL, MCPTool: "query",
	})
	require.Error(t, err)
	require.Equal(t, model.DecisionDeny, negative.Decision)
	require.Contains(t, []string{
		string(executor.ReasonRelationShape),
		string(executor.ReasonBinderModeRequired),
	}, negative.ErrorCode)
	require.Nil(t, negative.Result)
	require.Nil(t, negative.ColumnAuth, "shape rejection must happen before execution/audit delivery")
	lastValue, called := sequenceState(t, ctx, setup)
	require.Equal(t, int64(100), lastValue)
	require.False(t, called,
		"unsupported matview SELECT must not execute or fall back to its table-level allow")

	offDatasource := datasource
	offDatasource.ID = datasourceID + "-off"
	offConfig := loadClosedOnlyConfig(t, true)
	require.False(t, offConfig.ColumnAuthorization.Enabled)
	offAPIKey, _ := seedClosedOnlyMetadata(t, ctx, offConfig.Store.SQLitePath, secret, offDatasource, password, businessdb.SemanticFacts{}, false)
	offRuntime, err := bootstrap.Assemble(ctx, offConfig, secret)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, offRuntime.Close()) })
	require.Equal(t, bootstrap.B2StateFeatureOff, offRuntime.B2Status().State)
	require.Equal(t, 2, offRuntime.B2Status().Protocol)

	tableLevel, err := offRuntime.Pipeline.Process(ctx, pipeline.Request{
		APIKey: offAPIKey, DatasourceID: offDatasource.ID, SQL: matviewSQL, MCPTool: "query",
	})
	require.NoError(t, err)
	require.Equal(t, model.DecisionAllow, tableLevel.Decision)
	require.Nil(t, tableLevel.ColumnAuth, "explicit off must restore the table-level path")
	require.NotNil(t, tableLevel.Result)
	require.Len(t, tableLevel.Result.Rows, 2)
	lastValue, called = sequenceState(t, ctx, setup)
	require.Equal(t, int64(101), lastValue)
	require.True(t, called, "the explicit-off table-level query must reach PostgreSQL")
}

func bindClosedFacts(t *testing.T, ctx context.Context, datasource model.Datasource, password, sqlText string) businessdb.SemanticFacts {
	t.Helper()
	binder, err := businessdb.NewPostgresExecutor(ctx, datasource, password, true)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, binder.Close()) })
	prepared, err := binder.BindClosedSelect(ctx, businessdb.BindRequest{
		RawSQL:   sqlText,
		Identity: businessdb.SemanticIdentity{DatasourceIdentity: datasource.ID},
	}, executor.NewBudget(executor.DefaultLimits))
	require.NoError(t, err)
	facts := prepared.Program().Facts
	require.NoError(t, prepared.Close(ctx))
	return facts
}

func loadClosedOnlyConfig(t *testing.T, explicitlyOff bool) config.Config {
	t.Helper()
	root := t.TempDir()
	databasePath := filepath.Join(root, "control.db")
	contents := fmt.Sprintf(`server:
  http_listen: "127.0.0.1:7780"
store:
  sqlite_path: %q
defaults:
  statement_timeout_ms: 5000
  row_limit: 1000
  max_conns_per_datasource: 5
  qps_per_agent: 20
theme:
  default: dark
`, filepath.ToSlash(databasePath))
	if explicitlyOff {
		contents += "column_authorization:\n  enabled: false\n"
	}
	configPath := filepath.Join(root, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(contents), 0o600))
	loaded, err := config.Load(configPath)
	require.NoError(t, err)
	return loaded
}

func seedClosedOnlyMetadata(
	t *testing.T,
	ctx context.Context,
	path string,
	secret []byte,
	datasource model.Datasource,
	password string,
	facts businessdb.SemanticFacts,
	includeColumnPolicies bool,
) (string, model.Datasource) {
	t.Helper()
	metadata, err := store.OpenWithSecret(ctx, path, secret)
	require.NoError(t, err)
	apiKey, hash, err := store.GenerateAPIKey()
	require.NoError(t, err)
	_, err = metadata.Agents().Create(ctx, model.Agent{
		ID: "closed-only-agent-" + datasource.ID, Name: "Closed-only agent", Status: "active",
		APIKeyHash: hash, Level: "readonly",
	})
	require.NoError(t, err)
	storedDatasource, err := metadata.Datasources().Create(ctx, datasource, password)
	require.NoError(t, err)
	for index, objectName := range []string{"closed_e2e.accounts", "closed_e2e.notes", "closed_e2e.account_snapshot"} {
		_, err = metadata.Policies().Create(ctx, model.Policy{
			ID: fmt.Sprintf("table-%s-%d", datasource.ID, index), AgentID: "closed-only-agent-" + datasource.ID,
			DatasourceID: datasource.ID, ObjectType: "table", ObjectName: objectName, Action: "allow",
		})
		require.NoError(t, err)
	}
	if includeColumnPolicies {
		for _, policy := range closedOnlyColumnPolicies("closed-only-agent-"+datasource.ID, datasource.ID, facts) {
			_, err = metadata.Policies().Create(ctx, policy)
			require.NoError(t, err)
		}
	}
	require.NoError(t, metadata.Close())
	return apiKey, storedDatasource
}

func closedOnlyColumnPolicies(agentID, datasourceID string, facts businessdb.SemanticFacts) []model.Policy {
	relations := make(map[uint32]businessdb.SemanticRelation, len(facts.Relations))
	for _, relation := range facts.Relations {
		relations[relation.RelationOID] = relation
	}
	type permissionKey struct {
		ordinal int
		usage   string
	}
	permissions := make(map[uint32]map[permissionKey]model.PolicyColumnPermission, len(relations))
	columnNames := make(map[uint32]map[string]struct{}, len(relations))
	for _, use := range facts.ColumnUses {
		if permissions[use.RelationOID] == nil {
			permissions[use.RelationOID] = make(map[permissionKey]model.PolicyColumnPermission)
			columnNames[use.RelationOID] = make(map[string]struct{})
		}
		usage := string(use.Usage)
		key := permissionKey{ordinal: int(use.Attnum), usage: usage}
		permissions[use.RelationOID][key] = model.PolicyColumnPermission{
			ColumnOrdinal: int(use.Attnum), ColumnName: use.Name,
			ColumnTypeDigest: executor.PostgresColumnTypeDigest(use.TypeOID, use.TypeModifier, use.CollationOID),
			Usage:            usage,
		}
		columnNames[use.RelationOID][use.Name] = struct{}{}
	}
	oids := make([]int, 0, len(relations))
	for oid := range relations {
		oids = append(oids, int(oid))
	}
	sort.Ints(oids)
	result := make([]model.Policy, 0, len(oids))
	for index, rawOID := range oids {
		oid := uint32(rawOID)
		relation := relations[oid]
		bindingID := fmt.Sprintf("closed-binding-%s-%d", datasourceID, index)
		stableID := executor.PostgresStableObjectID(relation.DatabaseOID, relation.RelationOID)
		fingerprint := relation.CatalogFingerprint
		names := make([]string, 0, len(columnNames[oid]))
		for name := range columnNames[oid] {
			names = append(names, name)
		}
		sort.Strings(names)
		columns := strings.Join(names, ",")
		policy := model.Policy{
			ID: fmt.Sprintf("closed-column-%s-%d", datasourceID, index), AgentID: agentID,
			DatasourceID: datasourceID, ObjectType: "column", ObjectName: relation.Schema + "." + relation.Name,
			Columns: &columns, Action: "allow", Revision: 1,
			RelationBinding: &model.RelationPolicyBinding{
				ID: bindingID, SchemaName: relation.Schema, RelationName: relation.Name,
				StableObjectID: &stableID, CatalogFingerprint: &fingerprint, Status: "healthy", Revision: 1,
			},
		}
		keys := make([]permissionKey, 0, len(permissions[oid]))
		for key := range permissions[oid] {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool {
			if keys[i].ordinal == keys[j].ordinal {
				return keys[i].usage < keys[j].usage
			}
			return keys[i].ordinal < keys[j].ordinal
		})
		for _, key := range keys {
			permission := permissions[oid][key]
			permission.RelationEnrollmentID = bindingID
			policy.ColumnPermissions = append(policy.ColumnPermissions, permission)
		}
		result = append(result, policy)
	}
	return result
}

func sequenceState(t *testing.T, ctx context.Context, connection *pgx.Conn) (int64, bool) {
	t.Helper()
	var lastValue int64
	var called bool
	require.NoError(t, connection.QueryRow(ctx,
		`SELECT last_value,is_called FROM closed_e2e.execution_probe`).Scan(&lastValue, &called))
	return lastValue, called
}
