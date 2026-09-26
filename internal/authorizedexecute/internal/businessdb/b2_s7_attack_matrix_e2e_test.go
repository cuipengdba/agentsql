package businessdb

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const b2S7MatrixGate = "AGENTSQL_B2_S7_MATRIX"

// TestB2S7ColumnAuthorizationAttackMatrix is the opt-in, destructive red-team
// acceptance suite for B2. It deliberately runs against real PostgreSQL 14
// and 18 servers; the PG18 cell is TLS-only. Every case either proves that an
// attack is rejected or that every sensitive contributor is present in the
// immutable binder facts that the column authorizer consumes.
func TestB2S7ColumnAuthorizationAttackMatrix(t *testing.T) {
	if os.Getenv(b2S7MatrixGate) != "1" {
		t.Skip("set " + b2S7MatrixGate + "=1 to run the PG14/18 B2 S7 attack matrix")
	}
	for _, cell := range []struct {
		major int
		tls   bool
	}{{major: 14}, {major: 18, tls: true}} {
		cell := cell
		name := fmt.Sprintf("pg%d", cell.major)
		if cell.tls {
			name += "-tls"
		}
		t.Run(name, func(t *testing.T) {
			runB2S7AttackMatrix(t, cell.major, cell.tls)
		})
	}
}

func runB2S7AttackMatrix(t *testing.T, major int, tls bool) {
	t.Helper()
	unavailable, err := probeDockerAvailable()
	if unavailable {
		t.Skipf("docker daemon unavailable: %v", err)
	}
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	t.Cleanup(cancel)

	majorText := strconv.Itoa(major)
	root, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "dbext", "postgres", "agentsql_binder"))
	require.NoError(t, err)
	request := testcontainers.ContainerRequest{
		FromDockerfile: testcontainers.FromDockerfile{Context: root, Dockerfile: "Dockerfile.test",
			Repo: "agentsql-b2-s7", Tag: "pg" + majorText, BuildArgs: map[string]*string{"PG_MAJOR": &majorText}, KeepImage: true},
		Env:          map[string]string{"POSTGRES_DB": "agentsql", "POSTGRES_USER": "agentsql", "POSTGRES_PASSWORD": "agentsql-password"},
		ExposedPorts: []string{"5432/tcp"},
		Labels:       map[string]string{"agentsql.b2.s7-matrix": "true", "agentsql.pg-major": majorText},
		WaitingFor: wait.ForAll(wait.ForListeningPort("5432/tcp"),
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2)).WithDeadline(2 * time.Minute),
	}
	if tls {
		request.Entrypoint = []string{"/bin/bash", "-c"}
		request.Cmd = []string{`openssl req -new -x509 -nodes -days 1 -subj '/CN=localhost' -out /tmp/agentsql-server.crt -keyout /tmp/agentsql-server.key >/dev/null 2>&1 && chown postgres:postgres /tmp/agentsql-server.crt /tmp/agentsql-server.key && chmod 600 /tmp/agentsql-server.key && exec /usr/local/bin/docker-entrypoint.sh postgres -c ssl=on -c ssl_cert_file=/tmp/agentsql-server.crt -c ssl_key_file=/tmp/agentsql-server.key`}
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: request, Started: true})
	require.NoError(t, err)
	testcontainers.CleanupContainer(t, container)
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)
	sslmode := "disable"
	if tls {
		sslmode = "require"
	}
	executor := newPostgresDMLMatrixExecutor(t, ctx, host, port.Int(), sslmode)
	if tls {
		var encrypted bool
		require.NoError(t, executor.pool.QueryRow(ctx,
			`SELECT ssl FROM pg_catalog.pg_stat_ssl WHERE pid=pg_backend_pid()`).Scan(&encrypted))
		require.True(t, encrypted, "PG18 S7 cell must use TLS")
	}
	setupB2S7Fixture(t, ctx, executor)

	t.Run("escape-corpus", func(t *testing.T) { runB2S7EscapeCorpus(t, ctx, executor) })
	t.Run("fault-injection", func(t *testing.T) { runB2S7FaultInjection(t, ctx, executor) })
	t.Run("concurrent-ddl", func(t *testing.T) { runB2S7ConcurrentDDL(t, ctx, executor) })
	t.Run("network-and-pool", func(t *testing.T) { runB2S7NetworkAndPool(t, ctx, executor) })
	t.Run("dos-and-resources", func(t *testing.T) { runB2S7DoS(t, ctx, executor) })
	t.Run("closed-native-selector", func(t *testing.T) { runB2S7ClosedNative(t, ctx, executor, major) })
	requireS7PoolIdle(t, executor)
}

func setupB2S7Fixture(t *testing.T, ctx context.Context, executor *PostgresExecutor) {
	t.Helper()
	for _, statement := range []string{
		`CREATE SCHEMA agentsql_catalog`,
		`CREATE EXTENSION agentsql_binder WITH SCHEMA agentsql_catalog`,
		`CREATE SCHEMA s7`,
		`CREATE SCHEMA decoy`,
		`CREATE TABLE s7.accounts(id integer, allowed text, secret text)`,
		`CREATE TABLE s7.events(id integer, account_id integer, secret_note text)`,
		`CREATE TABLE decoy.accounts(id integer, allowed text, secret text)`,
		`INSERT INTO s7.accounts VALUES (1,'visible','s7-secret'),(2,'visible-2','s7-secret-2')`,
		`INSERT INTO s7.events VALUES (1,1,'event-secret')`,
		`INSERT INTO decoy.accounts VALUES (1,'decoy-visible','decoy-secret')`,
		`CREATE VIEW s7.account_view AS SELECT id,allowed,secret FROM s7.accounts`,
		`CREATE MATERIALIZED VIEW s7.account_mv AS SELECT id,allowed FROM s7.accounts WITH DATA`,
		`CREATE FUNCTION s7.eq_int(integer,integer) RETURNS boolean LANGUAGE SQL IMMUTABLE AS 'SELECT $1=$2'`,
		`CREATE OPERATOR s7.=== (LEFTARG=integer, RIGHTARG=integer, FUNCTION=s7.eq_int)`,
	} {
		_, err := executor.Execute(ctx, statement)
		require.NoError(t, err, statement)
	}
}

func runB2S7EscapeCorpus(t *testing.T, ctx context.Context, executor *PostgresExecutor) {
	t.Helper()
	deny := []struct{ name, sql string }{
		{"stacked-ddl", `SELECT a.allowed FROM s7.accounts a; DROP TABLE s7.accounts`},
		{"stacked-comment", `SeLeCt a.allowed FROM s7.accounts a /* ; hidden */; SELECT a.secret FROM s7.accounts a`},
		{"client-prepare", `PREPARE attacker AS SELECT secret FROM s7.accounts`},
		{"client-execute", `EXECUTE attacker`},
		{"select-into", `SELECT secret INTO s7.stolen FROM s7.accounts`},
		{"locking-read", `SELECT allowed FROM s7.accounts FOR UPDATE`},
		{"recursive-cte", `WITH RECURSIVE q(id) AS (SELECT 1 UNION ALL SELECT id+1 FROM q WHERE id<2) SELECT id FROM q`},
		{"modifying-cte", `WITH changed AS (DELETE FROM s7.events RETURNING secret_note) SELECT secret_note FROM changed`},
		{"whole-row", `SELECT a FROM s7.accounts a`},
		{"whole-row-function", `SELECT pg_catalog.count(a) FROM s7.accounts a`},
		{"system-ctid", `SELECT ctid FROM s7.accounts`},
		{"system-xmin", `SELECT xmin FROM s7.accounts`},
		{"system-tableoid", `SELECT tableoid FROM s7.accounts`},
		{"user-function", `SELECT s7.eq_int(a.id,1) FROM s7.accounts a`},
		{"escape-operator", `SELECT a.allowed FROM s7.accounts a WHERE a.id OPERATOR(s7.===) 1`},
		{"alias-hidden-base", `SELECT accounts.secret FROM s7.accounts AS hidden`},
		{"catalog-qualification", `SELECT pg_catalog.pg_read_file('/etc/passwd') FROM s7.accounts a`},
		{"copy-program", `COPY s7.accounts TO PROGRAM 'id'`},
		{"unicode-confusable-keyword", `ＳＥＬＥＣＴ secret FROM s7.accounts`},
	}
	for _, attack := range deny {
		attack := attack
		t.Run("deny-"+attack.name, func(t *testing.T) {
			_, err := executor.EnrollPostgresSelect(ctx, attack.sql, &unlimitedPostgresBudget{})
			require.Error(t, err, "escape reached an executable native proof")
			requireS7PoolIdle(t, executor)
		})
	}

	captured := []struct {
		name, sql, relation, column, usage string
	}{
		{"operator-predicate", `SELECT a.allowed FROM s7.accounts a WHERE a.secret='s7-secret'`, "accounts", "secret", "reference"},
		{"builtin-function", `SELECT pg_catalog.lower(a.secret) FROM s7.accounts a`, "accounts", "secret", "output"},
		{"scalar-subquery", `SELECT a.allowed FROM s7.accounts a WHERE a.id IN (SELECT e.account_id FROM s7.events e WHERE e.secret_note='event-secret')`, "events", "secret_note", "reference"},
		{"lateral", `SELECT a.allowed FROM s7.accounts a JOIN LATERAL (SELECT e.secret_note FROM s7.events e WHERE e.account_id=a.id) q ON true WHERE q.secret_note='event-secret'`, "events", "secret_note", "reference"},
		{"union", `SELECT a.allowed FROM s7.accounts a UNION SELECT b.secret FROM s7.accounts b`, "accounts", "secret", "output"},
		{"alias-shadow", `SELECT outer_a.allowed FROM s7.accounts outer_a WHERE EXISTS (SELECT inner_a.id FROM s7.accounts inner_a WHERE inner_a.secret=outer_a.secret)`, "accounts", "secret", "reference"},
		{"schema-qualified-decoy", `SELECT a.allowed FROM decoy.accounts a WHERE a.secret='decoy-secret'`, "accounts", "secret", "reference"},
	}
	for _, attack := range captured {
		attack := attack
		t.Run("capture-"+attack.name, func(t *testing.T) {
			enrollment, err := executor.EnrollPostgresSelect(ctx, attack.sql, &unlimitedPostgresBudget{})
			require.NoError(t, err)
			requireS7ManifestColumn(t, enrollment, attack.relation, attack.column, attack.usage)
			requireS7PoolIdle(t, executor)
		})
	}
}

func runB2S7FaultInjection(t *testing.T, ctx context.Context, executor *PostgresExecutor) {
	t.Helper()
	t.Run("binder-function-missing", func(t *testing.T) {
		_, err := executor.Execute(ctx, `ALTER FUNCTION agentsql_catalog.prepare(text,text) RENAME TO prepare_missing`)
		require.NoError(t, err)
		_, bindErr := executor.EnrollPostgresSelect(ctx, `SELECT a.allowed FROM s7.accounts a`, &unlimitedPostgresBudget{})
		require.Error(t, bindErr)
		_, err = executor.Execute(ctx, `ALTER FUNCTION agentsql_catalog.prepare_missing(text,text) RENAME TO prepare`)
		require.NoError(t, err)
		requireS7PoolIdle(t, executor)
	})
	t.Run("capability-malformed", func(t *testing.T) {
		_, err := executor.Execute(ctx, `ALTER FUNCTION agentsql_catalog.capabilities() RENAME TO capabilities_native`)
		require.NoError(t, err)
		_, err = executor.Execute(ctx, `CREATE FUNCTION agentsql_catalog.capabilities() RETURNS jsonb LANGUAGE SQL STABLE AS 'SELECT ''{"abi":"attacker"}''::jsonb'`)
		require.NoError(t, err)
		_, probeErr := executor.ProbePostgresBinderCapability(ctx, &unlimitedPostgresBudget{})
		requireAuthorizationReason(t, probeErr, "AUTH_BINDER_CAPABILITY_MISMATCH")
		_, err = executor.Execute(ctx, `DROP FUNCTION agentsql_catalog.capabilities()`)
		require.NoError(t, err)
		_, err = executor.Execute(ctx, `ALTER FUNCTION agentsql_catalog.capabilities_native() RENAME TO capabilities`)
		require.NoError(t, err)
		requireS7PoolIdle(t, executor)
	})
	t.Run("prepare-failure", func(t *testing.T) {
		_, err := executor.EnrollPostgresSelect(ctx, `SELECT FROM`, &unlimitedPostgresBudget{})
		require.Error(t, err)
		requireS7PoolIdle(t, executor)
	})
	t.Run("deallocate-failure", func(t *testing.T) {
		prepared, err := executor.PrepareBoundPostgresSelect(ctx, `SELECT a.allowed FROM s7.accounts a`, nil, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		_, err = prepared.Execute(ctx, 10)
		require.NoError(t, err)
		_, err = prepared.VerifyPost(ctx, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		_, err = prepared.tx.Exec(ctx, `DEALLOCATE `+quoteInternalPreparedName(prepared.name))
		require.NoError(t, err)
		require.Error(t, prepared.Close(ctx, true), "missing prepared statement at final DEALLOCATE must fail closed")
		requireS7PoolIdle(t, executor)
	})
	t.Run("catalog-digest-mismatch", func(t *testing.T) {
		enrollment, err := executor.EnrollPostgresSelect(ctx, `SELECT a.allowed FROM s7.accounts a`, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		enrollment.Fingerprint += "-tampered"
		_, err = executor.PrepareBoundPostgresSelect(ctx, `SELECT a.allowed FROM s7.accounts a`, &enrollment, &unlimitedPostgresBudget{})
		requireAuthorizationReason(t, err, "AUTH_CATALOG_RACE")
		requireS7PoolIdle(t, executor)
	})
	t.Run("relkind-mismatch", func(t *testing.T) {
		candidate, _, err := executor.discoverPostgresSelect(ctx, `SELECT a.allowed FROM s7.accounts a`, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		candidate.Relations[0].Kind = 'm'
		_, err = executor.prepareLockedPostgresSelect(ctx, `SELECT a.allowed FROM s7.accounts a`, candidate, nil, &unlimitedPostgresBudget{})
		requireAuthorizationReason(t, err, "AUTH_CATALOG_RACE")
		requireS7PoolIdle(t, executor)
	})
	t.Run("budget-failure", func(t *testing.T) {
		_, err := executor.EnrollPostgresSelect(ctx, `SELECT a.allowed FROM s7.accounts a`, &s7FailBudget{fail: "roundtrip"})
		require.Error(t, err)
		requireS7PoolIdle(t, executor)
	})
	t.Run("deadline", func(t *testing.T) {
		deadline, cancel := context.WithTimeout(ctx, time.Nanosecond)
		defer cancel()
		_, err := executor.EnrollPostgresSelect(deadline, `SELECT a.allowed FROM s7.accounts a`, &unlimitedPostgresBudget{})
		require.Error(t, err)
		requireS7PoolIdle(t, executor)
	})
}

func runB2S7ConcurrentDDL(t *testing.T, ctx context.Context, executor *PostgresExecutor) {
	t.Helper()
	t.Run("single-outer-retry-bound", func(t *testing.T) {
		require.Equal(t, 1, postgresBinderMaxRetries, "catalog drift may trigger exactly one full outer rebind")
	})
	attacks := []struct {
		name, selectSQL, ddl string
	}{
		{"alter-table", `SELECT a.allowed FROM s7.accounts a`, `ALTER TABLE s7.accounts ADD COLUMN injected integer`},
		{"replace-view", `SELECT v.allowed FROM s7.account_view v`, `CREATE OR REPLACE VIEW s7.account_view AS SELECT id,secret AS allowed,secret FROM s7.accounts`},
		{"refresh-matview", `SELECT m.allowed FROM s7.account_mv m`, `REFRESH MATERIALIZED VIEW s7.account_mv`},
		{"drop", `SELECT e.secret_note FROM s7.events e`, `DROP TABLE s7.events`},
		{"rename", `SELECT a.allowed FROM s7.accounts a`, `ALTER TABLE s7.accounts RENAME TO accounts_renamed`},
	}
	for _, attack := range attacks {
		attack := attack
		t.Run(attack.name, func(t *testing.T) {
			prepared, err := executor.PrepareBoundPostgresSelect(ctx, attack.selectSQL, nil, &unlimitedPostgresBudget{})
			require.NoError(t, err)
			connection, err := executor.pool.Acquire(ctx)
			require.NoError(t, err)
			_, err = connection.Exec(ctx, `SET lock_timeout='100ms'`)
			require.NoError(t, err)
			_, ddlErr := connection.Exec(ctx, attack.ddl)
			require.Error(t, ddlErr, "DDL crossed the authorization lock window")
			_, err = connection.Exec(ctx, `RESET lock_timeout`)
			require.NoError(t, err)
			connection.Release()
			_, err = prepared.Execute(ctx, 10)
			require.NoError(t, err)
			_, err = prepared.VerifyPost(ctx, &unlimitedPostgresBudget{})
			require.NoError(t, err)
			require.NoError(t, prepared.Close(ctx, true))
			requireS7PoolIdle(t, executor)
		})
	}
	t.Run("drop-recreate-oid-aba", func(t *testing.T) {
		_, err := executor.Execute(ctx, `CREATE TABLE s7.aba(id integer)`)
		require.NoError(t, err)
		enrollment, err := executor.EnrollPostgresSelect(ctx, `SELECT id FROM s7.aba`, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		oldOID := postgresRelationOID(enrollment.Catalog, "aba")
		_, err = executor.Execute(ctx, `DROP TABLE s7.aba`)
		require.NoError(t, err)
		_, err = executor.Execute(ctx, `CREATE TABLE s7.aba(id integer)`)
		require.NoError(t, err)
		fresh, err := executor.EnrollPostgresSelect(ctx, `SELECT id FROM s7.aba`, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		require.NotEqual(t, oldOID, postgresRelationOID(fresh.Catalog, "aba"))
		_, err = executor.PrepareBoundPostgresSelect(ctx, `SELECT id FROM s7.aba`, &enrollment, &unlimitedPostgresBudget{})
		requireAuthorizationReason(t, err, "AUTH_CATALOG_RACE")
		requireS7PoolIdle(t, executor)
	})
}

func runB2S7NetworkAndPool(t *testing.T, ctx context.Context, executor *PostgresExecutor) {
	t.Helper()
	t.Run("write-stage-one-way-partition", func(t *testing.T) {
		proxy, proxied := newS7PartitionedExecutor(t, ctx, executor)
		prepared, err := proxied.PrepareBoundPostgresSelect(ctx, `SELECT a.allowed FROM s7.accounts a`, nil, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		proxy.clientToServer.Store(true)
		operationCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		_, err = prepared.Execute(operationCtx, 10)
		cancel()
		require.Error(t, err)
		closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
		_ = prepared.Close(closeCtx, false)
		closeCancel()
		proxy.Close()
		requireS7PoolIdle(t, proxied)
	})
	t.Run("final-response-one-way-partition", func(t *testing.T) {
		proxy, proxied := newS7PartitionedExecutor(t, ctx, executor)
		prepared, err := proxied.PrepareBoundPostgresSelect(ctx, `SELECT a.allowed FROM s7.accounts a`, nil, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		_, err = prepared.Execute(ctx, 10)
		require.NoError(t, err)
		_, err = prepared.VerifyPost(ctx, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		proxy.serverToClient.Store(true)
		closeCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		err = prepared.Close(closeCtx, true)
		cancel()
		require.Error(t, err, "a lost final response must never be reported as success")
		proxy.Close()
		requireS7PoolIdle(t, proxied)
	})
	t.Run("read-stage-disconnect", func(t *testing.T) {
		prepared, err := executor.PrepareBoundPostgresSelect(ctx, `SELECT a.allowed FROM s7.accounts a`, nil, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		killer, err := executor.pool.Acquire(ctx)
		require.NoError(t, err)
		var killed bool
		require.NoError(t, killer.QueryRow(ctx, `SELECT pg_catalog.pg_terminate_backend($1)`, prepared.Manifest().BackendPID).Scan(&killed))
		require.True(t, killed)
		killer.Release()
		_, err = prepared.Execute(ctx, 10)
		require.Error(t, err)
		closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.Error(t, prepared.Close(closeCtx, false))
		requireS7PoolIdle(t, executor)
	})
	t.Run("final-ack-loss", func(t *testing.T) {
		prepared, err := executor.PrepareBoundPostgresSelect(ctx, `SELECT a.allowed FROM s7.accounts a`, nil, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		_, err = prepared.Execute(ctx, 10)
		require.NoError(t, err)
		_, err = prepared.VerifyPost(ctx, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		killer, err := executor.pool.Acquire(ctx)
		require.NoError(t, err)
		var killed bool
		require.NoError(t, killer.QueryRow(ctx, `SELECT pg_catalog.pg_terminate_backend($1)`, prepared.Manifest().BackendPID).Scan(&killed))
		require.True(t, killed)
		killer.Release()
		require.Error(t, prepared.Close(ctx, true), "lost final transaction acknowledgement cannot be reported as success")
		requireS7PoolIdle(t, executor)
	})
	t.Run("pool-exhaustion", func(t *testing.T) {
		connections := make([]interface{ Release() }, 0, executor.maxConns)
		for index := int32(0); index < executor.maxConns; index++ {
			connection, err := executor.pool.Acquire(ctx)
			require.NoError(t, err)
			connections = append(connections, connection)
		}
		acquireCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		_, err := executor.EnrollPostgresSelect(acquireCtx, `SELECT a.allowed FROM s7.accounts a`, &unlimitedPostgresBudget{})
		cancel()
		require.Error(t, err)
		for _, connection := range connections {
			connection.Release()
		}
		requireS7PoolIdle(t, executor)
	})
}

type s7PartitionProxy struct {
	listener       net.Listener
	upstream       string
	clientToServer atomic.Bool
	serverToClient atomic.Bool
	closeOnce      sync.Once
	connectionsMu  sync.Mutex
	connections    []net.Conn
}

func newS7PartitionedExecutor(t *testing.T, ctx context.Context, source *PostgresExecutor) (*s7PartitionProxy, *PostgresExecutor) {
	t.Helper()
	config := source.pool.Config().ConnConfig
	proxy := &s7PartitionProxy{upstream: net.JoinHostPort(config.Host, strconv.Itoa(int(config.Port)))}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	proxy.listener = listener
	go proxy.serve()
	t.Cleanup(proxy.Close)
	sslmode := "disable"
	if config.TLSConfig != nil {
		sslmode = "require"
	}
	return proxy, newPostgresDMLMatrixExecutor(t, ctx, "127.0.0.1", listener.Addr().(*net.TCPAddr).Port, sslmode)
}

func (proxy *s7PartitionProxy) serve() {
	for {
		client, err := proxy.listener.Accept()
		if err != nil {
			return
		}
		server, err := net.DialTimeout("tcp", proxy.upstream, time.Second)
		if err != nil {
			_ = client.Close()
			continue
		}
		proxy.connectionsMu.Lock()
		proxy.connections = append(proxy.connections, client, server)
		proxy.connectionsMu.Unlock()
		go proxy.forward(server, client, &proxy.clientToServer)
		go proxy.forward(client, server, &proxy.serverToClient)
	}
}

func (proxy *s7PartitionProxy) forward(destination, source net.Conn, blocked *atomic.Bool) {
	buffer := make([]byte, 32*1024)
	for {
		count, readErr := source.Read(buffer)
		if count > 0 && !blocked.Load() {
			if _, writeErr := destination.Write(buffer[:count]); writeErr != nil {
				_ = destination.Close()
				_ = source.Close()
				return
			}
		}
		if readErr != nil {
			_ = destination.Close()
			_ = source.Close()
			return
		}
	}
}

func (proxy *s7PartitionProxy) Close() {
	proxy.closeOnce.Do(func() {
		_ = proxy.listener.Close()
		proxy.connectionsMu.Lock()
		defer proxy.connectionsMu.Unlock()
		for _, connection := range proxy.connections {
			_ = connection.Close()
		}
	})
}

func runB2S7DoS(t *testing.T, ctx context.Context, executor *PostgresExecutor) {
	t.Helper()
	t.Run("wide-in-budget", func(t *testing.T) {
		values := make([]string, 2_000)
		for index := range values {
			values[index] = strconv.Itoa(index)
		}
		sql := `SELECT a.allowed FROM s7.accounts a WHERE a.id IN (` + strings.Join(values, ",") + `)`
		_, err := executor.EnrollPostgresSelect(ctx, sql, &s7FailBudget{fail: "nodes", threshold: 500})
		require.Error(t, err)
		requireS7PoolIdle(t, executor)
	})
	t.Run("long-identifier", func(t *testing.T) {
		_, err := executor.EnrollPostgresSelect(ctx, `SELECT a.`+strings.Repeat("x", 70_000)+` FROM s7.accounts a`, &s7FailBudget{fail: "binder_bytes", threshold: 4_096})
		require.Error(t, err)
		requireS7PoolIdle(t, executor)
	})
	t.Run("large-column-metadata", func(t *testing.T) {
		columns := make([]string, 300)
		for index := range columns {
			columns[index] = fmt.Sprintf("c%d integer", index)
		}
		_, err := executor.Execute(ctx, `CREATE TABLE s7.wide(`+strings.Join(columns, ",")+`)`)
		require.NoError(t, err)
		_, err = executor.EnrollPostgresSelect(ctx, `SELECT * FROM s7.wide`, &s7FailBudget{fail: "columns", threshold: 128})
		require.Error(t, err)
		requireS7PoolIdle(t, executor)
	})
	t.Run("batch-concurrency", func(t *testing.T) {
		const workers = 24
		var group sync.WaitGroup
		errorsFound := make(chan error, workers)
		for index := 0; index < workers; index++ {
			group.Add(1)
			go func() {
				defer group.Done()
				workerCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()
				_, err := executor.EnrollPostgresSelect(workerCtx, `SELECT a.allowed FROM s7.accounts a WHERE a.id=1`, &unlimitedPostgresBudget{})
				errorsFound <- err
			}()
		}
		group.Wait()
		close(errorsFound)
		for err := range errorsFound {
			require.NoError(t, err)
		}
		requireS7PoolIdle(t, executor)
	})
}

func runB2S7ClosedNative(t *testing.T, ctx context.Context, executor *PostgresExecutor, major int) {
	t.Helper()
	sharedReject := []struct{ name, sql string }{
		{"multi-row-insert", `INSERT INTO s7.events(id,account_id,secret_note) VALUES (10,1,'a'),(11,1,'b')`},
		{"insert-select", `INSERT INTO s7.events(id,account_id) SELECT id,id FROM s7.accounts`},
		{"insert-returning", `INSERT INTO s7.events(id,account_id) VALUES (12,1) RETURNING id`},
		{"upsert", `INSERT INTO s7.events(id,account_id) VALUES (12,1) ON CONFLICT DO NOTHING`},
		{"update-from", `UPDATE s7.events e SET secret_note=a.secret FROM s7.accounts a WHERE e.account_id=a.id`},
		{"delete-using", `DELETE FROM s7.events e USING s7.accounts a WHERE e.account_id=a.id`},
		{"dml-cte", `WITH q AS (SELECT id FROM s7.accounts) DELETE FROM s7.events WHERE account_id=1`},
		{"stacked-dml", `DELETE FROM s7.events WHERE id=1; DROP TABLE s7.accounts`},
	}
	for _, attack := range sharedReject {
		attack := attack
		t.Run(attack.name, func(t *testing.T) {
			request := BindRequest{RawSQL: attack.sql, Identity: SemanticIdentity{DatasourceIdentity: "s7-closed-native"}}
			closed, closedErr := executor.BindClosedDML(ctx, request, &unlimitedPostgresBudget{})
			if closed != nil {
				_ = closed.Close(ctx)
			}
			require.Error(t, closedErr, "closed mode widened beyond native rejection")
			_, nativeErr := executor.EnrollPostgresDML(ctx, attack.sql, "s7-closed-native", &unlimitedPostgresBudget{})
			require.Error(t, nativeErr, "native boundary unexpectedly accepted attack")
			class, _, _ := ClassifyEasyDeployClosedSyntax(attack.sql)
			decision, selectErr := NewBinderModeSelector(NewNativeHealthRegistry()).Select(BinderSelectionRequest{
				DatasourceIdentity: "s7-closed-native", RequestDigest: EasyDeployRequestDigest(attack.sql),
				StatementClass: class, ClosedDisposition: ClosedRequestMustReject, Provider: postgresProviderSelfManaged,
				Handshake: healthySelectorHandshake(t, major, 1),
			})
			require.Error(t, selectErr)
			require.True(t, decision.Rejected)
			requireS7PoolIdle(t, executor)
		})
	}

	t.Run("native-required-routing", func(t *testing.T) {
		sql := `SELECT pg_catalog.count(a.id) FROM s7.accounts a`
		class, disposition, _ := ClassifyEasyDeployClosedSyntax(sql)
		require.Equal(t, ClosedRequestNativeRequired, disposition)
		decision, err := NewBinderModeSelector(NewNativeHealthRegistry()).Select(BinderSelectionRequest{
			DatasourceIdentity: "s7-selector", RequestDigest: EasyDeployRequestDigest(sql), StatementClass: class,
			ClosedDisposition: disposition, Provider: postgresProviderSelfManaged, Handshake: healthySelectorHandshake(t, major, 1),
		})
		require.NoError(t, err)
		require.Equal(t, BinderModeNativeCV1, decision.Mode)
	})
	t.Run("native-absent-fallback-is-marked", func(t *testing.T) {
		handshake := healthySelectorHandshake(t, major, 1)
		handshake.NativeFilesAvailable = false
		handshake.NativeInstalled = false
		handshake.NativeHealth = BinderCodeModeRequired
		handshake.Native = CapabilityAttestation{Schema: BinderProofSchemaID, SchemaVersion: BinderProofSchemaVersion, Mode: BinderModeNativeCV1}
		decision, err := NewBinderModeSelector(NewNativeHealthRegistry()).Select(BinderSelectionRequest{
			DatasourceIdentity: "s7-no-native", RequestDigest: "sha256:s7", StatementClass: BinderStatementSelect,
			ClosedDisposition: ClosedRequestNativeRequired, Provider: postgresProviderSelfManaged, Handshake: handshake,
		})
		require.NoError(t, err)
		require.Equal(t, BinderModeCatalogClosedV1, decision.Mode)
		require.Equal(t, ModeSelectionNativeAbsent, decision.Reason)
	})
}

func requireS7ManifestColumn(t *testing.T, enrollment PostgresEnrollment, relationName, columnName, usage string) {
	t.Helper()
	columns := make(map[string]PostgresColumnIdentity)
	for _, column := range enrollment.Catalog.Columns {
		columns[closedColumnKey(column.RelationOID, column.Attnum)] = column
	}
	for _, use := range enrollment.Manifest.Columns {
		column, ok := columns[closedColumnKey(use.RelationOID, use.Attnum)]
		if !ok || column.Name != columnName || use.Usage != usage {
			continue
		}
		for _, relation := range enrollment.Catalog.Relations {
			if relation.OID == use.RelationOID && relation.Name == relationName {
				return
			}
		}
	}
	t.Fatalf("missing %s use for %s.%s", usage, relationName, columnName)
}

func requireS7PoolIdle(t *testing.T, executor *PostgresExecutor) {
	t.Helper()
	require.Eventually(t, func() bool {
		return executor.pool.Stat().AcquiredConns() == 0
	}, 3*time.Second, 10*time.Millisecond, "S7 path leaked a PostgreSQL connection")
	executor.resourcesMu.Lock()
	defer executor.resourcesMu.Unlock()
	require.Empty(t, executor.resources, "S7 path leaked a prepared resource")
}

type s7FailBudget struct {
	fail      string
	threshold int
	used      int
}

func (budget *s7FailBudget) charge(kind string, amount int) error {
	if kind != budget.fail {
		return nil
	}
	budget.used += amount
	if budget.threshold == 0 || budget.used > budget.threshold {
		return NewCapabilityFailure("AUTH_RESOURCE_LIMIT")
	}
	return nil
}

func (b *s7FailBudget) ChargeRelations(n int) error         { return b.charge("relations", n) }
func (b *s7FailBudget) CheckViewDepth(n int) error          { return b.charge("view_depth", n) }
func (b *s7FailBudget) ChargeCatalogRoundTrips(n int) error { return b.charge("roundtrip", n) }
func (b *s7FailBudget) ChargeDefinitionBytes(n int) error   { return b.charge("definition", n) }
func (b *s7FailBudget) ChargeBinderBytes(n int) error       { return b.charge("binder_bytes", n) }
func (b *s7FailBudget) ChargeCatalogBytes(n int) error      { return b.charge("catalog_bytes", n) }
func (b *s7FailBudget) ChargeCatalogRows(n int) error       { return b.charge("catalog_rows", n) }
func (b *s7FailBudget) ChargeColumnMetadata(n int) error    { return b.charge("columns", n) }
func (b *s7FailBudget) ChargeNodes(n int) error             { return b.charge("nodes", n) }
func (b *s7FailBudget) ChargeEdges(n int) error             { return b.charge("edges", n) }
func (b *s7FailBudget) ChargePaths(n int) error             { return b.charge("paths", n) }
func (b *s7FailBudget) ChargeWork(n int) error              { return b.charge("work", n) }

var _ PostgresCatalogBudget = (*s7FailBudget)(nil)
