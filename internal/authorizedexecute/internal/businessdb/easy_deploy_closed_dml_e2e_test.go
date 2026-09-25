package businessdb

import (
	"context"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/b5dml"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// runClosedDMLDifferentialScenarios shares the existing PG14-PG18 extension
// containers. Every major invokes it as both superuser and a regular role.
func runClosedDMLDifferentialScenarios(t *testing.T, ctx context.Context, executor *PostgresExecutor, datasourceIdentity string) {
	t.Helper()
	for _, test := range []struct {
		name   string
		sql    string
		action b5dml.Action
	}{
		{"insert-values-implicit-null", `INSERT INTO s3.base_b(id) VALUES (20)`, b5dml.ActionInsert},
		{"update-where", `UPDATE s3.base_b AS b SET note='changed' WHERE b.id=1`, b5dml.ActionUpdate},
		{"delete-where", `DELETE FROM s3.base_b AS b WHERE b.id=99`, b5dml.ActionDelete},
	} {
		t.Run("dml-differential-"+test.name, func(t *testing.T) {
			request := BindRequest{RawSQL: test.sql, Identity: SemanticIdentity{DatasourceIdentity: datasourceIdentity}}
			closed, err := executor.BindClosedDML(ctx, request, &unlimitedPostgresBudget{})
			require.NoError(t, err)
			program := closed.Program()
			require.Equal(t, test.action, program.Facts.Action)
			_, err = closed.VerifyPost(ctx, &unlimitedPostgresBudget{})
			require.NoError(t, err)
			require.NoError(t, closed.Close(ctx))

			native, err := executor.EnrollPostgresDML(ctx, test.sql, datasourceIdentity, &unlimitedPostgresBudget{})
			logPostgresDBError(t, err)
			require.NoError(t, err)
			auth := postgresDMLMatrixAuthorization(native)
			auth.DatasourceID = datasourceIdentity
			for index := range auth.Policies {
				auth.Policies[index].DatasourceID = datasourceIdentity
			}
			tx, nativeTx := beginPostgresDMLNative(t, ctx, executor)
			defer func() { _ = tx.Rollback(context.Background()) }()
			sealed, decision, err := executor.PrepareBoundPostgresDML(ctx, nativeTx, test.sql, &native, auth, &unlimitedPostgresBudget{})
			require.NoError(t, err)
			defer func() { _ = sealed.Close(context.Background()) }()
			require.True(t, decision.Allowed())
			native.Manifest = sealed.Manifest()
			nativeFacts, err := NativeDMLSemanticFacts(datasourceIdentity, SemanticIdentity{DatasourceIdentity: datasourceIdentity}, native)
			require.NoError(t, err)
			require.Equal(t, program.Facts, nativeFacts)
			require.NoError(t, CompareClosedWithNativeDML(program, datasourceIdentity,
				SemanticIdentity{DatasourceIdentity: datasourceIdentity}, native),
				"closed=%#v native=%#v", program.Facts, native.Facts)
			require.NoError(t, sealed.Close(ctx))
			require.NoError(t, tx.Rollback(ctx))

			input, policies := closedDMLAllowInput(program.Facts, program.Capability.ServerMajor)
			input.Policies = policies
			decision, err = AuthorizeB5(program.Facts, input)
			require.NoError(t, err)
			require.True(t, decision.Allowed(), decision.Reason())
			if test.action == b5dml.ActionInsert {
				implicit := semanticWriteByName(program.Facts.WriteTargets, "note", b5dml.WriteSourceImplicitNull)
				require.NotZero(t, implicit.RelationOID)
				require.Contains(t, program.Facts.WriteTargets, implicit)
				input.Policies[0].Grants = input.Policies[0].Grants[:len(input.Policies[0].Grants)-1]
				decision, err = AuthorizeB5(program.Facts, input)
				require.NoError(t, err)
				require.False(t, decision.Allowed())
				require.Equal(t, b5dml.ReasonWriteTargetGrantMissing, decision.Reason())
			}
		})
	}

	for _, test := range []struct {
		name string
		sql  string
	}{
		{"unqualified", `DELETE FROM base_b WHERE id=1`},
		{"insert-select", `INSERT INTO s3.base_b(id,note) SELECT id,secret FROM s3.base_a`},
		{"insert-default", `INSERT INTO s3.base_b(id,note) VALUES (2,DEFAULT)`},
		{"insert-function", `INSERT INTO s3.base_b(id,note) VALUES (2,pg_catalog.current_user)`},
		{"insert-subquery", `INSERT INTO s3.base_b(id,note) VALUES ((SELECT 2),'x')`},
		{"upsert", `INSERT INTO s3.base_b(id,note) VALUES (2,'x') ON CONFLICT DO NOTHING`},
		{"returning", `UPDATE s3.base_b SET note='x' WHERE id=1 RETURNING id`},
		{"update-no-where", `UPDATE s3.base_b SET note='x'`},
		{"update-from", `UPDATE s3.base_b b SET note=a.secret FROM s3.base_a a WHERE b.id=a.id`},
		{"update-subquery", `UPDATE s3.base_b SET note=(SELECT secret FROM s3.base_a LIMIT 1) WHERE id=1`},
		{"delete-no-where", `DELETE FROM s3.base_b`},
		{"delete-using", `DELETE FROM s3.base_b b USING s3.base_a a WHERE b.id=a.id`},
		{"delete-subquery", `DELETE FROM s3.base_b WHERE id IN (SELECT id FROM s3.base_a)`},
		{"cte", `WITH q AS (SELECT 1) DELETE FROM s3.base_b WHERE id=1`},
		{"multi", `DELETE FROM s3.base_b WHERE id=1; DELETE FROM s3.base_b WHERE id=2`},
		{"view", `DELETE FROM s3.v_ab WHERE id=1`},
		{"matview", `DELETE FROM s3.mv WHERE id=1`},
		{"partition", `DELETE FROM s3.part_parent WHERE id=1`},
		{"inheritance", `DELETE FROM s3.inh_parent WHERE id=1`},
		{"foreign", `DELETE FROM s3.foreign_table WHERE id=1`},
		{"unlogged", `DELETE FROM s3.unlogged_table WHERE id=1`},
		{"trigger", `UPDATE s3.has_trigger SET id=1 WHERE id=2`},
		{"rule", `UPDATE s3.has_rule SET id=1 WHERE id=2`},
		{"rls", `DELETE FROM s3.has_rls WHERE id=1`},
		{"foreign-key", `DELETE FROM s3.fk_parent WHERE id=1`},
		{"check", `UPDATE s3.has_check SET id=2 WHERE id=1`},
		{"default", `INSERT INTO s3.has_default(id) VALUES (1)`},
		{"generated", `INSERT INTO s3.has_generated(id) VALUES (1)`},
		{"expression-index", `UPDATE s3.has_expression_index SET value='x' WHERE value='y'`},
		{"partial-index", `DELETE FROM s3.has_partial_index WHERE id=1`},
		{"system-column", `DELETE FROM s3.base_b WHERE ctid='(0,1)'`},
		{"whole-row", `DELETE FROM s3.base_b b WHERE b IS NOT NULL`},
		{"escape-parameter", `UPDATE s3.base_b SET note=$1 WHERE id=1`},
		{"escape-comment-stack", `DELETE FROM s3.base_b WHERE id=1 /* ; */; DELETE FROM s3.base_b WHERE id=2`},
	} {
		t.Run("dml-fail-closed-"+test.name, func(t *testing.T) {
			prepared, err := executor.BindClosedDML(ctx, BindRequest{RawSQL: test.sql,
				Identity: SemanticIdentity{DatasourceIdentity: datasourceIdentity}}, &unlimitedPostgresBudget{})
			if prepared != nil {
				_ = prepared.Close(ctx)
			}
			require.Error(t, err, "unsafe statement accepted: %s", test.sql)
			var reason interface{ AuthorizationReason() string }
			require.ErrorAs(t, err, &reason)
		})
	}
	require.Eventually(t, func() bool { return executor.pool.Stat().AcquiredConns() == 0 }, time.Second, 10*time.Millisecond,
		"closed-DML corpus leaked %d pool connections", executor.pool.Stat().AcquiredConns())
}

func runClosedDMLCatalogRaceScenarios(t *testing.T, ctx context.Context, executor *PostgresExecutor, datasourceIdentity string) {
	t.Helper()
	t.Run("closed-dml-locks-block-ddl", func(t *testing.T) {
		prepared, err := executor.BindClosedDML(ctx, BindRequest{RawSQL: `UPDATE s3.base_b SET note='held' WHERE id=1`,
			Identity: SemanticIdentity{DatasourceIdentity: datasourceIdentity}}, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		defer func() { _ = prepared.Close(context.Background()) }()

		connection, err := executor.pool.Acquire(ctx)
		require.NoError(t, err)
		defer connection.Release()
		_, err = connection.Exec(ctx, `SET lock_timeout='100ms'`)
		require.NoError(t, err)
		_, err = connection.Exec(ctx, `ALTER TABLE s3.base_b RENAME TO should_block_s4`)
		require.Error(t, err)
		_, err = prepared.VerifyPost(ctx, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		require.NoError(t, prepared.Close(ctx))
	})

	t.Run("closed-dml-drop-recreate-aba", func(t *testing.T) {
		refs := []ClosedRelationRef{{Schema: "s3", Name: "s4_aba"}}
		candidate, err := executor.DiscoverClosedCatalog(ctx, refs, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		require.Len(t, candidate.Frame.Relations, 1)
		oldOID := candidate.Frame.Relations[0].OID
		_, err = executor.Execute(ctx, `DROP TABLE s3.s4_aba`)
		require.NoError(t, err)
		_, err = executor.Execute(ctx, `CREATE TABLE s3.s4_aba(id integer, note text)`)
		require.NoError(t, err)
		current, err := executor.DiscoverClosedCatalog(ctx, refs, &unlimitedPostgresBudget{})
		require.NoError(t, err)
		require.NotEqual(t, oldOID, current.Frame.Relations[0].OID)

		_, err = executor.prepareClosedCatalogResolved(ctx, `UPDATE s3.s4_aba SET note='x' WHERE id=1`, candidate,
			&unlimitedPostgresBudget{}, func(context.Context, pgx.Tx, PostgresCatalogFrame, PostgresCatalogBudget) (SemanticFacts, error) {
				t.Fatal("stale ABA candidate reached the DML resolver")
				return SemanticFacts{}, nil
			})
		requireAuthorizationReason(t, err, "AUTH_CATALOG_RACE")
	})
}

func closedDMLAllowInput(facts SemanticFacts, serverMajor int) (b5dml.AuthorizationInput, []b5dml.Policy) {
	relation := semanticB5Relation(facts.Relations[0])
	grants := []b5dml.Grant{{Element: b5dml.GrantAction, Action: facts.Action, Relation: relation}}
	for _, write := range facts.WriteTargets {
		grant := b5dml.Grant{Element: b5dml.GrantWriteTarget, Action: facts.Action, Relation: relation, WriteKind: write.Kind}
		if write.Kind == b5dml.WriteTargetColumn {
			grant.Column = semanticB5Column(relation, write.Attnum, write.Name, write.TypeOID, write.TypeModifier, write.CollationOID)
		}
		grants = append(grants, grant)
	}
	for _, reference := range facts.ColumnUses {
		column := semanticB5Column(relation, reference.Attnum, reference.Name, reference.TypeOID, reference.TypeModifier, reference.CollationOID)
		grants = append(grants, b5dml.Grant{Element: b5dml.GrantReference, Action: facts.Action, Relation: relation,
			ReferenceKind: b5dml.ReferenceColumn, Column: column})
	}
	input := closedDMLAuthorizationInput(nil)
	input.DatasourceID = relation.DatasourceID
	input.CurrentServerMajor = serverMajor
	return input, []b5dml.Policy{{ID: "matrix", Revision: 1, PrincipalID: "agent", DatasourceID: relation.DatasourceID, Effect: b5dml.GrantAllow, Grants: grants}}
}

func semanticWriteByName(values []SemanticWriteTarget, name string, source b5dml.WriteSource) SemanticWriteTarget {
	for _, value := range values {
		if value.Name == name && value.Source == source {
			return value
		}
	}
	return SemanticWriteTarget{}
}
