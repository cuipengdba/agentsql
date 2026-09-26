package businessdb

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/lockrank"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestPostgresBinderS3Matrix(t *testing.T) {
	for _, major := range []string{"14", "15", "16", "17", "18"} {
		major := major
		t.Run("pg"+major, func(t *testing.T) {
			runPostgresBinderS3Scenarios(t, major)
		})
	}
}

func runPostgresBinderS3Scenarios(t *testing.T, major string) {
	t.Helper()
	ctx := dockerTestContext(t)
	root, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "dbext", "postgres", "agentsql_binder"))
	require.NoError(t, err)
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			FromDockerfile: testcontainers.FromDockerfile{
				Context: root, Dockerfile: "Dockerfile.test", Repo: "agentsql-binder-test", Tag: "pg" + major,
				BuildArgs: map[string]*string{"PG_MAJOR": &major}, KeepImage: true,
			},
			Env: map[string]string{
				"POSTGRES_DB": "agentsql", "POSTGRES_USER": "agentsql", "POSTGRES_PASSWORD": "agentsql-password",
			},
			ExposedPorts: []string{"5432/tcp"},
			WaitingFor: wait.ForAll(
				wait.ForListeningPort("5432/tcp"),
				wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
			).WithDeadline(90 * time.Second),
		},
		Started: true,
	})
	require.NoError(t, err)
	testcontainers.CleanupContainer(t, container)

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)
	executor, err := NewPostgresExecutor(ctx, model.Datasource{
		ID: "s3-pg-" + major, DBType: "postgres", Host: host, Port: port.Int(),
		Database: "agentsql", Username: "agentsql", ConnLimit: 8, StmtTimeoutMS: 5_000,
	}, "agentsql-password", false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, executor.Close()) })

	for _, statement := range []string{
		`CREATE SCHEMA agentsql_catalog`,
		`CREATE EXTENSION agentsql_binder WITH SCHEMA agentsql_catalog`,
		`CREATE ROLE s3_other`,
		`CREATE ROLE s3_regular LOGIN PASSWORD 'regular-password' NOSUPERUSER NOCREATEDB NOCREATEROLE`,
		`CREATE SCHEMA s3`,
		`CREATE TABLE s3.base_a(id integer PRIMARY KEY, secret text)`,
		`CREATE TABLE s3.base_b(id integer, note text)`,
		`CREATE TABLE s3.s4_aba(id integer, note text)`,
		`CREATE UNLOGGED TABLE s3.unlogged_table(id integer)`,
		`CREATE EXTENSION postgres_fdw`,
		`CREATE SERVER s3_loopback FOREIGN DATA WRAPPER postgres_fdw OPTIONS (host '127.0.0.1', dbname 'agentsql')`,
		`CREATE FOREIGN TABLE s3.foreign_table(id integer) SERVER s3_loopback OPTIONS (schema_name 's3', table_name 'base_a')`,
		`CREATE VIEW s3.v_ab AS SELECT a.id,a.secret,b.note FROM s3.base_a a JOIN s3.base_b b USING(id)`,
		`CREATE VIEW s3.v2 AS SELECT id,secret FROM s3.v_ab`,
		`INSERT INTO s3.base_a VALUES (1,'alpha')`,
		`INSERT INTO s3.base_b VALUES (1,'note')`,
		`GRANT USAGE ON SCHEMA agentsql_catalog,s3 TO s3_regular`,
		`GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA agentsql_catalog TO s3_regular`,
		`GRANT SELECT ON ALL TABLES IN SCHEMA s3 TO s3_regular`,
	} {
		_, err = executor.Execute(ctx, statement)
		require.NoError(t, err, statement)
	}

	budget := &unlimitedPostgresBudget{}
	enrollment, err := executor.EnrollPostgresSelect(ctx, `SELECT id,secret FROM s3.v2 WHERE id > 0`, budget)
	if err != nil {
		diagnosePostgresBinder(t, ctx, executor, `SELECT id,secret FROM s3.v2 WHERE id > 0`)
	}
	require.NoError(t, err, "%#v", err)
	require.Equal(t, 4, len(enrollment.Catalog.Relations))
	require.NotEmpty(t, enrollment.Catalog.Dependencies)
	requirePostgresRelationIdentity(t, enrollment.Catalog, "base_a", 'r')
	requirePostgresRelationIdentity(t, enrollment.Catalog, "base_b", 'r')
	requirePostgresRelationIdentity(t, enrollment.Catalog, "v_ab", 'v')
	requirePostgresRelationIdentity(t, enrollment.Catalog, "v2", 'v')
	requirePostgresColumnLineage(t, enrollment, "base_a", "secret")
	requirePostgresColumnLineage(t, enrollment, "v_ab", "secret")
	requirePostgresColumnLineage(t, enrollment, "v2", "secret")
	requirePostgresACLInputs(t, enrollment.Manifest, enrollment.Catalog)
	require.NotEmpty(t, enrollment.Fingerprint)
	prepared, err := executor.PrepareBoundPostgresSelect(ctx, `SELECT id,secret FROM s3.v2 WHERE id > 0`, &enrollment, budget)
	logPostgresDBError(t, err)
	require.NoError(t, err)
	result, err := prepared.Execute(ctx, 10)
	require.NoError(t, err)
	require.Len(t, result.Rows, 1)
	fpost, err := prepared.VerifyPost(ctx, budget)
	require.NoError(t, err)
	require.Equal(t, prepared.Fpre().Fingerprint, fpost.Fingerprint)
	require.NoError(t, prepared.Close(ctx, true))

	t.Run("base-and-using-lineage", func(t *testing.T) {
		base, err := executor.EnrollPostgresSelect(ctx, `SELECT id,secret FROM s3.base_a`, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		require.Len(t, base.Catalog.Relations, 1)
		using, err := executor.EnrollPostgresSelect(ctx, `SELECT id FROM s3.base_a JOIN s3.base_b USING(id)`, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		requirePostgresColumnLineage(t, using, "base_a", "id")
		requirePostgresColumnLineage(t, using, "base_b", "id")
		aOID := postgresRelationOID(using.Catalog, "base_a")
		bOID := postgresRelationOID(using.Catalog, "base_b")
		require.NotZero(t, aOID)
		require.NotZero(t, bOID)
	})

	t.Run("control-before-business-lock-rank", func(t *testing.T) {
		tracked := lockrank.WithTracker(ctx)
		control, err := lockrank.Acquire(tracked, lockrank.Control)
		require.NoError(t, err)
		_, err = executor.EnrollPostgresSelect(tracked, `SELECT id FROM s3.base_a`, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		control.Release()
		business, err := lockrank.Acquire(tracked, lockrank.Business)
		require.NoError(t, err)
		_, err = lockrank.Acquire(tracked, lockrank.Control)
		require.ErrorIs(t, err, lockrank.ErrReverseOrder)
		business.Release()
	})

	runPostgresShapeGateScenarios(t, ctx, executor)
	runPostgresMatviewS3mScenarios(t, ctx, executor)
	runPostgresImplicitObjectScenarios(t, ctx, executor)
	runPostgresExactAllowlistScenarios(t, ctx, executor)
	runPostgresPreparedIdentityScenarios(t, ctx, executor)
	runPostgresCatalogRaceScenarios(t, ctx, executor)
	runClosedDMLCatalogRaceScenarios(t, ctx, executor, "s4-race-pg-"+major)
	_, err = executor.Execute(ctx, `GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA s3 TO s3_regular`)
	require.NoError(t, err)
	regular, err := NewPostgresExecutor(ctx, model.Datasource{ID: "s3-regular-pg-" + major, DBType: "postgres", Host: host,
		Port: port.Int(), Database: "agentsql", Username: "s3_regular", ConnLimit: 8, StmtTimeoutMS: 5_000}, "regular-password", false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, regular.Close()) })
	for _, role := range []struct {
		name     string
		executor *PostgresExecutor
	}{{"super", executor}, {"regular", regular}} {
		t.Run("closed-select-"+role.name, func(t *testing.T) {
			runClosedSelectDifferentialScenarios(t, ctx, role.executor, "s3-differential-pg-"+major+"-"+role.name)
		})
		t.Run("closed-dml-"+role.name, func(t *testing.T) {
			runClosedDMLDifferentialScenarios(t, ctx, role.executor, "s4-differential-pg-"+major+"-"+role.name)
		})
	}
	requirePostgresExecutorClosesPromptly(t, regular)
	requirePostgresExecutorClosesPromptly(t, executor)
}

func requirePostgresExecutorClosesPromptly(t *testing.T, executor *PostgresExecutor) {
	t.Helper()
	closed := make(chan error, 1)
	go func() { closed <- executor.Close() }()
	select {
	case err := <-closed:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("PostgreSQL executor Close exceeded 3 seconds")
	}
}

func runPostgresShapeGateScenarios(t *testing.T, ctx context.Context, executor *PostgresExecutor) {
	t.Helper()
	for _, statement := range []string{
		`CREATE TABLE s3.inh_parent(id integer)`,
		`CREATE TABLE s3.inh_child(extra text) INHERITS (s3.inh_parent)`,
		`CREATE TABLE s3.part_parent(id integer) PARTITION BY RANGE(id)`,
		`CREATE TABLE s3.part_leaf PARTITION OF s3.part_parent FOR VALUES FROM (0) TO (10)`,
		`CREATE TYPE s3.typed_row AS (id integer)`,
		`CREATE TABLE s3.typed OF s3.typed_row`,
	} {
		_, err := executor.Execute(ctx, statement)
		require.NoError(t, err, statement)
	}
	for name, query := range map[string]string{
		"inherits-parent-only": `SELECT id FROM ONLY s3.inh_parent`,
		"inherits-child-only":  `SELECT id FROM ONLY s3.inh_child`,
		"partitioned-only":     `SELECT id FROM ONLY s3.part_parent`,
		"partition-leaf-only":  `SELECT id FROM ONLY s3.part_leaf`,
		"typed-table":          `SELECT id FROM s3.typed`,
	} {
		t.Run("shape-"+name, func(t *testing.T) {
			_, err := executor.EnrollPostgresSelect(ctx, query, &unlimitedPostgresBudget{})
			require.Error(t, err)
		})
	}
	t.Run("select-only", func(t *testing.T) {
		_, err := executor.EnrollPostgresSelect(ctx, `UPDATE s3.base_a SET secret='blocked'`, &unlimitedPostgresBudget{})
		require.Error(t, err)
	})
	for name, query := range map[string]string{
		"whole-row":     `SELECT base_a FROM s3.base_a`,
		"whole-row-agg": `SELECT pg_catalog.count(base_a) FROM s3.base_a`,
		"record":        `SELECT ROW(id,secret) FROM s3.base_a`,
		"ctid":          `SELECT ctid FROM s3.base_a`,
		"xmin":          `SELECT xmin FROM s3.base_a`,
		"tableoid":      `SELECT tableoid FROM s3.base_a`,
	} {
		t.Run("column-"+name, func(t *testing.T) {
			_, err := executor.EnrollPostgresSelect(ctx, query, &unlimitedPostgresBudget{})
			requireAuthorizationReason(t, err, "AUTH_COLUMN_SHAPE_UNSUPPORTED")
		})
	}
}

func runPostgresMatviewS3mScenarios(t *testing.T, ctx context.Context, executor *PostgresExecutor) {
	t.Helper()
	for _, statement := range []string{
		`CREATE MATERIALIZED VIEW s3.mv_typed (entity_id,secret_alias,score,note_count) AS
SELECT v.id,v.secret,v.id+b.id,pg_catalog.count(b.note)
FROM s3.v2 v JOIN s3.base_b b ON b.id=v.id
GROUP BY v.id,v.secret,b.id WITH DATA`,
		`CREATE UNIQUE INDEX mv_typed_unique ON s3.mv_typed(entity_id)`,
		`CREATE MATERIALIZED VIEW s3.mv_outer (outer_id,outer_secret,outer_score) AS
SELECT entity_id,secret_alias,score FROM s3.mv_typed WITH DATA`,
		`CREATE MATERIALIZED VIEW s3.mv_no_data (aliased_id,aliased_secret) AS SELECT id,secret FROM s3.v2 WITH NO DATA`,
		`CREATE MATERIALIZED VIEW s3.mv AS SELECT id FROM s3.base_a WITH NO DATA`,
		`CREATE VIEW s3.v_on_mv AS SELECT outer_id,outer_secret FROM s3.mv_outer`,
		`CREATE MATERIALIZED VIEW s3.mv_aba AS SELECT id FROM s3.base_a WITH DATA`,
	} {
		_, err := executor.Execute(ctx, statement)
		require.NoError(t, err, statement)
	}

	t.Run("typed-lineage-alias-expression-aggregate", func(t *testing.T) {
		capability, err := executor.ProbePostgresBinderCapability(ctx, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		require.True(t, capability.Matview)
		require.Equal(t, "agentsql-binder-4.2", capability.ABI)
		require.Equal(t, "0.4-s3m", capability.ExtensionVersion)

		enrollment, err := executor.EnrollPostgresSelect(ctx,
			`SELECT outer_id,outer_secret,outer_score FROM s3.mv_outer WHERE outer_id > 0`, &unlimitedPostgresBudget{})
		if err != nil {
			logPostgresDBError(t, err)
			diagnosePostgresBinder(t, ctx, executor, `SELECT outer_id,outer_secret,outer_score FROM s3.mv_outer WHERE outer_id > 0`)
		}
		require.NoError(t, err)
		for _, relation := range []struct {
			name string
			kind byte
		}{{"mv_outer", 'm'}, {"mv_typed", 'm'}, {"v2", 'v'}, {"v_ab", 'v'}, {"base_a", 'r'}, {"base_b", 'r'}} {
			requirePostgresRelationIdentity(t, enrollment.Catalog, relation.name, relation.kind)
		}
		for _, column := range []struct{ relation, column string }{
			{"mv_outer", "outer_secret"}, {"mv_typed", "secret_alias"}, {"v2", "secret"},
			{"v_ab", "secret"}, {"base_a", "secret"}, {"base_b", "id"},
		} {
			requirePostgresColumnLineage(t, enrollment, column.relation, column.column)
		}
		require.True(t, postgresManifestHasRelationKind(enrollment.Manifest, 'm'))

		aggregate, err := executor.EnrollPostgresSelect(ctx, `SELECT note_count FROM s3.mv_typed`, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		requirePostgresColumnLineage(t, aggregate, "base_b", "note")

		prepared, err := executor.PrepareBoundPostgresSelect(ctx,
			`SELECT outer_id,outer_secret,outer_score FROM s3.mv_outer WHERE outer_id > 0`, &enrollment, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		result, err := prepared.Execute(ctx, 10)
		require.NoError(t, err)
		require.Len(t, result.Rows, 1)
		_, err = prepared.VerifyPost(ctx, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		require.NoError(t, prepared.Close(ctx, true))
	})

	t.Run("with-no-data-binds-without-reading", func(t *testing.T) {
		enrollment, err := executor.EnrollPostgresSelect(ctx, `SELECT aliased_secret FROM s3.mv_no_data`, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		requirePostgresRelationIdentity(t, enrollment.Catalog, "mv_no_data", 'm')
		requirePostgresColumnLineage(t, enrollment, "base_a", "secret")
	})

	t.Run("ordinary-view-on-matview", func(t *testing.T) {
		enrollment, err := executor.EnrollPostgresSelect(ctx, `SELECT outer_secret FROM s3.v_on_mv`, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		requirePostgresRelationIdentity(t, enrollment.Catalog, "v_on_mv", 'v')
		requirePostgresRelationIdentity(t, enrollment.Catalog, "mv_outer", 'm')
		requirePostgresColumnLineage(t, enrollment, "base_a", "secret")
	})

	t.Run("refresh-and-concurrent-refresh-rebind", func(t *testing.T) {
		_, err := executor.EnrollPostgresSelect(ctx, `SELECT secret_alias FROM s3.mv_typed`, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		_, err = executor.Execute(ctx, `REFRESH MATERIALIZED VIEW s3.mv_typed`)
		require.NoError(t, err)
		_, err = executor.Execute(ctx, `REFRESH MATERIALIZED VIEW CONCURRENTLY s3.mv_typed`)
		require.NoError(t, err)
		rebound, err := executor.EnrollPostgresSelect(ctx, `SELECT secret_alias FROM s3.mv_typed`, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		prepared, err := executor.PrepareBoundPostgresSelect(ctx, `SELECT secret_alias FROM s3.mv_typed`, &rebound, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		_, err = prepared.Execute(ctx, 10)
		require.NoError(t, err)
		_, err = prepared.VerifyPost(ctx, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		require.NoError(t, prepared.Close(ctx, true))
	})

	t.Run("relkind-locks-block-refresh-and-dependency-ddl", func(t *testing.T) {
		prepared, err := executor.PrepareBoundPostgresSelect(ctx,
			`SELECT outer_secret FROM s3.mv_outer`, nil, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		connection, err := executor.pool.Acquire(ctx)
		require.NoError(t, err)
		defer connection.Release()
		_, err = connection.Exec(ctx, `SET lock_timeout='100ms'`)
		require.NoError(t, err)
		_, err = connection.Exec(ctx, `REFRESH MATERIALIZED VIEW s3.mv_outer`)
		if err == nil {
			require.NoError(t, prepared.Close(ctx, false))
			t.Fatal("REFRESH MATERIALIZED VIEW bypassed the prepared matview lock")
		}
		_, err = connection.Exec(ctx, `ALTER TABLE s3.base_a ADD COLUMN s3m_should_block integer`)
		if err == nil {
			_, cleanupErr := connection.Exec(ctx, `ALTER TABLE s3.base_a DROP COLUMN s3m_should_block`)
			require.NoError(t, cleanupErr)
			require.NoError(t, prepared.Close(ctx, false))
			t.Fatal("dependency DDL bypassed the prepared closure lock")
		}
		_, err = prepared.Execute(ctx, 10)
		require.NoError(t, err)
		_, err = prepared.VerifyPost(ctx, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		require.NoError(t, prepared.Close(ctx, true))
	})

	t.Run("drop-recreate-invalidates-enrollment", func(t *testing.T) {
		old, err := executor.EnrollPostgresSelect(ctx, `SELECT id FROM s3.mv_aba`, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		oldOID := postgresRelationOID(old.Catalog, "mv_aba")
		_, err = executor.Execute(ctx, `DROP MATERIALIZED VIEW s3.mv_aba`)
		require.NoError(t, err)
		_, err = executor.Execute(ctx, `CREATE MATERIALIZED VIEW s3.mv_aba AS SELECT id FROM s3.base_a WITH DATA`)
		require.NoError(t, err)
		rebound, err := executor.EnrollPostgresSelect(ctx, `SELECT id FROM s3.mv_aba`, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		require.NotEqual(t, oldOID, postgresRelationOID(rebound.Catalog, "mv_aba"))
		_, err = executor.PrepareBoundPostgresSelect(ctx, `SELECT id FROM s3.mv_aba`, &old, &unlimitedPostgresBudget{})
		requireAuthorizationReason(t, err, "AUTH_CATALOG_RACE")
		prepared, err := executor.PrepareBoundPostgresSelect(ctx, `SELECT id FROM s3.mv_aba`, &rebound, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		require.NoError(t, prepared.Close(ctx, false))
	})

	t.Run("closed-mode-fails-closed", func(t *testing.T) {
		_, err := executor.BindClosedSelect(ctx, BindRequest{RawSQL: `SELECT outer_secret FROM s3.mv_outer`,
			Identity: SemanticIdentity{DatasourceIdentity: "s3m-closed"}}, &unlimitedPostgresBudget{})
		requireAuthorizationReason(t, err, "AUTH_RELATION_SHAPE_UNSUPPORTED")
	})

	runPostgresMatviewEscapeScenarios(t, ctx, executor)
}

func runPostgresMatviewEscapeScenarios(t *testing.T, ctx context.Context, executor *PostgresExecutor) {
	t.Helper()
	for _, statement := range []string{
		`CREATE FUNCTION s3.s3m_security_definer(integer) RETURNS integer LANGUAGE sql IMMUTABLE SECURITY DEFINER AS 'SELECT $1'`,
		`CREATE MATERIALIZED VIEW s3.mv_security_definer AS SELECT s3.s3m_security_definer(id) AS id FROM s3.base_a WITH DATA`,
		`CREATE TABLE s3.mv_trigger_base(id integer)`,
		`CREATE MATERIALIZED VIEW s3.mv_trigger AS SELECT id FROM s3.mv_trigger_base WITH DATA`,
		`CREATE FUNCTION s3.s3m_trigger() RETURNS trigger LANGUAGE plpgsql AS 'BEGIN RETURN NEW; END'`,
		`CREATE TRIGGER s3m_trigger BEFORE INSERT ON s3.mv_trigger_base FOR EACH ROW EXECUTE FUNCTION s3.s3m_trigger()`,
		`CREATE TABLE s3.mv_rule_base(id integer)`,
		`CREATE MATERIALIZED VIEW s3.mv_rule AS SELECT id FROM s3.mv_rule_base WITH DATA`,
		`CREATE RULE s3m_rule AS ON UPDATE TO s3.mv_rule_base DO ALSO NOTHING`,
		`CREATE VIEW s3.v_mv_escape AS SELECT outer_id FROM s3.mv_outer`,
		`CREATE FUNCTION s3.s3m_view_trigger() RETURNS trigger LANGUAGE plpgsql AS 'BEGIN RETURN NEW; END'`,
		`CREATE TRIGGER s3m_view_trigger INSTEAD OF INSERT ON s3.v_mv_escape FOR EACH ROW EXECUTE FUNCTION s3.s3m_view_trigger()`,
	} {
		_, err := executor.Execute(ctx, statement)
		require.NoError(t, err, statement)
	}
	for name, query := range map[string]string{
		"security-definer":     `SELECT id FROM s3.mv_security_definer`,
		"dependency-trigger":   `SELECT id FROM s3.mv_trigger`,
		"dependency-rule":      `SELECT id FROM s3.mv_rule`,
		"view-on-matview-hook": `SELECT outer_id FROM s3.v_mv_escape`,
	} {
		t.Run("escape-"+name, func(t *testing.T) {
			_, err := executor.EnrollPostgresSelect(ctx, query, &unlimitedPostgresBudget{})
			require.Error(t, err)
		})
	}
}

func postgresManifestHasRelationKind(manifest PostgresPreparedManifest, kind byte) bool {
	for _, relation := range manifest.Relations {
		if relation.Kind == kind {
			return true
		}
	}
	return false
}

func runPostgresImplicitObjectScenarios(t *testing.T, ctx context.Context, executor *PostgresExecutor) {
	t.Helper()
	for _, statement := range []string{
		`CREATE TABLE s3.has_default(id integer DEFAULT 1)`,
		`CREATE TABLE s3.has_generated(id integer, doubled integer GENERATED ALWAYS AS (id*2) STORED)`,
		`CREATE TABLE s3.has_trigger(id integer)`,
		`CREATE FUNCTION s3.trigger_fn() RETURNS trigger LANGUAGE plpgsql AS 'BEGIN RETURN NEW; END'`,
		`CREATE TRIGGER user_trigger BEFORE INSERT ON s3.has_trigger FOR EACH ROW EXECUTE FUNCTION s3.trigger_fn()`,
		`CREATE TABLE s3.has_rls(id integer)`,
		`ALTER TABLE s3.has_rls ENABLE ROW LEVEL SECURITY`,
		`CREATE TABLE s3.has_rule(id integer)`,
		`CREATE RULE user_rule AS ON UPDATE TO s3.has_rule DO ALSO NOTHING`,
		`CREATE TABLE s3.has_check(id integer CHECK (id > 0))`,
		`CREATE TABLE s3.fk_parent(id integer PRIMARY KEY)`,
		`CREATE TABLE s3.fk_child(id integer REFERENCES s3.fk_parent(id))`,
		`CREATE TABLE s3.has_expression_index(value text)`,
		`CREATE INDEX expression_index ON s3.has_expression_index ((pg_catalog.lower(value)))`,
		`CREATE TABLE s3.has_partial_index(id integer)`,
		`CREATE INDEX partial_index ON s3.has_partial_index(id) WHERE id > 0`,
		`CREATE COLLATION s3.user_collation (provider = libc, locale = 'C')`,
		`CREATE TABLE s3.has_user_collation(value text COLLATE s3.user_collation)`,
		`CREATE FUNCTION s3.plus_one(integer) RETURNS integer LANGUAGE sql IMMUTABLE AS 'SELECT $1 + 1'`,
		`CREATE VIEW s3.has_user_function AS SELECT s3.plus_one(id) AS id FROM s3.base_a`,
		`CREATE AGGREGATE s3.user_sum(integer) (SFUNC = pg_catalog.int4pl, STYPE = integer, INITCOND = '0')`,
		`CREATE VIEW s3.has_user_aggregate AS SELECT s3.user_sum(id) AS id FROM s3.base_a`,
	} {
		_, err := executor.Execute(ctx, statement)
		require.NoError(t, err, statement)
	}
	for name, query := range map[string]string{
		"default":          `SELECT id FROM s3.has_default`,
		"generated":        `SELECT id FROM s3.has_generated`,
		"trigger":          `SELECT id FROM s3.has_trigger`,
		"rls":              `SELECT id FROM s3.has_rls`,
		"rule":             `SELECT id FROM s3.has_rule`,
		"check":            `SELECT id FROM s3.has_check`,
		"fk-outgoing":      `SELECT id FROM s3.fk_child`,
		"fk-incoming":      `SELECT id FROM s3.fk_parent`,
		"expression-index": `SELECT value FROM s3.has_expression_index`,
		"partial-index":    `SELECT id FROM s3.has_partial_index`,
	} {
		t.Run("implicit-"+name, func(t *testing.T) {
			_, err := executor.EnrollPostgresSelect(ctx, query, &unlimitedPostgresBudget{})
			requireAuthorizationReason(t, err, "AUTH_IMPLICIT_OBJECT_UNSUPPORTED")
		})
	}
	for name, query := range map[string]string{
		"user-collation": `SELECT value FROM s3.has_user_collation`,
		"user-function":  `SELECT id FROM s3.has_user_function`,
		"user-aggregate": `SELECT id FROM s3.has_user_aggregate`,
	} {
		t.Run("object-"+name, func(t *testing.T) {
			_, err := executor.EnrollPostgresSelect(ctx, query, &unlimitedPostgresBudget{})
			requireAuthorizationReason(t, err, "AUTH_EXPRESSION_IDENTITY_UNSUPPORTED")
		})
	}
}

func runPostgresExactAllowlistScenarios(t *testing.T, ctx context.Context, executor *PostgresExecutor) {
	t.Helper()
	cases := []struct {
		name  string
		query string
		strip func(*PostgresOIDAllowlist)
	}{
		{"table-am", `SELECT id FROM s3.base_a`, func(a *PostgresOIDAllowlist) { a.RelationAMs = nil }},
		{"index-am", `SELECT id FROM s3.base_a`, func(a *PostgresOIDAllowlist) { a.IndexAMs = nil }},
		{"opclass", `SELECT id FROM s3.base_a`, func(a *PostgresOIDAllowlist) { a.Opclasses = nil }},
		{"opfamily", `SELECT id FROM s3.base_a`, func(a *PostgresOIDAllowlist) { a.Opfamilies = nil }},
		{"amop", `SELECT id FROM s3.base_a`, func(a *PostgresOIDAllowlist) { a.AMOperators = nil }},
		{"amproc", `SELECT id FROM s3.base_a`, func(a *PostgresOIDAllowlist) { a.AMProcedures = nil }},
		{"type-io", `SELECT id FROM s3.base_a`, func(a *PostgresOIDAllowlist) { a.TypeIOFunctions = nil }},
		{"collation", `SELECT secret FROM s3.base_a`, func(a *PostgresOIDAllowlist) { a.Collations = nil }},
		{"aggregate-support", `SELECT pg_catalog.count(id) FROM s3.base_a`, func(a *PostgresOIDAllowlist) { a.AggregateSupport = nil }},
		{"window-support", `SELECT pg_catalog.row_number() OVER () FROM s3.base_a`, func(a *PostgresOIDAllowlist) { a.WindowSupport = nil }},
	}
	for _, test := range cases {
		t.Run("exact-allowlist-"+test.name, func(t *testing.T) {
			manifest, _, err := executor.discoverPostgresSelect(ctx, test.query, &unlimitedPostgresBudget{})
			require.NoError(t, err)
			if test.name == "window-support" && !postgresManifestHasWindowSupport(ctx, executor, manifest) {
				t.Skip("this PostgreSQL major has no support function for the selected window function")
			}
			allow := manifest.Capability.Allowlist
			test.strip(&allow)
			err = scanPostgresManifestWithAllowlist(ctx, executor, manifest, allow)
			requireAuthorizationReason(t, err, "AUTH_IMPLICIT_OBJECT_UNSUPPORTED")
		})
	}
}

func postgresManifestHasWindowSupport(ctx context.Context, executor *PostgresExecutor, manifest PostgresPreparedManifest) bool {
	for _, object := range manifest.Objects {
		if object.Kind == "window" {
			connection, err := executor.pool.Acquire(ctx)
			if err != nil {
				return false
			}
			defer connection.Release()
			var present bool
			if err := connection.QueryRow(ctx, `SELECT prosupport::oid<>0::oid FROM pg_catalog.pg_proc WHERE oid=$1`, object.OID).Scan(&present); err != nil {
				return false
			}
			return present
		}
	}
	return false
}

func runPostgresPreparedIdentityScenarios(t *testing.T, ctx context.Context, executor *PostgresExecutor) {
	t.Helper()
	newPrepared := func(t *testing.T) *PostgresPreparedSelect {
		t.Helper()
		prepared, err := executor.PrepareBoundPostgresSelect(ctx, `SELECT id FROM s3.base_a`, nil, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, prepared.Close(context.Background(), false)) })
		return prepared
	}
	t.Run("same-backend-sealed", func(t *testing.T) {
		prepared := newPrepared(t)
		var pid uint32
		require.NoError(t, prepared.tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid))
		require.Equal(t, prepared.Manifest().BackendPID, pid)
		require.NotZero(t, prepared.Manifest().PlanGeneration)
		_, err := prepared.Execute(ctx, 10)
		require.NoError(t, err)
		_, err = prepared.VerifyPost(ctx, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		require.NoError(t, prepared.Close(ctx, true))
	})
	t.Run("search-path-change", func(t *testing.T) {
		prepared := newPrepared(t)
		_, err := prepared.tx.Exec(ctx, `SET LOCAL search_path=pg_catalog,public`)
		require.NoError(t, err)
		_, err = prepared.Execute(ctx, 10)
		require.Error(t, err)
		require.NoError(t, prepared.Close(ctx, false))
	})
	t.Run("role-change", func(t *testing.T) {
		prepared := newPrepared(t)
		_, err := prepared.tx.Exec(ctx, `SET LOCAL ROLE s3_other`)
		require.NoError(t, err)
		_, err = prepared.Execute(ctx, 10)
		require.Error(t, err)
		require.NoError(t, prepared.Close(ctx, false))
	})
	t.Run("plan-invalidation", func(t *testing.T) {
		prepared := newPrepared(t)
		_, err := prepared.tx.Exec(ctx, `DISCARD PLANS`)
		require.NoError(t, err)
		_, err = prepared.Execute(ctx, 10)
		require.Error(t, err)
		require.NoError(t, prepared.Close(ctx, false))
	})
	t.Run("same-name-hijack", func(t *testing.T) {
		prepared := newPrepared(t)
		name := prepared.name
		_, err := prepared.tx.Exec(ctx, `DEALLOCATE `+quoteInternalPreparedName(name))
		require.NoError(t, err)
		_, err = prepared.tx.Exec(ctx, `PREPARE `+quoteInternalPreparedName(name)+` AS SELECT 1`)
		require.Error(t, err)
		require.NoError(t, prepared.Close(ctx, false))
	})
}

func runPostgresCatalogRaceScenarios(t *testing.T, ctx context.Context, executor *PostgresExecutor) {
	t.Helper()
	for _, statement := range []string{
		`CREATE TABLE s3.aba(id integer)`,
		`CREATE TABLE s3.rename_before(id integer)`,
	} {
		_, err := executor.Execute(ctx, statement)
		require.NoError(t, err)
	}
	t.Run("drop-recreate-aba", func(t *testing.T) {
		enrollment, err := executor.EnrollPostgresSelect(ctx, `SELECT id FROM s3.aba`, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		oldOID := postgresRelationOID(enrollment.Catalog, "aba")
		_, err = executor.Execute(ctx, `DROP TABLE s3.aba`)
		require.NoError(t, err)
		_, err = executor.Execute(ctx, `CREATE TABLE s3.aba(id integer)`)
		require.NoError(t, err)
		newEnrollment, err := executor.EnrollPostgresSelect(ctx, `SELECT id FROM s3.aba`, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		require.NotEqual(t, oldOID, postgresRelationOID(newEnrollment.Catalog, "aba"))
		_, err = executor.PrepareBoundPostgresSelect(ctx, `SELECT id FROM s3.aba`, &enrollment, &unlimitedPostgresBudget{})
		requireAuthorizationReason(t, err, "AUTH_CATALOG_RACE")
	})
	t.Run("rename", func(t *testing.T) {
		enrollment, err := executor.EnrollPostgresSelect(ctx, `SELECT id FROM s3.rename_before`, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		_, err = executor.Execute(ctx, `ALTER TABLE s3.rename_before RENAME TO rename_after`)
		require.NoError(t, err)
		_, err = executor.PrepareBoundPostgresSelect(ctx, `SELECT id FROM s3.rename_after`, &enrollment, &unlimitedPostgresBudget{})
		requireAuthorizationReason(t, err, "AUTH_CATALOG_RACE")
	})
	t.Run("unexpected-closure", func(t *testing.T) {
		candidate, _, err := executor.discoverPostgresSelect(ctx, `SELECT id FROM s3.v2`, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		require.Greater(t, len(candidate.Relations), 1)
		candidate.Relations = candidate.Relations[:len(candidate.Relations)-1]
		_, err = executor.prepareLockedPostgresSelect(ctx, `SELECT id FROM s3.v2`, candidate, nil, &unlimitedPostgresBudget{})
		requireAuthorizationReason(t, err, "AUTH_BIND_CLOSURE_MISMATCH")
	})
	t.Run("locks-block-ddl", func(t *testing.T) {
		prepared, err := executor.PrepareBoundPostgresSelect(ctx, `SELECT id FROM s3.base_a`, nil, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		connection, err := executor.pool.Acquire(ctx)
		require.NoError(t, err)
		_, err = connection.Exec(ctx, `SET lock_timeout='100ms'`)
		require.NoError(t, err)
		_, err = connection.Exec(ctx, `ALTER TABLE s3.base_a RENAME TO should_block`)
		require.Error(t, err)
		connection.Release()
		_, err = prepared.Execute(ctx, 10)
		require.NoError(t, err)
		_, err = prepared.VerifyPost(ctx, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		require.NoError(t, prepared.Close(ctx, true))
	})
}

func scanPostgresManifestWithAllowlist(ctx context.Context, executor *PostgresExecutor, manifest PostgresPreparedManifest, allow PostgresOIDAllowlist) error {
	connection, err := executor.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer connection.Release()
	tx, err := connection.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = scanPostgresCatalog(ctx, tx, manifestRelationOIDs(manifest), manifestViewDepths(manifest), manifest.Objects, allow, &unlimitedPostgresBudget{})
	return err
}

func requireAuthorizationReason(t *testing.T, err error, want string) {
	t.Helper()
	require.Error(t, err)
	var reasoned interface{ AuthorizationReason() string }
	require.True(t, errors.As(err, &reasoned), "%T: %v", err, err)
	require.Equal(t, want, reasoned.AuthorizationReason())
}

func logPostgresDBError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	var databaseError *DBError
	if errors.As(err, &databaseError) {
		t.Logf("PostgreSQL error stage=%s kind=%s code=%s driver=%s", databaseError.Stage, databaseError.Kind, databaseError.Code, databaseError.DriverCode)
	}
}

func postgresRelationOID(frame PostgresCatalogFrame, name string) uint32 {
	for _, relation := range frame.Relations {
		if relation.Name == name {
			return relation.OID
		}
	}
	return 0
}

func requirePostgresRelationIdentity(t *testing.T, frame PostgresCatalogFrame, name string, kind byte) {
	t.Helper()
	oid := postgresRelationOID(frame, name)
	require.NotZero(t, oid, name)
	for _, relation := range frame.Relations {
		if relation.OID == oid {
			require.Equal(t, kind, relation.Kind)
			require.NotZero(t, relation.DatabaseOID)
			require.NotZero(t, relation.NamespaceOID)
			return
		}
	}
}

func requirePostgresColumnLineage(t *testing.T, enrollment PostgresEnrollment, relationName, columnName string) {
	t.Helper()
	oid := postgresRelationOID(enrollment.Catalog, relationName)
	require.NotZero(t, oid, relationName)
	attnums := make([]int16, 0, 1)
	for _, column := range enrollment.Catalog.Columns {
		if column.RelationOID == oid && column.Name == columnName {
			attnums = append(attnums, column.Attnum)
		}
	}
	require.NotEmpty(t, attnums, relationName+"."+columnName)
	sort.Slice(attnums, func(i, j int) bool { return attnums[i] < attnums[j] })
	for _, use := range enrollment.Manifest.Columns {
		if use.RelationOID == oid && use.Attnum == attnums[0] {
			require.True(t, use.ContributorComplete)
			return
		}
	}
	require.Fail(t, "missing analyzed column lineage", relationName+"."+columnName)
}

func requirePostgresACLInputs(t *testing.T, manifest PostgresPreparedManifest, frame PostgresCatalogFrame) {
	t.Helper()
	required := map[uint32]bool{
		postgresRelationOID(frame, "base_a"): false,
		postgresRelationOID(frame, "v_ab"):   false,
		postgresRelationOID(frame, "v2"):     false,
	}
	for _, relation := range manifest.Relations {
		if _, ok := required[relation.OID]; ok && relation.RequiredPerms != 0 {
			required[relation.OID] = true
		}
	}
	for oid, present := range required {
		require.NotZero(t, oid)
		require.True(t, present, "missing analyzed ACL input for relation OID %d", oid)
	}
}

func diagnosePostgresBinder(t *testing.T, ctx context.Context, executor *PostgresExecutor, rawSQL string) {
	t.Helper()
	connection, err := executor.pool.Acquire(ctx)
	if err != nil {
		t.Logf("binder diagnostic acquire: %v", err)
		return
	}
	defer connection.Release()
	tx, err := connection.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Logf("binder diagnostic begin: %v", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, probe := range []struct {
		name, sql string
		args      []any
	}{
		{"prepare", `SELECT agentsql_catalog.prepare($1,$2)`, []any{"agentsql_diagnostic", rawSQL}},
		{"capability", `SELECT agentsql_catalog.capabilities()`, nil},
		{"manifest", `SELECT * FROM agentsql_catalog.prepared_manifest($1)`, []any{"agentsql_diagnostic"}},
		{"relations", `SELECT * FROM agentsql_catalog.prepared_relations($1)`, []any{"agentsql_diagnostic"}},
		{"vars", `SELECT * FROM agentsql_catalog.prepared_vars($1)`, []any{"agentsql_diagnostic"}},
		{"objects", `SELECT * FROM agentsql_catalog.prepared_objects($1)`, []any{"agentsql_diagnostic"}},
	} {
		rows, probeErr := tx.Query(ctx, probe.sql, probe.args...)
		if probeErr == nil {
			for rows.Next() {
			}
			probeErr = rows.Err()
			rows.Close()
		}
		if probeErr != nil {
			t.Logf("binder diagnostic %s: %s", probe.name, fmt.Sprintf("%+v", probeErr))
			return
		}
	}
}

type unlimitedPostgresBudget struct{}

func (*unlimitedPostgresBudget) ChargeRelations(int) error         { return nil }
func (*unlimitedPostgresBudget) CheckViewDepth(int) error          { return nil }
func (*unlimitedPostgresBudget) ChargeCatalogRoundTrips(int) error { return nil }
func (*unlimitedPostgresBudget) ChargeDefinitionBytes(int) error   { return nil }
func (*unlimitedPostgresBudget) ChargeBinderBytes(int) error       { return nil }
func (*unlimitedPostgresBudget) ChargeCatalogBytes(int) error      { return nil }
func (*unlimitedPostgresBudget) ChargeCatalogRows(int) error       { return nil }
func (*unlimitedPostgresBudget) ChargeColumnMetadata(int) error    { return nil }
func (*unlimitedPostgresBudget) ChargeNodes(int) error             { return nil }
func (*unlimitedPostgresBudget) ChargeEdges(int) error             { return nil }
func (*unlimitedPostgresBudget) ChargePaths(int) error             { return nil }
func (*unlimitedPostgresBudget) ChargeWork(int) error              { return nil }

var _ PostgresCatalogBudget = (*unlimitedPostgresBudget)(nil)
