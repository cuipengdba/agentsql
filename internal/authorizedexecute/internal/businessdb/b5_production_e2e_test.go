package businessdb_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/authorizedexecute/internal/businessdb"
	"github.com/cuipengdba/agentsql/internal/b5"
	"github.com/cuipengdba/agentsql/internal/b5coordinator"
	"github.com/cuipengdba/agentsql/internal/b5dml"
	"github.com/cuipengdba/agentsql/internal/bootstrap"
	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/mcpserver"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	mysqlcontainer "github.com/testcontainers/testcontainers-go/modules/mysql"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestB5ProductionOrdinaryPostgresNoExtensionCrossRequestAndFailClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("B5 production path requires ordinary PostgreSQL and MySQL containers")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)
	const (
		databaseName = "agentsql"
		username     = "agentsql"
		password     = "b5-production-password"
		datasourceID = "b5-production-pg18"
		agentID      = "b5-production-agent"
		statementSQL = `UPDATE b5prod.items SET value=value+5 WHERE id=1`
	)
	container, err := postgrescontainer.Run(ctx, "postgres:18-bookworm",
		postgrescontainer.WithDatabase(databaseName), postgrescontainer.WithUsername(username),
		postgrescontainer.WithPassword(password), postgrescontainer.BasicWaitStrategies())
	if err != nil {
		if container != nil {
			testcontainers.CleanupContainer(t, container)
		}
		require.NoError(t, err, "start ordinary postgres:18")
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
	_, err = setup.Exec(ctx, `CREATE SCHEMA b5prod; CREATE TABLE b5prod.items(id integer,value integer); INSERT INTO b5prod.items VALUES(1,10),(2,20)`)
	require.NoError(t, err)
	var extensionAvailable, extensionInstalled bool
	require.NoError(t, setup.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_available_extensions WHERE name='agentsql_binder'),EXISTS(SELECT 1 FROM pg_catalog.pg_extension WHERE extname='agentsql_binder')`).Scan(&extensionAvailable, &extensionInstalled))
	require.False(t, extensionAvailable)
	require.False(t, extensionInstalled)

	datasource := model.Datasource{ID: datasourceID, Name: "B5 ordinary PostgreSQL", DBType: "postgres", Host: host,
		Port: port.Int(), Database: databaseName, Username: username, ConnLimit: 4, StmtTimeoutMS: 5_000, RowLimit: 100}
	binder, err := businessdb.NewPostgresExecutor(ctx, datasource, password, true)
	require.NoError(t, err)
	prepared, err := binder.BindClosedDML(ctx, businessdb.BindRequest{RawSQL: statementSQL,
		Identity: businessdb.SemanticIdentity{DatasourceIdentity: datasourceID}}, executor.NewBudget(executor.DefaultLimits))
	require.NoError(t, err)
	facts := prepared.Program().Facts
	require.NoError(t, prepared.Close(ctx))
	require.NoError(t, binder.Close())

	secret := []byte("0123456789abcdef0123456789abcdef")
	metadataPath := filepath.Join(t.TempDir(), "metadata.db")
	metadata, err := store.OpenWithSecret(ctx, metadataPath, secret)
	require.NoError(t, err)
	metadataOpen := true
	t.Cleanup(func() {
		if metadataOpen {
			require.NoError(t, metadata.Close())
		}
	})
	apiKey := "asql_b5_production_e2e"
	_, err = metadata.Agents().Create(ctx, model.Agent{ID: agentID, Name: "B5 production", Status: "active", APIKeyHash: store.HashAPIKey(apiKey), Level: "dml"})
	require.NoError(t, err)
	_, err = metadata.Datasources().Create(ctx, datasource, password)
	require.NoError(t, err)
	_, err = metadata.Datasources().Create(ctx, model.Datasource{ID: "b5-production-broken-pg", Name: "Broken PG", DBType: "postgres",
		Host: "127.0.0.1", Port: 1, Database: databaseName, Username: username, ConnLimit: 1, StmtTimeoutMS: 500, RowLimit: 10}, "wrong")
	require.NoError(t, err)
	_, err = metadata.Datasources().Create(ctx, model.Datasource{ID: "b5-production-mysql-neighbor", Name: "MySQL neighbor", DBType: "mysql",
		Host: "127.0.0.1", Port: 3306, Database: databaseName, Username: username, ConnLimit: 1, StmtTimeoutMS: 500, RowLimit: 10}, "wrong")
	require.NoError(t, err)
	seedB5ProductionGrants(t, ctx, metadata, agentID, datasourceID, facts)
	require.NoError(t, metadata.Close())
	metadataOpen = false

	cfg := config.Config{Server: config.ServerConfig{HTTPListen: "127.0.0.1:8651", EventStreamMaxConnections: 100},
		Store:    config.StoreConfig{SQLitePath: metadataPath, AutoMigrate: true},
		Defaults: config.DefaultsConfig{StatementTimeoutMS: 5_000, RowLimit: 1_000, MaxConnsPerDatasource: 5, QPSPerAgent: 100}, Theme: config.ThemeConfig{Default: "dark"}}
	runtime, err := bootstrap.Assemble(ctx, cfg, secret)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })
	require.NotNil(t, runtime.B5)
	authority, err := runtime.B5.ResolveDatasource(ctx, datasourceID)
	require.NoError(t, err)
	require.Equal(t, "CATALOG_CLOSED_V1", authority.Mode)
	_, err = runtime.B5.ResolveDatasource(ctx, "b5-production-broken-pg")
	require.Error(t, err)
	require.Equal(t, b5.ErrorPostgresCapabilityUnavailable, b5coordinator.ErrorCode(err))
	_, err = runtime.B5.ResolveDatasource(ctx, "b5-production-mysql-neighbor")
	require.Error(t, err)
	require.Equal(t, b5.ErrorDialectTransactionUnsupported, b5coordinator.ErrorCode(err))
	_, err = runtime.B5.ResolveDatasource(ctx, datasourceID)
	require.NoError(t, err, "a broken PG and MySQL neighbor must not revoke the healthy PG capability")

	service := &mcpserver.B5CoordinatorService{Directory: runtime.B5.Directory, Coordinator: runtime.B5.Coordinator,
		AnalyzerResolver: runtime.B5.Analyzer, InstanceID: runtime.B5.InstanceID, StickyRoute: runtime.B5.StickyRoute,
		Admission: runtime.B5.Admit, CheckDialect: runtime.B5.CheckDatasourceDialect, Limits: runtime.B5.Limits,
		FinalFence: runtime.B5.FinalFence, ResolveDatasource: func(resolveContext context.Context, id string) (mcpserver.B5DatasourceAuthority, error) {
			value, resolveErr := runtime.B5.ResolveDatasource(resolveContext, id)
			return mcpserver.B5DatasourceAuthority{Dialect: value.Dialect, Mode: value.Mode, ServerMajor: value.ServerMajor,
				KeyRevision: value.KeyRevision, DatasourceRevision: value.DatasourceRevision, PolicyRevision: value.PolicyRevision}, resolveErr
		}}
	opened, err := service.OpenSession(ctx, agentID, mcpserver.B5OpenSessionInput{})
	require.NoError(t, err)
	begin := mcpserver.B5BeginInput{B5Continuation: mcpserver.B5Continuation{SessionID: opened.SessionID, OwnerEpoch: opened.OwnerEpoch, RequestID: "begin-production"},
		TransactionID: "b5-production-tx", DatasourceID: datasourceID, Dialect: "postgres", ServerMajor: 18,
		KeyRevision: 99, DatasourceRevision: 99, PolicyRevision: 99,
		Statements: []mcpserver.B5PlanStatement{{OperationID: "update-one", SQL: statementSQL, Reason: "production no-extension transaction"}}}
	signMatrix(t, opened.ContinuationSecret, "begin_transaction", &begin.B5Continuation, begin)
	beginResult, err := service.Begin(ctx, agentID, begin)
	require.NoError(t, err)
	require.Equal(t, b5.TransactionActive, beginResult.Status)
	execute := mcpserver.B5ExecuteInput{B5Continuation: mcpserver.B5Continuation{SessionID: opened.SessionID, OwnerEpoch: opened.OwnerEpoch, RequestID: "execute-production"},
		TransactionID: begin.TransactionID, OperationID: "update-one", Ordinal: 0}
	signMatrix(t, opened.ContinuationSecret, "execute_transaction_statement", &execute.B5Continuation, execute)
	executed, err := service.Execute(ctx, agentID, execute)
	require.NoError(t, err)
	require.EqualValues(t, 1, executed.AffectedRows)
	commit := mcpserver.B5FinishInput{B5Continuation: mcpserver.B5Continuation{SessionID: opened.SessionID, OwnerEpoch: opened.OwnerEpoch, RequestID: "commit-production"}, TransactionID: begin.TransactionID}
	signMatrix(t, opened.ContinuationSecret, "commit_transaction", &commit.B5Continuation, commit)
	committed, err := service.Commit(ctx, agentID, commit)
	require.NoError(t, err)
	require.Equal(t, b5.OutcomeCommitted, committed.DBOutcome)
	var value int
	require.NoError(t, setup.QueryRow(ctx, `SELECT value FROM b5prod.items WHERE id=1`).Scan(&value))
	require.Equal(t, 15, value)

	second, err := service.OpenSession(ctx, agentID, mcpserver.B5OpenSessionInput{})
	require.NoError(t, err)
	invalid := mcpserver.B5BeginInput{B5Continuation: mcpserver.B5Continuation{SessionID: second.SessionID, OwnerEpoch: second.OwnerEpoch, RequestID: "begin-out-of-bounds"},
		TransactionID: "b5-production-invalid", DatasourceID: datasourceID, Dialect: "postgres", ServerMajor: 18,
		KeyRevision: 1, DatasourceRevision: 1, PolicyRevision: 1,
		Statements: []mcpserver.B5PlanStatement{{OperationID: "bad", SQL: `SELECT value FROM b5prod.items WHERE id=2`, Reason: "must fail closed"}}}
	signMatrix(t, second.ContinuationSecret, "begin_transaction", &invalid.B5Continuation, invalid)
	_, err = service.Begin(ctx, agentID, invalid)
	require.Error(t, err)
	require.Equal(t, b5.ErrorTxSelectUnsupported, b5coordinator.ErrorCode(err))
	require.ErrorIs(t, func() error {
		_, lookupErr := runtime.Store.B5Transactions().Get(ctx, invalid.TransactionID)
		return lookupErr
	}(), store.ErrNotFound)
	require.NoError(t, setup.QueryRow(ctx, `SELECT value FROM b5prod.items WHERE id=2`).Scan(&value))
	require.Equal(t, 20, value)
}

func TestB5ProductionMySQLContainerUnsupported(t *testing.T) {
	if testing.Short() {
		t.Skip("B5 production MySQL rejection requires a real container")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	container, err := mysqlcontainer.Run(ctx, "mysql:8.4", mysqlcontainer.WithDatabase("agentsql"),
		mysqlcontainer.WithUsername("agentsql"), mysqlcontainer.WithPassword("b5-mysql-password"))
	if err != nil {
		if container != nil {
			testcontainers.CleanupContainer(t, container)
		}
		require.NoError(t, err, "start mysql:8.4")
	}
	testcontainers.CleanupContainer(t, container)
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "3306/tcp")
	require.NoError(t, err)
	secret := []byte("0123456789abcdef0123456789abcdef")
	cfg := config.Config{Server: config.ServerConfig{HTTPListen: "127.0.0.1:8652", EventStreamMaxConnections: 100},
		Store:    config.StoreConfig{SQLitePath: filepath.Join(t.TempDir(), "mysql-metadata.db"), AutoMigrate: true},
		Defaults: config.DefaultsConfig{StatementTimeoutMS: 5_000, RowLimit: 1_000, MaxConnsPerDatasource: 5, QPSPerAgent: 100}, Theme: config.ThemeConfig{Default: "dark"}}
	runtime, err := bootstrap.Assemble(ctx, cfg, secret)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })
	_, err = runtime.Store.Datasources().Create(ctx, model.Datasource{ID: "b5-production-mysql", Name: "B5 MySQL", DBType: "mysql",
		Host: host, Port: port.Int(), Database: "agentsql", Username: "agentsql", ConnLimit: 2, StmtTimeoutMS: 5_000, RowLimit: 100}, "b5-mysql-password")
	require.NoError(t, err)
	err = runtime.B5.CheckDatasourceDialect(ctx, "b5-production-mysql")
	require.Error(t, err)
	require.Equal(t, b5.ErrorDialectTransactionUnsupported, b5coordinator.ErrorCode(err))
	require.Contains(t, err.Error(), "MySQL")
	require.Contains(t, err.Error(), "不受支持")
	require.Contains(t, err.Error(), "未取得可写连接")
	_, err = runtime.B5.ResolveDatasource(ctx, "b5-production-mysql")
	require.Error(t, err)
	require.Equal(t, b5.ErrorDialectTransactionUnsupported, b5coordinator.ErrorCode(err))
}

func seedB5ProductionGrants(t *testing.T, ctx context.Context, metadata *store.Store, principal, datasource string, facts businessdb.SemanticFacts) {
	t.Helper()
	require.Len(t, facts.Relations, 1)
	relation := facts.Relations[0]
	_, err := metadata.Policies().Create(ctx, model.Policy{
		ID:           "b5-production-policy",
		AgentID:      principal,
		DatasourceID: datasource,
		ObjectType:   "table",
		ObjectName:   relation.Schema + "." + relation.Name,
		Action:       "allow",
		Revision:     1,
	})
	require.NoError(t, err)
	base := store.B5DMLGrant{PolicyID: "b5-production-policy", PolicyRevision: 1, PrincipalID: principal, DatasourceID: datasource,
		Effect: b5.GrantAllow, Action: facts.Action, DatabaseOID: relation.DatabaseOID, RelationOID: relation.RelationOID,
		RelationKind: string([]byte{relation.Kind}), SchemaName: relation.Schema, RelationName: relation.Name,
		CatalogFingerprint: relation.CatalogFingerprint, ProofSchemaID: b5.DMLGrantProofSchemaID, ProofSchemaVersion: b5.DMLGrantProofSchemaVersion}
	create := func(id string, grant store.B5DMLGrant) {
		grant.GrantID = id
		digest := sha256.Sum256([]byte(id + "\x00" + relation.CatalogFingerprint))
		grant.ProofDigest = digest[:]
		_, err := metadata.B5DMLGrants().Create(ctx, grant)
		require.NoError(t, err)
	}
	action := base
	action.Element = b5.GrantElementAction
	create("b5-production-action", action)
	for index, write := range facts.WriteTargets {
		grant := base
		grant.Element = b5.GrantElementWriteTarget
		var kind string
		switch write.Kind {
		case b5dml.WriteTargetColumn:
			kind = "COLUMN"
		case b5dml.WriteTargetRow:
			kind = "ROW"
		default:
			t.Fatalf("unsupported B5 production write target kind: %d", write.Kind)
		}
		grant.WriteTargetKind = &kind
		if write.Kind == b5dml.WriteTargetColumn {
			attnum, modifier := int(write.Attnum), int(write.TypeModifier)
			grant.ColumnAttnum, grant.ColumnName, grant.ColumnTypeOID = &attnum, &write.Name, &write.TypeOID
			grant.ColumnTypeModifier, grant.ColumnCollationOID = &modifier, &write.CollationOID
		}
		create(fmt.Sprintf("b5-production-write-%d", index), grant)
	}
	for index, reference := range facts.ColumnUses {
		if reference.Usage != businessdb.SemanticUsageReference {
			continue
		}
		grant := base
		grant.Element = b5.GrantElementReference
		kind := "COLUMN"
		attnum, modifier := int(reference.Attnum), int(reference.TypeModifier)
		grant.ReferenceKind = &kind
		grant.ColumnAttnum, grant.ColumnName, grant.ColumnTypeOID = &attnum, &reference.Name, &reference.TypeOID
		grant.ColumnTypeModifier, grant.ColumnCollationOID = &modifier, &reference.CollationOID
		create(fmt.Sprintf("b5-production-reference-%d", index), grant)
	}
}
