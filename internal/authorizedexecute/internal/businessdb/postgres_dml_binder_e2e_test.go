package businessdb

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/b5dml"
	"github.com/cuipengdba/agentsql/internal/b5terminal"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// TestPostgresDMLBinderMatrix is deliberately opt-in: it builds five native
// extension images and exercises TLS/plain connections plus concurrent DDL.
// It has no bearing on a runtime flag or production handler.
func TestPostgresDMLBinderMatrix(t *testing.T) {
	if os.Getenv("AGENTSQL_B5_PG_DML_MATRIX") != "1" {
		t.Skip("set AGENTSQL_B5_PG_DML_MATRIX=1 to run the PG14-18 DML binder matrix")
	}
	for major := 14; major <= 18; major++ {
		major := major
		t.Run("pg"+strconv.Itoa(major), func(t *testing.T) {
			runPostgresDMLBinderMajor(t, major)
		})
	}
}

func runPostgresDMLBinderMajor(t *testing.T, major int) {
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
			FromDockerfile: testcontainers.FromDockerfile{
				Context: root, Dockerfile: "Dockerfile.test", Repo: "agentsql-binder-dml-test", Tag: "pg" + majorString,
				BuildArgs: map[string]*string{"PG_MAJOR": &majorString}, KeepImage: true,
			},
			Env:          map[string]string{"POSTGRES_DB": "agentsql", "POSTGRES_USER": "agentsql", "POSTGRES_PASSWORD": "agentsql-password"},
			Entrypoint:   []string{"/bin/bash", "-c"},
			Cmd:          []string{`openssl req -new -x509 -nodes -days 1 -subj '/CN=localhost' -out /tmp/agentsql-server.crt -keyout /tmp/agentsql-server.key >/dev/null 2>&1 && chown postgres:postgres /tmp/agentsql-server.crt /tmp/agentsql-server.key && chmod 600 /tmp/agentsql-server.key && exec /usr/local/bin/docker-entrypoint.sh postgres -c ssl=on -c ssl_cert_file=/tmp/agentsql-server.crt -c ssl_key_file=/tmp/agentsql-server.key`},
			Labels:       map[string]string{"agentsql.b5.pg-dml-matrix": "true"},
			ExposedPorts: []string{"5432/tcp"},
			WaitingFor:   wait.ForAll(wait.ForListeningPort("5432/tcp"), wait.ForLog("database system is ready to accept connections").WithOccurrence(2)).WithDeadline(2 * time.Minute),
		},
		Started: true,
	})
	require.NoError(t, err)
	testcontainers.CleanupContainer(t, container)
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)

	plain := newPostgresDMLMatrixExecutor(t, ctx, host, port.Int(), "disable")
	tls := newPostgresDMLMatrixExecutor(t, ctx, host, port.Int(), "require")
	for _, executor := range []*PostgresExecutor{plain, tls} {
		connection, acquireErr := executor.pool.Acquire(ctx)
		require.NoError(t, acquireErr)
		var encrypted bool
		require.NoError(t, connection.QueryRow(ctx, `SELECT ssl FROM pg_catalog.pg_stat_ssl WHERE pid=pg_backend_pid()`).Scan(&encrypted))
		connection.Release()
		require.Equal(t, executor == tls, encrypted)
	}

	for _, statement := range postgresDMLMatrixSchema() {
		_, err = plain.Execute(ctx, statement)
		require.NoError(t, err, statement)
	}

	for name, executor := range map[string]*PostgresExecutor{"plain": plain, "tls": tls} {
		t.Run(name+"-legal", func(t *testing.T) {
			for _, sql := range []string{
				`INSERT INTO s5.legal_insert(id) VALUES (1)`,
				`UPDATE s5.legal_update SET value=value+1 WHERE id=1`,
				`DELETE FROM s5.legal_delete WHERE id=1`,
			} {
				runAllowedPostgresDML(t, ctx, executor, sql)
			}
		})
	}

	t.Run("exact-write-and-reference-grants", func(t *testing.T) {
		insert, err := plain.EnrollPostgresDML(ctx, `INSERT INTO s5.legal_insert(id) VALUES (2)`, "matrix", &unlimitedPostgresBudget{})
		require.NoError(t, err)
		require.Len(t, insert.Facts.Writes, 2)
		require.Equal(t, b5dml.WriteSourceImplicitNull, insert.Facts.Writes[1].Source)
		auth := postgresDMLMatrixAuthorization(insert)
		for index, grant := range auth.Policies[0].Grants {
			if grant.Element == b5dml.GrantWriteTarget && grant.Column.Attnum == 2 {
				auth.Policies[0].Grants = append(auth.Policies[0].Grants[:index], auth.Policies[0].Grants[index+1:]...)
				break
			}
		}
		tx, native := beginPostgresDMLNative(t, ctx, plain)
		_, decision, err := plain.PrepareBoundPostgresDML(ctx, native, `INSERT INTO s5.legal_insert(id) VALUES (2)`, &insert, auth, &unlimitedPostgresBudget{})
		require.Error(t, err)
		require.Equal(t, b5dml.ReasonWriteTargetGrantMissing, decision.Reason())
		require.NoError(t, tx.Rollback(ctx))

		update, err := plain.EnrollPostgresDML(ctx, `UPDATE s5.legal_update SET value=value+1 WHERE id=1`, "matrix", &unlimitedPostgresBudget{})
		require.NoError(t, err)
		auth = postgresDMLMatrixAuthorization(update)
		for index, grant := range auth.Policies[0].Grants {
			if grant.Element == b5dml.GrantReference {
				auth.Policies[0].Grants = append(auth.Policies[0].Grants[:index], auth.Policies[0].Grants[index+1:]...)
				break
			}
		}
		tx, native = beginPostgresDMLNative(t, ctx, plain)
		_, decision, err = plain.PrepareBoundPostgresDML(ctx, native, `UPDATE s5.legal_update SET value=value+1 WHERE id=1`, &update, auth, &unlimitedPostgresBudget{})
		require.Error(t, err)
		require.Equal(t, b5dml.ReasonReferenceGrantMissing, decision.Reason())
		require.NoError(t, tx.Rollback(ctx))
	})

	t.Run("closure-negative-matrix", func(t *testing.T) {
		cases := map[string]string{
			"trigger-outside-manifest": `INSERT INTO s5.has_trigger(id) VALUES (1)`,
			"foreign-key":              `INSERT INTO s5.fk_child(id) VALUES (1)`,
			"constraint":               `UPDATE s5.has_unique SET value=1 WHERE id=1`,
			"default":                  `INSERT INTO s5.has_default(id) VALUES (1)`,
			"identity":                 `INSERT INTO s5.has_identity(value) VALUES (1)`,
			"generated":                `INSERT INTO s5.has_generated(id) VALUES (1)`,
			"rule":                     `UPDATE s5.has_rule SET id=1`,
			"view":                     `UPDATE s5.target_view SET value=1 WHERE id=1`,
			"partition":                `INSERT INTO s5.part_parent VALUES (1)`,
			"rls":                      `UPDATE s5.has_rls SET id=1`,
			"udf":                      `UPDATE s5.legal_update SET value=s5.plus_one(value)`,
			"expression-index":         `UPDATE s5.has_expression_index SET value='x'`,
			"update-from":              `UPDATE s5.legal_update u SET value=d.value FROM s5.legal_delete d WHERE u.id=d.id`,
			"delete-using":             `DELETE FROM s5.legal_delete d USING s5.legal_update u WHERE d.id=u.id`,
			"insert-select":            `INSERT INTO s5.legal_insert(id,note) SELECT id,'x' FROM s5.legal_update`,
			"upsert":                   `INSERT INTO s5.has_unique(id,value) VALUES (1,2) ON CONFLICT(id) DO UPDATE SET value=excluded.value`,
			"cte":                      `WITH x AS (SELECT 1) UPDATE s5.legal_update SET value=1`,
			"writable-cte":             `WITH x AS (UPDATE s5.legal_update SET value=1 RETURNING id) DELETE FROM s5.legal_delete`,
			"returning":                `UPDATE s5.legal_update SET value=1 RETURNING id`,
			"subquery":                 `UPDATE s5.legal_update SET value=1 WHERE id IN (SELECT id FROM s5.legal_delete)`,
		}
		for name, sql := range cases {
			t.Run(name, func(t *testing.T) {
				_, err := plain.EnrollPostgresDML(ctx, sql, "matrix", &unlimitedPostgresBudget{})
				require.Error(t, err)
			})
		}
	})

	t.Run("identity-and-seal", func(t *testing.T) {
		for name, mutate := range map[string]func(context.Context, pgx.Tx, string) error{
			"search-path": func(ctx context.Context, tx pgx.Tx, _ string) error {
				_, err := tx.Exec(ctx, `SET LOCAL search_path=pg_catalog`)
				return err
			},
			"role": func(ctx context.Context, tx pgx.Tx, _ string) error {
				_, err := tx.Exec(ctx, `SET LOCAL ROLE s5_other`)
				return err
			},
			"replan": func(ctx context.Context, tx pgx.Tx, _ string) error {
				_, err := tx.Exec(ctx, `DISCARD PLANS`)
				return err
			},
			"same-name": func(ctx context.Context, tx pgx.Tx, preparedName string) error {
				if _, err := tx.Exec(ctx, `DEALLOCATE `+quoteInternalPreparedName(preparedName)); err != nil {
					return err
				}
				_, err := tx.Exec(ctx, `PREPARE `+quoteInternalPreparedName(preparedName)+` AS SELECT 1`)
				return err
			},
		} {
			t.Run(name, func(t *testing.T) {
				prepared, tx := bindAllowedPostgresDML(t, ctx, plain, `UPDATE s5.legal_update SET value=value+1 WHERE id=1`)
				err := mutate(ctx, tx, prepared.name)
				if name == "same-name" {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
					_, err = prepared.Execute(ctx, &unlimitedPostgresBudget{})
					require.Error(t, err)
				}
				_ = tx.Rollback(ctx)
			})
		}
	})

	t.Run("ordered-oid-lock-blocks-ddl", func(t *testing.T) {
		prepared, tx := bindAllowedPostgresDML(t, ctx, plain, `UPDATE s5.legal_update SET value=value+1 WHERE id=1`)
		ddl, err := plain.pool.Acquire(ctx)
		require.NoError(t, err)
		_, err = ddl.Exec(ctx, `SET lock_timeout='100ms'`)
		require.NoError(t, err)
		_, err = ddl.Exec(ctx, `ALTER TABLE s5.legal_update RENAME COLUMN value TO blocked`)
		require.Error(t, err)
		ddl.Release()
		require.NoError(t, prepared.Close(ctx))
		require.NoError(t, tx.Rollback(ctx))
	})

	t.Run("closure-drift-single-outer-retry", func(t *testing.T) {
		enrollment, err := plain.EnrollPostgresDML(ctx, `INSERT INTO s5.drift(id) VALUES (1)`, "matrix", &unlimitedPostgresBudget{})
		require.NoError(t, err)
		_, err = plain.Execute(ctx, `CREATE TRIGGER drift_trigger AFTER INSERT ON s5.drift FOR EACH ROW EXECUTE FUNCTION s5.trigger_write()`)
		require.NoError(t, err)
		tx, native := beginPostgresDMLNative(t, ctx, plain)
		_, _, err = plain.PrepareBoundPostgresDML(ctx, native, `INSERT INTO s5.drift(id) VALUES (1)`, &enrollment, postgresDMLMatrixAuthorization(enrollment), &unlimitedPostgresBudget{})
		require.Error(t, err)
		require.True(t, IsPostgresDMLClosureRace(err))
		require.NoError(t, tx.Rollback(ctx))
		_, err = plain.EnrollPostgresDML(ctx, `INSERT INTO s5.drift(id) VALUES (1)`, "matrix", &unlimitedPostgresBudget{})
		require.Error(t, err, "one outer refresh must hard-deny the newly visible closure")
		require.Equal(t, 2, PostgresDMLMaxBindAttempts)
	})

	t.Run("mysql-remains-unsupported", func(t *testing.T) {
		decision := b5dml.CheckClosure(b5dml.DialectMySQL, b5dml.ShapeSimple, nil)
		require.False(t, decision.Allowed)
		require.Equal(t, b5dml.ClosureDialectUnsupported, decision.Reason)
	})
}

func newPostgresDMLMatrixExecutor(t *testing.T, ctx context.Context, host string, port int, sslmode string) *PostgresExecutor {
	t.Helper()
	connectionURL := url.URL{Scheme: "postgres", User: url.UserPassword("agentsql", "agentsql-password"), Host: fmt.Sprintf("%s:%d", host, port), Path: "/agentsql", RawQuery: "sslmode=" + sslmode}
	config, err := pgxpool.ParseConfig(connectionURL.String())
	require.NoError(t, err)
	config.MaxConns = 8
	config.ConnConfig.RuntimeParams["statement_timeout"] = "5000"
	config.ConnConfig.BuildFrontend = newBoundedPostgresFrontend
	config.ConnConfig.OnNotice = func(*pgconn.PgConn, *pgconn.Notice) {}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	require.NoError(t, err)
	executor := &PostgresExecutor{pool: pool, maxConns: 8, timeout: 5 * time.Second, clock: wallClock{}, sessions: make(map[string]Session)}
	require.NoError(t, executor.Ping(ctx))
	t.Cleanup(func() { pool.Close() })
	return executor
}

func postgresDMLMatrixSchema() []string {
	return []string{
		`CREATE SCHEMA agentsql_catalog`, `CREATE EXTENSION agentsql_binder WITH SCHEMA agentsql_catalog`, `CREATE SCHEMA s5`, `CREATE ROLE s5_other`,
		`CREATE TABLE s5.legal_insert(id integer, note text)`, `CREATE TABLE s5.legal_update(id integer, value integer)`, `INSERT INTO s5.legal_update VALUES (1,10)`,
		`CREATE TABLE s5.legal_delete(id integer, note text)`, `INSERT INTO s5.legal_delete VALUES (1,'x')`, `CREATE TABLE s5.audit_log(id integer)`,
		`CREATE FUNCTION s5.trigger_write() RETURNS trigger LANGUAGE plpgsql AS 'BEGIN INSERT INTO s5.audit_log VALUES (NEW.id); RETURN NEW; END'`,
		`CREATE TABLE s5.has_trigger(id integer)`, `CREATE TRIGGER outside_manifest AFTER INSERT ON s5.has_trigger FOR EACH ROW EXECUTE FUNCTION s5.trigger_write()`,
		`CREATE TABLE s5.fk_parent(id integer PRIMARY KEY)`, `CREATE TABLE s5.fk_child(id integer REFERENCES s5.fk_parent(id))`,
		`CREATE TABLE s5.has_unique(id integer UNIQUE, value integer)`, `CREATE TABLE s5.has_default(id integer, value integer DEFAULT 7)`,
		`CREATE TABLE s5.has_identity(id integer GENERATED ALWAYS AS IDENTITY, value integer)`, `CREATE TABLE s5.has_generated(id integer, doubled integer GENERATED ALWAYS AS (id*2) STORED)`,
		`CREATE TABLE s5.has_rule(id integer)`, `CREATE RULE extra_write AS ON UPDATE TO s5.has_rule DO ALSO INSERT INTO s5.audit_log VALUES (NEW.id)`,
		`CREATE VIEW s5.target_view AS SELECT id,value FROM s5.legal_update`, `CREATE TABLE s5.part_parent(id integer) PARTITION BY RANGE(id)`, `CREATE TABLE s5.part_leaf PARTITION OF s5.part_parent FOR VALUES FROM (0) TO (10)`,
		`CREATE TABLE s5.has_rls(id integer)`, `ALTER TABLE s5.has_rls ENABLE ROW LEVEL SECURITY`, `CREATE FUNCTION s5.plus_one(integer) RETURNS integer LANGUAGE sql IMMUTABLE AS 'SELECT $1+1'`,
		`CREATE TABLE s5.has_expression_index(value text)`, `CREATE INDEX expression_idx ON s5.has_expression_index ((pg_catalog.lower(value)))`, `CREATE TABLE s5.drift(id integer)`,
	}
}

func runAllowedPostgresDML(t *testing.T, ctx context.Context, executor *PostgresExecutor, sql string) {
	t.Helper()
	prepared, tx := bindAllowedPostgresDML(t, ctx, executor, sql)
	tag, err := prepared.Execute(ctx, &unlimitedPostgresBudget{})
	require.NoError(t, err)
	require.NotZero(t, tag.RowsAffected())
	require.NoError(t, prepared.Close(ctx))
	require.NoError(t, tx.Rollback(ctx))
}

func bindAllowedPostgresDML(t *testing.T, ctx context.Context, executor *PostgresExecutor, sql string) (*PostgresPreparedDML, pgx.Tx) {
	t.Helper()
	enrollment, err := executor.EnrollPostgresDML(ctx, sql, "matrix", &unlimitedPostgresBudget{})
	logPostgresDBError(t, err)
	require.NoError(t, err)
	tx, native := beginPostgresDMLNative(t, ctx, executor)
	prepared, decision, err := executor.PrepareBoundPostgresDML(ctx, native, sql, &enrollment, postgresDMLMatrixAuthorization(enrollment), &unlimitedPostgresBudget{})
	if err != nil {
		_ = tx.Rollback(ctx)
	}
	require.NoError(t, err)
	require.True(t, decision.Allowed())
	return prepared, tx
}

func beginPostgresDMLNative(t *testing.T, ctx context.Context, executor *PostgresExecutor) (pgx.Tx, *PostgresDMLNativeTx) {
	t.Helper()
	tx, err := executor.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	require.NoError(t, err)
	owner := b5terminal.NewTerminalOwner(1, 1)
	machine := b5dml.NewBeginMachine(1, 1, owner)
	require.True(t, machine.PinConnection())
	require.True(t, machine.ObserveBegin(b5dml.BeginAttemptEvidence{Write: b5terminal.WriteEvidence{Phase: b5terminal.WriteFullFrame, FrameBytes: 5, BytesWritten: 5}, Reply: b5dml.BeginReplyPositiveACK, Correlation: b5terminal.CorrelationStrongCurrentOperation, ServerStatus: b5terminal.ServerReadyIdleInTransaction}))
	native, err := AttachPostgresDMLNativeTx(ctx, tx, machine)
	require.NoError(t, err)
	return tx, native
}

func postgresDMLMatrixAuthorization(enrollment PostgresDMLEnrollment) PostgresDMLAuthorization {
	grants := []b5dml.Grant{{Element: b5dml.GrantAction, Action: enrollment.Facts.Action, Relation: enrollment.Facts.Target}}
	for _, write := range enrollment.Facts.Writes {
		grant := b5dml.Grant{Element: b5dml.GrantWriteTarget, Action: enrollment.Facts.Action, Relation: write.Relation, WriteKind: write.Kind}
		if write.Kind == b5dml.WriteTargetColumn {
			grant.Column = write.Column
		}
		grants = append(grants, grant)
	}
	for _, reference := range enrollment.Facts.References {
		grants = append(grants, b5dml.Grant{Element: b5dml.GrantReference, Action: enrollment.Facts.Action, Relation: reference.Relation, Column: reference.Column, ReferenceKind: reference.Kind})
	}
	major := enrollment.Manifest.Capability.ServerMajor
	attestations := PostgresDMLBinderAttestations()
	for index := range attestations {
		candidate := attestations[index].ServerMajor
		if candidate == major {
			attestations[index].BuildHash = enrollment.Manifest.Capability.BuildHash
			attestations[index].ExtensionHash = enrollment.Manifest.Capability.ExtensionHash
			attestations[index].NodeManifestHash = enrollment.Manifest.Capability.NodeManifestHash
			attestations[index].AllowlistHash = enrollment.Manifest.Capability.AllowlistHash
		}
	}
	return PostgresDMLAuthorization{PrincipalID: "matrix-principal", DatasourceID: "matrix", Policies: []b5dml.Policy{{ID: "matrix-policy", Revision: 1, PrincipalID: "matrix-principal", DatasourceID: "matrix", Effect: b5dml.GrantAllow, Grants: grants}}, PreliminaryAllowed: true, DatasourceSupported: true, PolicySnapshotDigest: "matrix-policy-v1", PlanDigest: "matrix-plan-v1", Attestations: attestations}
}
