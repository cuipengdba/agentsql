package businessdb

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/b5"
	"github.com/cuipengdba/agentsql/internal/b5coordinator"
	"github.com/cuipengdba/agentsql/internal/b5dml"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// TestB5CoordinatorPostgresMatrix is opt-in because it builds the native
// binder extension. It exercises the complete feature-off S6 path against the
// oldest/newest supported PostgreSQL majors; PG18 uses an encrypted socket.
func TestB5CoordinatorPostgresMatrix(t *testing.T) {
	if os.Getenv("AGENTSQL_B5_COORDINATOR_MATRIX") != "1" {
		t.Skip("set AGENTSQL_B5_COORDINATOR_MATRIX=1 to run the PG14/18 coordinator matrix")
	}
	for _, test := range []struct {
		major   int
		sslmode string
	}{{major: 14, sslmode: "disable"}, {major: 18, sslmode: "require"}} {
		test := test
		t.Run("pg"+strconv.Itoa(test.major)+"-"+test.sslmode, func(t *testing.T) {
			runB5CoordinatorPostgres(t, test.major, test.sslmode)
		})
	}
}

type coordinatorMatrixAuthorizer struct{}

func (coordinatorMatrixAuthorizer) DatasourceID() string { return "matrix" }
func (coordinatorMatrixAuthorizer) AuthorizeCandidate(_ context.Context, enrollment PostgresDMLEnrollment, _ b5coordinator.StatementRequest, _ int) (B5PostgresAuthorization, error) {
	authorization := postgresDMLMatrixAuthorization(enrollment)
	return B5PostgresAuthorization{Policies: authorization.Policies, Decision: b5coordinator.DecisionAllow, PreliminaryAllowed: true, DatasourceSupported: true, PolicySnapshotDigest: authorization.PolicySnapshotDigest}, nil
}

func runB5CoordinatorPostgres(t *testing.T, major int, sslmode string) {
	t.Helper()
	unavailable, err := probeDockerAvailable()
	if unavailable {
		t.Skipf("docker daemon unavailable: %v", err)
	}
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	t.Cleanup(cancel)
	root, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "dbext", "postgres", "agentsql_binder"))
	require.NoError(t, err)
	majorString := strconv.Itoa(major)
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			FromDockerfile: testcontainers.FromDockerfile{Context: root, Dockerfile: "Dockerfile.test", Repo: "agentsql-binder-dml-test", Tag: "pg" + majorString, BuildArgs: map[string]*string{"PG_MAJOR": &majorString}, KeepImage: true},
			Env:            map[string]string{"POSTGRES_DB": "agentsql", "POSTGRES_USER": "agentsql", "POSTGRES_PASSWORD": "agentsql-password"},
			Entrypoint:     []string{"/bin/bash", "-c"},
			Cmd:            []string{`openssl req -new -x509 -nodes -days 1 -subj '/CN=localhost' -out /tmp/agentsql-server.crt -keyout /tmp/agentsql-server.key >/dev/null 2>&1 && chown postgres:postgres /tmp/agentsql-server.crt /tmp/agentsql-server.key && chmod 600 /tmp/agentsql-server.key && exec /usr/local/bin/docker-entrypoint.sh postgres -c ssl=on -c ssl_cert_file=/tmp/agentsql-server.crt -c ssl_key_file=/tmp/agentsql-server.key`},
			Labels:         map[string]string{"agentsql.b5.coordinator-matrix": "true"},
			ExposedPorts:   []string{"5432/tcp"},
			WaitingFor: wait.ForAll(
				wait.ForListeningPort("5432/tcp"),
				wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
			).WithDeadline(2 * time.Minute),
		},
		Started: true,
	})
	require.NoError(t, err)
	testcontainers.CleanupContainer(t, container)
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)
	executor := newPostgresDMLMatrixExecutor(t, ctx, host, port.Int(), sslmode)
	for _, statement := range []string{
		`CREATE SCHEMA agentsql_catalog`,
		`CREATE EXTENSION agentsql_binder WITH SCHEMA agentsql_catalog`,
		`CREATE SCHEMA s6`,
		`CREATE TABLE s6.items(id integer, value integer)`,
		`INSERT INTO s6.items VALUES (1,10),(2,20)`,
	} {
		_, err = executor.Execute(ctx, statement)
		require.NoError(t, err, statement)
	}
	if sslmode == "require" {
		var encrypted bool
		require.NoError(t, executor.pool.QueryRow(ctx, `SELECT ssl FROM pg_catalog.pg_stat_ssl WHERE pid=pg_backend_pid()`).Scan(&encrypted))
		require.True(t, encrypted)
	}
	runtime, err := NewB5PostgresRuntime(executor, coordinatorMatrixAuthorizer{})
	require.NoError(t, err)

	newCoordinator := func(audit *b5coordinator.MemoryAuditor) *b5coordinator.Coordinator {
		coordinator, newErr := b5coordinator.New(b5coordinator.Config{
			Transactions: b5coordinator.NewMemoryTransactionStore(),
			Sessions:     b5coordinator.StaticSessionGate{SessionID: "matrix-session", OwnerEpoch: 7},
			Engine:       runtime,
			Audit:        audit,
		})
		require.NoError(t, newErr)
		return coordinator
	}
	plan := func(sql ...string) b5coordinator.PlanRequest {
		statements := make([]b5coordinator.StatementRequest, len(sql))
		for index := range sql {
			statements[index] = b5coordinator.StatementRequest{OperationID: fmt.Sprintf("op-%d", index), SQL: sql[index], Reason: "coordinator matrix"}
		}
		return b5coordinator.PlanRequest{TenantID: "matrix-tenant", PrincipalID: "matrix-principal", AgentID: "matrix-agent", DatasourceID: "matrix", KeyRevision: 1, DatasourceRevision: 1, PolicyRevision: 1, Dialect: "postgres", ServerMajor: major, Isolation: "serializable", BinderABI: b5dml.BinderABI, ClosurePolicy: "closed-v1", Statements: statements, Limits: b5coordinator.DefaultResourceLimits()}
	}
	session := b5coordinator.SessionAuthorization{SessionID: "matrix-session", OwnerEpoch: 7}

	t.Run("ordered-commit", func(t *testing.T) {
		coordinator := newCoordinator(&b5coordinator.MemoryAuditor{})
		request := plan(`UPDATE s6.items SET value=value+1 WHERE id=1`, `UPDATE s6.items SET value=value+2 WHERE id=2`)
		_, err = coordinator.Begin(ctx, b5coordinator.BeginRequest{Session: session, TransactionID: "matrix-commit", RequestID: "begin-commit", Plan: request}, runtime)
		require.NoError(t, err)
		for index := range request.Statements {
			_, err = coordinator.Execute(ctx, b5coordinator.ExecuteRequest{Session: session, TransactionID: "matrix-commit", RequestID: fmt.Sprintf("execute-%d", index), OperationID: request.Statements[index].OperationID, Ordinal: index})
			require.NoError(t, err)
		}
		result, commitErr := coordinator.Commit(ctx, b5coordinator.FinishRequest{Session: session, TransactionID: "matrix-commit", RequestID: "commit"})
		require.NoError(t, commitErr)
		require.Equal(t, b5.OutcomeCommitted, result.DBOutcome)
		assertS6Values(t, ctx, executor, 11, 22)
	})

	t.Run("statement-audit-failure-rolls-back", func(t *testing.T) {
		audit := &b5coordinator.MemoryAuditor{Fail: func(event b5coordinator.AuditEvent) error {
			if event.Kind == b5coordinator.AuditStatement {
				return errors.New("injected statement audit outage")
			}
			return nil
		}}
		coordinator := newCoordinator(audit)
		request := plan(`UPDATE s6.items SET value=value+100 WHERE id=1`)
		_, err = coordinator.Begin(ctx, b5coordinator.BeginRequest{Session: session, TransactionID: "matrix-rollback", RequestID: "begin-rollback", Plan: request}, runtime)
		require.NoError(t, err)
		result, executeErr := coordinator.Execute(ctx, b5coordinator.ExecuteRequest{Session: session, TransactionID: "matrix-rollback", RequestID: "execute-rollback", OperationID: request.Statements[0].OperationID, Ordinal: 0})
		require.Error(t, executeErr)
		require.Equal(t, b5.OutcomeNotCommitted, result.DBOutcome)
		assertS6Values(t, ctx, executor, 11, 22)
	})
}

func assertS6Values(t *testing.T, ctx context.Context, executor *PostgresExecutor, first, second int) {
	t.Helper()
	rows, err := executor.pool.Query(ctx, `SELECT value FROM s6.items ORDER BY id`)
	require.NoError(t, err)
	defer rows.Close()
	values := make([]int, 0, 2)
	for rows.Next() {
		var value int
		require.NoError(t, rows.Scan(&value))
		values = append(values, value)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []int{first, second}, values)
}
