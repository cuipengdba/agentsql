package b5dml

import "testing"

func TestClosureNegativeMatrix(t *testing.T) {
	t.Parallel()
	relation := testRelation("target", 100)
	dependency := testRelation("dependency", 200)
	findings := []struct {
		name       string
		kind       ClosureKind
		inManifest bool
	}{
		{"trigger-write-outside-manifest", ClosureTriggerWriteOutsideManifest, false},
		{"trigger", ClosureTrigger, true},
		{"foreign-key-read", ClosureForeignKeyRead, true},
		{"foreign-key-cascade-write", ClosureForeignKeyCascadeWrite, true},
		{"check-constraint-read", ClosureCheckConstraintRead, true},
		{"unique-constraint-internal-read", ClosureUniqueConstraintRead, true},
		{"exclusion-constraint-internal-read", ClosureExclusionConstraintRead, true},
		{"rule", ClosureRule, true},
		{"view", ClosureView, true},
		{"partition-routing", ClosurePartitionRouting, true},
		{"partition-child", ClosurePartitionChild, true},
		{"udf", ClosureUserDefinedFunction, true},
		{"rls", ClosureRowLevelSecurity, true},
		{"default", ClosureDefaultExpression, true},
		{"identity-sequence", ClosureIdentitySequence, true},
		{"generated", ClosureGeneratedExpression, true},
		{"expression-index", ClosureExpressionIndex, true},
		{"partial-index", ClosurePartialIndexPredicate, true},
		{"domain-coercion", ClosureDomainOrCoercion, true},
	}
	shapes := []struct {
		name  string
		shape StatementShape
	}{
		{"update-from", ShapeUpdateFrom},
		{"delete-using", ShapeDeleteUsing},
		{"insert-select", ShapeInsertSelect},
		{"upsert", ShapeUpsert},
		{"merge", ShapeMerge},
		{"cte", ShapeCTE},
		{"writable-cte", ShapeWritableCTE},
		{"dml-subquery", ShapeDMLSubquery},
	}
	if len(findings)+len(shapes) != FrozenClosureNegativeEntries {
		t.Fatalf("matrix rows = %d, frozen = %d", len(findings)+len(shapes), FrozenClosureNegativeEntries)
	}
	cells := 0
	for _, dialect := range []Dialect{DialectPostgreSQL, DialectMySQL} {
		for _, test := range findings {
			cells++
			t.Run(dialectName(dialect)+"/"+test.name, func(t *testing.T) {
				decision := CheckClosure(dialect, ShapeSimple, []ClosureFinding{{Kind: test.kind,
					Owner: relation, Dependency: dependency, InManifest: test.inManifest}})
				assertClosureDeny(t, dialect, decision)
			})
		}
		for _, test := range shapes {
			cells++
			t.Run(dialectName(dialect)+"/"+test.name, func(t *testing.T) {
				assertClosureDeny(t, dialect, CheckClosure(dialect, test.shape, nil))
			})
		}
	}
	if cells != FrozenClosureDialectCells {
		t.Fatalf("matrix cells = %d, frozen = %d", cells, FrozenClosureDialectCells)
	}
	if decision := CheckClosure(DialectPostgreSQL, ShapeSimple, nil); !decision.Allowed || decision.Reason != ClosureAllowed {
		t.Fatalf("empty simple closure = %+v", decision)
	}
}

func assertClosureDeny(t *testing.T, dialect Dialect, decision ClosureDecision) {
	t.Helper()
	if decision.Allowed {
		t.Fatal("negative closure was allowed")
	}
	if dialect == DialectMySQL && decision.Reason != ClosureDialectUnsupported {
		t.Fatalf("mysql reason = %s", decision.Reason)
	}
}

func dialectName(dialect Dialect) string {
	if dialect == DialectPostgreSQL {
		return "postgres"
	}
	return "mysql"
}
