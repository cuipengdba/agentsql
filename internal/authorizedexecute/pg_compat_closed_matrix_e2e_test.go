package authorizedexecute_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/authorizedexecute/internal/businessdb"
	"github.com/cuipengdba/agentsql/internal/b5"
	"github.com/cuipengdba/agentsql/internal/b5coordinator"
	"github.com/cuipengdba/agentsql/internal/b5dml"
	"github.com/cuipengdba/agentsql/internal/bootstrap"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/pipeline"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

const pgCompatClosedMatrixEnv = "AGENTSQL_PG_COMPAT_CLOSED_MATRIX"
const pgCompatClosedMatrixBase64Env = "AGENTSQL_PG_COMPAT_CLOSED_MATRIX_B64"

type pgCompatClosedTarget struct {
	Name         string `json:"name"`
	Host         string `json:"host"`
	Port         int    `json:"port"`
	Database     string `json:"database"`
	Username     string `json:"username"`
	Password     string `json:"password"`
	ServerMajor  int    `json:"server_major"`
	ExpectClosed bool   `json:"expect_closed"`
	ExpectReason string `json:"expect_reason,omitempty"`
}

// TestPGCompatibleClosedOnlyMatrix exercises real vendor kernels. It is
// environment-gated because the images and credentials are not redistributable
// CI fixtures. The JSON environment value is an array of pgCompatClosedTarget.
func TestPGCompatibleClosedOnlyMatrix(t *testing.T) {
	if testing.Short() {
		t.Skip("PG-compatible closed-only matrix requires real vendor databases")
	}
	raw := os.Getenv(pgCompatClosedMatrixEnv)
	if encoded := os.Getenv(pgCompatClosedMatrixBase64Env); raw == "" && encoded != "" {
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		require.NoError(t, err)
		raw = string(decoded)
	}
	if raw == "" {
		t.Skip(pgCompatClosedMatrixEnv + " or " + pgCompatClosedMatrixBase64Env + " is not set")
	}
	var targets []pgCompatClosedTarget
	require.NoError(t, json.Unmarshal([]byte(raw), &targets))
	require.NotEmpty(t, targets)

	for index, target := range targets {
		index := index
		target := target
		name := target.Name
		if name == "" {
			name = "target-" + strconv.Itoa(index)
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			t.Cleanup(cancel)
			require.NotEmpty(t, target.Host)
			require.Positive(t, target.Port)
			require.NotEmpty(t, target.Database)
			require.NotEmpty(t, target.Username)
			require.Positive(t, target.ServerMajor)

			secret := []byte("pg-compat-matrix-secret-32-bytes")
			cipher, err := store.NewPasswordCipher(secret)
			require.NoError(t, err)
			passwordEnc, err := cipher.Encrypt(target.Password)
			require.NoError(t, err)
			datasource := model.Datasource{
				ID: "pg-compat-" + strconv.Itoa(index), Name: name, DBType: "postgres",
				Host: target.Host, Port: target.Port, Database: target.Database, Username: target.Username,
				PasswordEnc: passwordEnc, ConnLimit: 2, StmtTimeoutMS: 10_000, RowLimit: 100,
			}
			gateway := executor.NewGateway(false)
			t.Cleanup(func() { require.NoError(t, gateway.CloseAll()) })
			capability, probeErr := gateway.ProbePostgresB2Modes(ctx, datasource, secret)
			if !target.ExpectClosed {
				require.Error(t, probeErr)
				if target.ExpectReason != "" {
					require.Equal(t, target.ExpectReason, string(executor.StableError(probeErr).Reason))
				}
				return
			}

			require.NoError(t, probeErr)
			require.Equal(t, target.ServerMajor, capability.ServerMajor)
			require.Equal(t, string(businessdb.BinderModeCatalogClosedV1), capability.Mode)
			require.NotEmpty(t, capability.ClosedDigest)
			require.False(t, capability.NativeAvailable)
			require.Empty(t, capability.NativeDigest)
			require.Empty(t, capability.ABI)
			require.Empty(t, capability.ExtensionVersion)
			exercisePGCompatibleClosedMatrix(t, ctx, gateway, datasource, secret, target.Password, target.ServerMajor, index)
		})
	}
}

func exercisePGCompatibleClosedMatrix(t *testing.T, ctx context.Context, gateway *executor.Gateway, datasource model.Datasource, secret []byte, password string, serverMajor, index int) {
	t.Helper()
	connectionURL := url.URL{Scheme: "postgres", User: url.UserPassword(datasource.Username, password),
		Host: net.JoinHostPort(datasource.Host, strconv.Itoa(datasource.Port)), Path: "/" + datasource.Database}
	setup, err := pgx.Connect(ctx, connectionURL.String())
	require.NoError(t, err)
	schema := "agentsql_closed_matrix_" + strconv.Itoa(index)
	_, err = setup.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = setup.Exec(cleanupContext, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
		_ = setup.Close(cleanupContext)
	})
	_, err = setup.Exec(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	_, err = setup.Exec(ctx, "CREATE TABLE "+schema+".closed_probe(id integer,label text)")
	require.NoError(t, err)
	_, err = setup.Exec(ctx, "CREATE TABLE "+schema+".indexed_probe(id integer PRIMARY KEY,label text)")
	require.NoError(t, err)
	_, err = setup.Exec(ctx, "INSERT INTO "+schema+".closed_probe VALUES (1,'closed-only'),(4,'must-stay')")
	require.NoError(t, err)

	binder, err := businessdb.NewPostgresExecutor(ctx, datasource, password, true)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, binder.Close()) })
	query := fmt.Sprintf("SELECT p.id FROM %s.closed_probe p WHERE p.id=1", schema)
	prepared, err := binder.BindClosedSelect(ctx, businessdb.BindRequest{RawSQL: query,
		Identity: businessdb.SemanticIdentity{DatasourceIdentity: datasource.ID}}, executor.NewBudget(executor.DefaultLimits))
	require.NoError(t, err)
	program := prepared.Program()
	require.Equal(t, businessdb.BinderModeCatalogClosedV1, program.Mode)
	result, err := prepared.Execute(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, [][]string{{"1"}}, result.Rows)
	proof, err := prepared.VerifyPost(ctx, executor.NewBudget(executor.DefaultLimits))
	require.NoError(t, err)
	require.Equal(t, businessdb.BinderModeCatalogClosedV1, proof.Mode)
	require.NoError(t, prepared.Close(ctx))

	exercisePGCompatibleColumnAuthorization(t, ctx, datasource, secret, password, schema, query, program.Facts)
	exercisePGCompatibleClosedDML(t, ctx, gateway, datasource, secret, schema, serverMajor, setup)

	unsafeSQL := fmt.Sprintf("UPDATE %s.indexed_probe SET label='unsafe' WHERE id=1", schema)
	unsafe, unsafeErr := binder.BindClosedDML(ctx, businessdb.BindRequest{RawSQL: unsafeSQL,
		Identity: businessdb.SemanticIdentity{DatasourceIdentity: datasource.ID}}, executor.NewBudget(executor.DefaultLimits))
	if unsafe != nil {
		_ = unsafe.Close(ctx)
	}
	require.Error(t, unsafeErr, "indexed DML must fail closed before execution")
	require.Equal(t, "AUTH_IMPLICIT_OBJECT_UNSUPPORTED", string(executor.StableError(unsafeErr).Reason))
}

func exercisePGCompatibleColumnAuthorization(t *testing.T, ctx context.Context, datasource model.Datasource,
	secret []byte, password, schema, query string, facts businessdb.SemanticFacts) {
	t.Helper()
	config := loadClosedOnlyConfig(t, false)
	config.ColumnAuthorization.Enabled = true
	config.ColumnAuthorization.InstanceID = "pg-compat-" + datasource.ID
	apiKey := seedPGCompatColumnMetadata(t, ctx, config.Store.SQLitePath, secret, datasource, password, schema, facts)
	runtime, err := bootstrap.Assemble(ctx, config, secret)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })
	status := runtime.B2Status()
	require.Equal(t, bootstrap.B2StateActive, status.State)
	require.Equal(t, string(businessdb.BinderModeCatalogClosedV1), status.DatasourceModes[datasource.ID])

	allowed, err := runtime.Pipeline.Process(ctx, pipeline.Request{APIKey: apiKey, DatasourceID: datasource.ID,
		SQL: query, MCPTool: "query"})
	require.NoError(t, err)
	require.Equal(t, model.DecisionAllow, allowed.Decision)
	require.NotNil(t, allowed.ColumnAuth)
	require.NotNil(t, allowed.Result)
	require.Equal(t, [][]string{{"1"}}, allowed.Result.Rows)

	deniedSQL := fmt.Sprintf("SELECT p.label FROM %s.closed_probe p WHERE p.id=1", schema)
	denied, err := runtime.Pipeline.Process(ctx, pipeline.Request{APIKey: apiKey, DatasourceID: datasource.ID,
		SQL: deniedSQL, MCPTool: "query"})
	require.NoError(t, err)
	require.Equal(t, model.DecisionDeny, denied.Decision)
	require.NotNil(t, denied.ColumnAuth)
	require.Nil(t, denied.Result)
}

func seedPGCompatColumnMetadata(t *testing.T, ctx context.Context, path string, secret []byte,
	datasource model.Datasource, password, schema string, facts businessdb.SemanticFacts) string {
	t.Helper()
	metadata, err := store.OpenWithSecret(ctx, path, secret)
	require.NoError(t, err)
	defer func() { require.NoError(t, metadata.Close()) }()
	apiKey, hash, err := store.GenerateAPIKey()
	require.NoError(t, err)
	agentID := "pg-compat-column-agent-" + datasource.ID
	_, err = metadata.Agents().Create(ctx, model.Agent{ID: agentID, Name: "PG compatibility column agent",
		Status: "active", APIKeyHash: hash, Level: "readonly"})
	require.NoError(t, err)
	_, err = metadata.Datasources().Create(ctx, datasource, password)
	require.NoError(t, err)
	_, err = metadata.Policies().Create(ctx, model.Policy{ID: "pg-compat-table-" + datasource.ID,
		AgentID: agentID, DatasourceID: datasource.ID, ObjectType: "table",
		ObjectName: schema + ".closed_probe", Action: "allow", Revision: 1})
	require.NoError(t, err)
	for _, policy := range closedOnlyColumnPolicies(agentID, datasource.ID, facts) {
		_, err = metadata.Policies().Create(ctx, policy)
		require.NoError(t, err)
	}
	return apiKey
}

func exercisePGCompatibleClosedDML(t *testing.T, ctx context.Context, gateway *executor.Gateway,
	datasource model.Datasource, secret []byte, schema string, serverMajor int, setup *pgx.Conn) {
	t.Helper()
	const principal = "pg-compat-dml-agent"
	provider := func(_ context.Context, facts b5dml.StatementFacts, statement b5coordinator.StatementRequest, _ int) (executor.B5DMLAuthorizationConfig, error) {
		grants := []b5dml.Grant{{Element: b5dml.GrantAction, Action: facts.Action, Relation: facts.Target}}
		if statement.OperationID != "denied-update" {
			for _, write := range facts.Writes {
				grants = append(grants, b5dml.Grant{Element: b5dml.GrantWriteTarget, Action: facts.Action,
					Relation: write.Relation, WriteKind: write.Kind, Column: write.Column})
			}
			for _, reference := range facts.References {
				grants = append(grants, b5dml.Grant{Element: b5dml.GrantReference, Action: facts.Action,
					Relation: reference.Relation, ReferenceKind: reference.Kind, Column: reference.Column})
			}
		}
		policy := b5dml.Policy{ID: "pg-compat-" + statement.OperationID, Revision: 1, PrincipalID: principal,
			DatasourceID: datasource.ID, Effect: b5dml.GrantAllow, Grants: grants}
		return executor.B5DMLAuthorizationConfig{PrincipalID: principal, DatasourceID: datasource.ID,
			Policies: []b5dml.Policy{policy}, Decision: b5coordinator.DecisionAllow, PreliminaryAllowed: true,
			DatasourceSupported: true, PolicySnapshotDigest: "pg-compat-policy-v1:" + statement.OperationID}, nil
	}
	runtime, err := gateway.NewB5ClosedPostgresRuntimeWithPolicyProvider(ctx, datasource, secret, provider)
	require.NoError(t, err)
	newCoordinator := func() *b5coordinator.Coordinator {
		value, newErr := b5coordinator.New(b5coordinator.Config{Transactions: b5coordinator.NewMemoryTransactionStore(),
			Sessions: b5coordinator.StaticSessionGate{SessionID: "pg-compat-session", OwnerEpoch: 1},
			Engine:   runtime.Engine, Audit: &b5coordinator.MemoryAuditor{}})
		require.NoError(t, newErr)
		return value
	}
	plan := func(statements ...b5coordinator.StatementRequest) b5coordinator.PlanRequest {
		limits := b5coordinator.DefaultResourceLimits()
		limits.StatementTimeout = 10 * time.Second
		return b5coordinator.PlanRequest{TenantID: "pg-compat", PrincipalID: principal, AgentID: principal,
			DatasourceID: datasource.ID, KeyRevision: 1, DatasourceRevision: 1, PolicyRevision: 1,
			Dialect: "postgres", ServerMajor: serverMajor, Isolation: "serializable", BinderABI: b5dml.BinderABI,
			ClosurePolicy: string(b5dml.BinderAttestationCatalogClosed), Statements: statements, Limits: limits}
	}
	session := b5coordinator.SessionAuthorization{SessionID: "pg-compat-session", OwnerEpoch: 1}
	statements := []b5coordinator.StatementRequest{
		{OperationID: "insert-values", SQL: fmt.Sprintf("INSERT INTO %s.closed_probe(id,label) VALUES (2,'inserted')", schema), Reason: "closed insert values"},
		{OperationID: "insert-implicit-null", SQL: fmt.Sprintf("INSERT INTO %s.closed_probe(id) VALUES (3)", schema), Reason: "closed insert implicit null"},
		{OperationID: "update-where", SQL: fmt.Sprintf("UPDATE %s.closed_probe SET label='updated' WHERE id=2", schema), Reason: "closed update where"},
		{OperationID: "delete-where", SQL: fmt.Sprintf("DELETE FROM %s.closed_probe WHERE id=1", schema), Reason: "closed delete where"},
	}
	coordinator := newCoordinator()
	_, err = coordinator.Begin(ctx, b5coordinator.BeginRequest{Session: session, TransactionID: "pg-compat-commit",
		RequestID: "begin", Plan: plan(statements...)}, runtime.Analyzer)
	require.NoError(t, err)
	for ordinal, statement := range statements {
		result, executeErr := coordinator.Execute(ctx, b5coordinator.ExecuteRequest{Session: session,
			TransactionID: "pg-compat-commit", RequestID: fmt.Sprintf("execute-%d", ordinal),
			OperationID: statement.OperationID, Ordinal: ordinal})
		require.NoError(t, executeErr)
		require.EqualValues(t, 1, result.AffectedRows)
	}
	committed, err := coordinator.Commit(ctx, b5coordinator.FinishRequest{Session: session,
		TransactionID: "pg-compat-commit", RequestID: "commit"})
	require.NoError(t, err)
	require.Equal(t, b5.OutcomeCommitted, committed.DBOutcome)
	rows, err := setup.Query(ctx, "SELECT id,label FROM "+schema+".closed_probe ORDER BY id")
	require.NoError(t, err)
	type row struct {
		id    int
		label *string
	}
	var values []row
	for rows.Next() {
		var value row
		require.NoError(t, rows.Scan(&value.id, &value.label))
		values = append(values, value)
	}
	require.NoError(t, rows.Err())
	rows.Close()
	updated, untouched := "updated", "must-stay"
	require.Equal(t, []row{{id: 2, label: &updated}, {id: 3}, {id: 4, label: &untouched}}, values)

	rollbackStatement := b5coordinator.StatementRequest{OperationID: "rollback-update",
		SQL: fmt.Sprintf("UPDATE %s.closed_probe SET label='rolled-back' WHERE id=4", schema), Reason: "closed rollback proof"}
	rollbackCoordinator := newCoordinator()
	_, err = rollbackCoordinator.Begin(ctx, b5coordinator.BeginRequest{Session: session, TransactionID: "pg-compat-rollback",
		RequestID: "begin-rollback", Plan: plan(rollbackStatement)}, runtime.Analyzer)
	require.NoError(t, err)
	rollbackExecute, err := rollbackCoordinator.Execute(ctx, b5coordinator.ExecuteRequest{Session: session,
		TransactionID: "pg-compat-rollback", RequestID: "execute-rollback", OperationID: rollbackStatement.OperationID, Ordinal: 0})
	require.NoError(t, err)
	require.EqualValues(t, 1, rollbackExecute.AffectedRows)
	rolledBack, err := rollbackCoordinator.Rollback(ctx, b5coordinator.FinishRequest{Session: session,
		TransactionID: "pg-compat-rollback", RequestID: "rollback"})
	require.NoError(t, err)
	require.Equal(t, b5.OutcomeNotCommitted, rolledBack.DBOutcome)
	var label string
	require.NoError(t, setup.QueryRow(ctx, "SELECT label FROM "+schema+".closed_probe WHERE id=4").Scan(&label))
	require.Equal(t, "must-stay", label)

	denied := b5coordinator.StatementRequest{OperationID: "denied-update",
		SQL: fmt.Sprintf("UPDATE %s.closed_probe SET label='forbidden' WHERE id=4", schema), Reason: "missing write grant must deny"}
	_, err = newCoordinator().Begin(ctx, b5coordinator.BeginRequest{Session: session, TransactionID: "pg-compat-deny",
		RequestID: "begin-deny", Plan: plan(denied)}, runtime.Analyzer)
	require.Error(t, err)
	require.Equal(t, b5.ErrorAuthDMLWriteTargetGrantMissing, b5coordinator.ErrorCode(err))
	require.NoError(t, setup.QueryRow(ctx, "SELECT label FROM "+schema+".closed_probe WHERE id=4").Scan(&label))
	require.Equal(t, "must-stay", label)
}
