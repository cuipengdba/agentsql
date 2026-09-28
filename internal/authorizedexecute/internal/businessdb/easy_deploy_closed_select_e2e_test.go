package businessdb

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// runClosedSelectDifferentialScenarios is called by the existing PG14-PG18 C
// extension matrix. Each major runs this corpus as both the superuser and a
// NOSUPERUSER/NOCREATEDB/NOCREATEROLE account.
func runClosedSelectDifferentialScenarios(t *testing.T, ctx context.Context, executor *PostgresExecutor, datasourceIdentity string) {
	t.Helper()
	for _, test := range []struct {
		name string
		sql  string
	}{
		{"direct", `SELECT a.id,a.secret FROM s3.base_a a WHERE a.id > 0`},
		{"self-join", `SELECT a.id,b.secret FROM s3.base_a a INNER JOIN s3.base_a b ON a.id=b.id WHERE b.id > 0`},
		{"inner-join", `SELECT a.id,b.note FROM s3.base_a a INNER JOIN s3.base_b b ON a.id=b.id WHERE b.note IS NOT NULL`},
		{"left-join", `SELECT a.id+b.id FROM s3.base_a a LEFT JOIN s3.base_b b ON a.id=b.id WHERE a.id > 0`},
		{"closed-expression", `SELECT a.id+1 FROM s3.base_a a WHERE NOT (a.id=0) AND a.secret IS NOT NULL`},
		{"exists", `SELECT a.id FROM s3.base_a a WHERE EXISTS (SELECT b.id FROM s3.base_b b WHERE b.id=a.id)`},
		{"in-subquery", `SELECT a.id FROM s3.base_a a WHERE a.id IN (SELECT b.id FROM s3.base_b b WHERE b.id > 0)`},
	} {
		t.Run("differential-"+test.name, func(t *testing.T) {
			request := BindRequest{RawSQL: test.sql, Identity: SemanticIdentity{DatasourceIdentity: datasourceIdentity}}
			closed, err := executor.BindClosedSelect(ctx, request, &unlimitedPostgresBudget{})
			require.NoError(t, err)
			program := closed.Program()
			_, err = closed.Execute(ctx, 10)
			require.NoError(t, err)
			_, err = closed.VerifyPost(ctx, &unlimitedPostgresBudget{})
			require.NoError(t, err)
			require.NoError(t, closed.Close(ctx))

			native, err := executor.PrepareBoundPostgresSelect(ctx, test.sql, nil, &unlimitedPostgresBudget{})
			require.NoError(t, err)
			err = CompareClosedWithNativeSelect(program, datasourceIdentity,
				SemanticIdentity{DatasourceIdentity: datasourceIdentity}, native.Manifest(), native.Fpre())
			require.NoError(t, err, "closed=%#v native-manifest=%#v native-frame=%#v", program.Facts, native.Manifest(), native.Fpre())
			require.NoError(t, native.Close(ctx, false))
		})
	}

	for _, test := range []struct {
		name string
		sql  string
	}{
		{"view", `SELECT id FROM s3.v_ab`},
		{"matview", `SELECT id FROM s3.mv`},
		{"cte", `WITH q AS (SELECT id FROM s3.base_a) SELECT id FROM q`},
		{"lateral", `SELECT q.id FROM s3.base_a a JOIN LATERAL (SELECT a.id) q ON true`},
		{"function", `SELECT pg_catalog.lower(a.secret) FROM s3.base_a a`},
		{"aggregate", `SELECT pg_catalog.count(a.id) FROM s3.base_a a`},
		{"cast", `SELECT a.id::bigint FROM s3.base_a a`},
		{"unknown-coercion", `SELECT a.secret FROM s3.base_a a WHERE a.secret='alpha'`},
		{"distinct", `SELECT DISTINCT a.id FROM s3.base_a a`},
		{"group", `SELECT a.id FROM s3.base_a a GROUP BY a.id`},
		{"sort", `SELECT a.id FROM s3.base_a a ORDER BY a.id`},
		{"whole-row", `SELECT a FROM s3.base_a a`},
		{"qualified-star", `SELECT a.* FROM s3.base_a a`},
		{"system-column", `SELECT a.ctid FROM s3.base_a a`},
		{"select-into", `SELECT a.id INTO s3.copy FROM s3.base_a a`},
		{"for-update", `SELECT a.id FROM s3.base_a a FOR UPDATE`},
		{"multi", `SELECT a.id FROM s3.base_a a; SELECT b.id FROM s3.base_b b`},
		{"unlogged", `SELECT id FROM s3.unlogged_table`},
		{"foreign-postgres-fdw", `SELECT id FROM s3.foreign_table`},
		{"inheritance", `SELECT id FROM ONLY s3.inh_parent`},
		{"partition", `SELECT id FROM ONLY s3.part_parent`},
		{"trigger", `SELECT id FROM s3.has_trigger`},
		{"rule", `SELECT id FROM s3.has_rule`},
		{"rls", `SELECT id FROM s3.has_rls`},
		{"check", `SELECT id FROM s3.has_check`},
		{"foreign-key", `SELECT id FROM s3.fk_child`},
		{"default", `SELECT id FROM s3.has_default`},
		{"generated", `SELECT id FROM s3.has_generated`},
		{"expression-index", `SELECT value FROM s3.has_expression_index`},
		{"partial-index", `SELECT id FROM s3.has_partial_index`},
		{"user-collation", `SELECT value FROM s3.has_user_collation`},
		{"dblink", `SELECT dblink('x','SELECT 1') FROM s3.base_a a`},
		{"escape-comment-stack", "SELECT a.id FROM s3.base_a a /* ; */; SELECT 1"},
		{"escape-dollar", `SELECT $1 FROM s3.base_a a`},
		{"escape-operator", `SELECT a.id OPERATOR(pg_catalog.+) 1 FROM s3.base_a a`},
	} {
		t.Run("fail-closed-"+test.name, func(t *testing.T) {
			prepared, err := executor.BindClosedSelect(ctx, BindRequest{RawSQL: test.sql,
				Identity: SemanticIdentity{DatasourceIdentity: datasourceIdentity}}, &unlimitedPostgresBudget{})
			if prepared != nil {
				_ = prepared.Close(ctx)
			}
			require.Error(t, err, "unsafe statement accepted: %s", test.sql)
			var reason interface{ AuthorizationReason() string }
			require.ErrorAs(t, err, &reason)
			require.Contains(t, []string{BinderCodeModeRequired, BinderCodeModeUnsupported, "AUTH_COLUMN_SHAPE_UNSUPPORTED",
				"AUTH_RELATION_SHAPE_UNSUPPORTED", "AUTH_IMPLICIT_OBJECT_UNSUPPORTED"}, reason.AuthorizationReason())
		})
	}
	require.Eventually(t, func() bool {
		return executor.pool.Stat().AcquiredConns() == 0
	}, time.Second, 10*time.Millisecond, "closed-select corpus leaked %d pool connections", executor.pool.Stat().AcquiredConns())
}
