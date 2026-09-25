package businessdb

import (
	"context"
	"errors"
	"testing"

	closedparser "github.com/cuipengdba/agentsql/internal/parser"
)

func TestClosedSelectResolverDirectJoinAndExpressions(t *testing.T) {
	t.Parallel()
	frame := closedSelectUnitFrame()
	tests := []struct {
		name       string
		sql        string
		outputs    int
		references int
		relations  int
	}{
		{"direct", `SELECT a.id, a.note FROM app.accounts a WHERE a.id > 0 AND a.note IS NOT NULL`, 2, 2, 1},
		{"self-join", `SELECT a.id, b.note FROM app.accounts a INNER JOIN app.accounts b ON a.id=b.id WHERE b.id > 0`, 2, 1, 1},
		{"left-join", `SELECT a.id + b.id FROM app.accounts a LEFT JOIN app.other b ON a.id=b.id WHERE b.id IS NOT NULL`, 2, 2, 2},
		{"exists-correlated", `SELECT a.id FROM app.accounts a WHERE EXISTS (SELECT b.id FROM app.other b WHERE b.id=a.id)`, 1, 3, 2},
		{"in-subquery", `SELECT a.id FROM app.accounts a WHERE a.id IN (SELECT b.id FROM app.other b WHERE b.id > 0)`, 1, 3, 2},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			parsed, err := closedparser.ParsePostgresClosedSelect(test.sql)
			if err != nil {
				t.Fatal(err)
			}
			facts, err := resolvePostgresClosedSelect(context.Background(), parsed, frame, "ds-1", closedSelectUnitOperator, &unlimitedPostgresBudget{})
			if err != nil {
				t.Fatal(err)
			}
			outputs, references := 0, 0
			for _, use := range facts.ColumnUses {
				switch use.Usage {
				case SemanticUsageOutput:
					outputs++
				case SemanticUsageReference:
					references++
				}
				if use.Attnum <= 0 || use.RelationOID == 0 || use.Name == "" || use.BindingAlias == "" {
					t.Fatalf("incomplete use: %#v", use)
				}
			}
			if outputs != test.outputs || references != test.references || len(facts.Relations) != test.relations {
				t.Fatalf("facts outputs=%d references=%d relations=%d: %#v", outputs, references, len(facts.Relations), facts)
			}
			if facts.StatementClass != BinderStatementSelect || facts.Identity.DatasourceIdentity != "ds-1" || len(facts.ObjectUses) == 0 {
				t.Fatalf("facts = %#v", facts)
			}
		})
	}
}

func TestClosedSelectResolverFailClosedNameAndShapeCorpus(t *testing.T) {
	t.Parallel()
	frame := closedSelectUnitFrame()
	tests := []struct {
		name string
		sql  string
		code string
	}{
		{"ambiguous", `SELECT id FROM app.accounts a JOIN app.other b ON a.id=b.id`, BinderCodeModeRequired},
		{"missing", `SELECT a.missing FROM app.accounts a`, BinderCodeModeRequired},
		{"system", `SELECT a.ctid FROM app.accounts a`, "AUTH_COLUMN_SHAPE_UNSUPPORTED"},
		{"whole-row", `SELECT a FROM app.accounts a`, "AUTH_COLUMN_SHAPE_UNSUPPORTED"},
		{"implicit-cast", `SELECT a.small_id + 1 FROM app.accounts a`, BinderCodeModeRequired},
		{"type-mismatch", `SELECT a.id + b.big_id FROM app.accounts a JOIN app.other b ON a.id=b.id`, BinderCodeModeRequired},
		{"user-type", `SELECT a.composite_value FROM app.accounts a`, "AUTH_COLUMN_SHAPE_UNSUPPORTED"},
		{"user-collation", `SELECT a.custom_collation FROM app.accounts a`, "AUTH_COLUMN_SHAPE_UNSUPPORTED"},
	}
	for _, test := range tests {
		parsed, err := closedparser.ParsePostgresClosedSelect(test.sql)
		if err != nil {
			t.Fatalf("%s parser: %v", test.name, err)
		}
		_, err = resolvePostgresClosedSelect(context.Background(), parsed, frame, "ds-1", closedSelectUnitOperator, &unlimitedPostgresBudget{})
		requireEasyDeployReason(t, err, test.code)
	}
}

func TestClosedSelectParserErrorsMapToStableBinderCodes(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		sql  string
		code string
	}{
		{`SELECT * FROM app.accounts`, "AUTH_COLUMN_SHAPE_UNSUPPORTED"},
		{`SELECT lower(a.note) FROM app.accounts a`, BinderCodeModeRequired},
		{`SELECT a.id FROM app.accounts a; SELECT 1`, BinderCodeModeUnsupported},
	} {
		_, err := parseClosedSelectForBinding(test.sql, &unlimitedPostgresBudget{})
		requireEasyDeployReason(t, err, test.code)
	}
}

func TestClosedSelectDifferentialGateRejectsAnyFieldDrift(t *testing.T) {
	t.Parallel()
	facts := semanticSelectFixture()
	facts.Relations = facts.Relations[:1]
	closed := BoundProgram{Mode: BinderModeCatalogClosedV1, Facts: facts}
	manifest := PostgresPreparedManifest{RoleOID: 10, RoleName: "agent", SearchPath: "pg_catalog", CommandType: "SELECT",
		AnalyzedDigest: "analyzed", DependencyDigest: "dependency", PlanGeneration: 1,
		Relations: []PostgresBoundRelation{{OID: 100, Kind: 'r', Path: "query.rte1"}},
		Columns:   []PostgresColumnUse{{Site: "target.1", RelationOID: 100, Attnum: 1, Usage: "output", ContributorGroup: 1, ContributorComplete: true}}}
	frame := PostgresCatalogFrame{ServerVersion: 160000, DatabaseOID: 9, Fingerprint: "catalog-v1",
		Relations: []PostgresRelationIdentity{{DatabaseOID: 9, OID: 100, NamespaceOID: 11, Schema: "public", Name: "a", Kind: 'r', Persistence: 'p'}},
		Columns:   []PostgresColumnIdentity{{RelationOID: 100, Attnum: 1, Name: "id", TypeOID: 23, Typmod: -1}}}
	closed.Facts.Relations[0].BindingAlias = "query.rte1"
	closed.Facts.ColumnUses = closed.Facts.ColumnUses[:1]
	closed.Facts.ColumnUses[0].Site = "target.1"
	closed.Facts.ColumnUses[0].BindingAlias = "query.rte1"
	closed.Facts.ObjectUses = nil
	session := facts.Identity
	session.SearchPathDigest = ""
	native, err := NativeSelectSemanticFacts("ds-1", session, manifest, frame)
	if err != nil {
		t.Fatal(err)
	}
	closed.Facts.Identity = native.Identity
	if err := CompareClosedWithNativeSelect(closed, "ds-1", session, manifest, frame); err != nil {
		t.Fatalf("equal facts rejected: %v", err)
	}
	manifest.Columns[0].Site = "drift"
	err = CompareClosedWithNativeSelect(closed, "ds-1", session, manifest, frame)
	var binderErr *BinderError
	if !errors.As(err, &binderErr) || binderErr.Code != BinderCodeDivergence {
		t.Fatalf("drift error = %#v", err)
	}
}

func closedSelectUnitFrame() PostgresCatalogFrame {
	return PostgresCatalogFrame{ServerVersion: 160000, DatabaseOID: 9, Fingerprint: "catalog-v1",
		Relations: []PostgresRelationIdentity{
			{DatabaseOID: 9, OID: 100, NamespaceOID: 11, Schema: "app", Name: "accounts", Kind: 'r', Persistence: 'p'},
			{DatabaseOID: 9, OID: 200, NamespaceOID: 11, Schema: "app", Name: "other", Kind: 'r', Persistence: 'p'},
		},
		Columns: []PostgresColumnIdentity{
			{RelationOID: 100, Attnum: 1, Name: "id", TypeOID: 23, Typmod: -1},
			{RelationOID: 100, Attnum: 2, Name: "note", TypeOID: 25, Typmod: -1, Collation: 100},
			{RelationOID: 100, Attnum: 3, Name: "small_id", TypeOID: 21, Typmod: -1},
			{RelationOID: 100, Attnum: 4, Name: "composite_value", TypeOID: 20000, Typmod: -1},
			{RelationOID: 100, Attnum: 5, Name: "custom_collation", TypeOID: 25, Typmod: -1, Collation: 20000},
			{RelationOID: 200, Attnum: 1, Name: "id", TypeOID: 23, Typmod: -1},
			{RelationOID: 200, Attnum: 2, Name: "big_id", TypeOID: 20, Typmod: -1},
		}}
}

func closedSelectUnitOperator(_ context.Context, name string, left, right uint32, _ PostgresCatalogBudget) (closedOperatorIdentity, error) {
	if left != right {
		return closedOperatorIdentity{}, NewPrecisionFailure(BinderCodeModeRequired)
	}
	result := left
	if closedComparisonOperator(name) {
		result = 16
	}
	return closedOperatorIdentity{OID: 500 + left, FunctionOID: 1500 + left, ResultType: result}, nil
}
