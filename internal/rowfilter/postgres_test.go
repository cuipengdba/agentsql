package rowfilter

import (
	"errors"
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/parser"
	pg_query "github.com/pganalyze/pg_query_go/v5"
)

func TestCompileForPoliciesClosesAmbiguousScopeAndCombinesRules(t *testing.T) {
	west, positive, east := "region = 'west'", "amount > 0", "region = 'east'"
	base := model.Policy{ID: "p1", AgentID: "agent", DatasourceID: "ds", ObjectType: "table", ObjectName: "public.sales", RowFilter: &west, Action: "allow"}
	second := base
	second.ID, second.RowFilter = "p2", &positive
	unrelated := base
	unrelated.ID, unrelated.AgentID, unrelated.RowFilter = "p3", "other-agent", &east
	sql := "SELECT region FROM public.sales"
	got, digest, relation, err := CompileForPolicies(sql, "agent", "ds", []model.Policy{base, second, unrelated})
	if err != nil || relation != "public.sales" || len(digest) != 64 ||
		!strings.Contains(got, "region = 'west'") || !strings.Contains(got, "amount > 0") || strings.Contains(got, "region = 'east'") {
		t.Fatalf("incorrect scoped row rule: sql=%q digest=%q relation=%q err=%v", got, digest, relation, err)
	}
	for _, mutation := range []func(*model.Policy){
		func(p *model.Policy) { p.ObjectName = "public.*" },
		func(p *model.Policy) { p.ObjectName = "private.sales" },
		func(p *model.Policy) { p.ObjectType = "column" },
		func(p *model.Policy) { p.Action = "deny" },
		func(p *model.Policy) { p.Columns = &positive },
		func(p *model.Policy) { p.RelationBindingID = &positive },
		func(p *model.Policy) { p.LegacyUnrepresentable = true },
	} {
		bad := base
		mutation(&bad)
		if _, _, _, err := CompileForPolicies(sql, "agent", "ds", []model.Policy{base, bad}); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("ambiguous row policy did not close: %+v err=%v", bad, err)
		}
	}
	if got, digest, relation, err := CompileForPolicies(sql, "agent", "ds", []model.Policy{unrelated}); err != nil || got != sql || digest != "" || relation != "" {
		t.Fatalf("unrelated policy affected caller: sql=%q digest=%q relation=%q err=%v", got, digest, relation, err)
	}
}

func TestRewriteSingleTableConjoinsFilterAfterCallerOr(t *testing.T) {
	got, digest, err := RewriteSingleTable(
		"SELECT region, amount FROM public.sales WHERE amount > 0 OR region = 'east' LIMIT 2",
		"public", "sales", []string{"region = 'west'", "amount IS NOT NULL"},
	)
	if err != nil || len(digest) != 64 {
		t.Fatalf("rewrite failed: sql=%q digest=%q err=%v", got, digest, err)
	}
	parsed, err := pg_query.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	where := parsed.Stmts[0].Stmt.GetSelectStmt().WhereClause.GetBoolExpr()
	if where == nil || where.Boolop != pg_query.BoolExprType_AND_EXPR || len(where.Args) != 3 ||
		where.Args[0].GetBoolExpr() == nil || where.Args[0].GetBoolExpr().Boolop != pg_query.BoolExprType_OR_EXPR {
		t.Fatalf("caller OR escaped row predicate: %s", got)
	}
	if !strings.Contains(got, "region = 'west'") || !strings.Contains(got, "amount IS NOT NULL") {
		t.Fatalf("rewritten predicate missing: %s", got)
	}
}

func TestProbeColumnUsesValidatedRowPredicate(t *testing.T) {
	for _, item := range []struct{ filter, column string }{
		{"region = 'east'", "region"},
		{"id IS NOT NULL AND region = 'east'", "id"},
		{`"quoted name" IS NULL`, "quoted name"},
	} {
		got, err := ProbeColumn(item.filter)
		if err != nil || got != item.column {
			t.Fatalf("filter=%q got=%q err=%v", item.filter, got, err)
		}
	}
	if column, err := ProbeColumn("region = 'east' OR region = 'west'"); err != nil || column != "region" {
		t.Fatalf("bounded OR predicate failed metadata probe: column=%q err=%v", column, err)
	}
}

func TestMetadataProbeAfterRowRewriteUsesClosedBinderGrammar(t *testing.T) {
	query := `SELECT "region" FROM "agentsql_files"."agentsql_file_feac83eafebb5564a5d14c7419aab486" WHERE "region" IS NULL AND "region" IS NOT NULL LIMIT 1`
	rewritten, _, err := RewriteSingleTable(query, "agentsql_files", "agentsql_file_feac83eafebb5564a5d14c7419aab486", []string{"region = 'east'"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parser.ParsePostgresClosedSelect(rewritten); err != nil {
		t.Fatalf("closed binder cannot parse metadata probe %q: %v", rewritten, err)
	}
}

func TestPostgresVariableMembershipRewriteStaysInClosedGrammar(t *testing.T) {
	for _, filter := range []string{
		`("status" = 'paid' OR "status" = 'shipped')`,
		`("status" <> 'paid' AND "status" <> 'shipped')`,
	} {
		rewritten, _, err := RewriteSingleTable(`SELECT "status" FROM "public"."orders" LIMIT 10`,
			"public", "orders", []string{filter})
		if err != nil {
			t.Fatalf("membership rewrite rejected %q: %v", filter, err)
		}
		if _, err := parser.ParsePostgresClosedSelect(rewritten); err != nil {
			t.Fatalf("closed binder rejected membership %q: %v", rewritten, err)
		}
	}
	for _, filter := range []string{
		`"status" = 'paid' OR EXISTS (SELECT 1)`,
		`"status" = 'paid' OR "status" IN (SELECT secret FROM public.other)`,
	} {
		if _, _, err := RewriteSingleTable(`SELECT "status" FROM "public"."orders"`,
			"public", "orders", []string{filter}); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("unsafe membership accepted: %q err=%v", filter, err)
		}
	}
	rewritten, _, err := RewriteSingleTable(
		`SELECT "status" FROM "public"."orders" WHERE "status" = 'cancelled' OR "status" = 'pending'`,
		"public", "orders", []string{`("status" = 'paid' OR "status" = 'shipped')`})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := pg_query.Parse(rewritten)
	if err != nil {
		t.Fatal(err)
	}
	outer := parsed.Stmts[0].Stmt.GetSelectStmt().WhereClause.GetBoolExpr()
	if outer == nil || outer.Boolop != pg_query.BoolExprType_AND_EXPR || len(outer.Args) != 2 ||
		outer.Args[0].GetBoolExpr() == nil || outer.Args[0].GetBoolExpr().Boolop != pg_query.BoolExprType_OR_EXPR ||
		outer.Args[1].GetBoolExpr() == nil || outer.Args[1].GetBoolExpr().Boolop != pg_query.BoolExprType_OR_EXPR {
		t.Fatalf("caller OR escaped variable membership: %s", rewritten)
	}
}

func TestRewriteSingleTableRejectsAmbiguousOrNestedSources(t *testing.T) {
	cases := []string{
		"SELECT * FROM sales",
		"SELECT * FROM public.other",
		"SELECT * FROM public.sales s JOIN public.other o ON s.id = o.id",
		"SELECT (SELECT count(*) FROM public.sales) FROM public.sales",
		"SELECT * FROM public.sales WHERE EXISTS (SELECT 1 FROM public.sales)",
		"WITH s AS (SELECT * FROM public.sales) SELECT * FROM s",
		"SELECT * FROM public.sales UNION SELECT * FROM public.sales",
		"SELECT * FROM public.sales; SELECT * FROM public.sales",
		"UPDATE public.sales SET amount = 9",
	}
	for _, sql := range cases {
		t.Run(sql, func(t *testing.T) {
			if _, _, err := RewriteSingleTable(sql, "public", "sales", []string{"region = 'west'"}); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("expected fail closed, got %v", err)
			}
		})
	}
}

func TestCompilePredicateRejectsSQLExecutionAndUnboundReferences(t *testing.T) {
	bad := []string{
		"region = 'west'; DROP TABLE public.sales", "region = current_user", "region = $1",
		"region = (SELECT secret FROM public.secrets)", "lower(region) = 'west'",
		"other.region = 'west'", "region = 'west' OR 1 = 1", "region = 'west' OR EXISTS (SELECT 1)",
		"NOT (region = 'west')", "region IN ('west', 'east')", "region LIKE 'w%'",
		"region = NULL", "region = 'west'::text", "region = 'west' --comment\n OR true",
	}
	for _, filter := range bad {
		t.Run(filter, func(t *testing.T) {
			if _, err := CompilePredicate(filter); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("expected fail closed, got %v", err)
			}
		})
	}
	for _, filter := range []string{"region = 'west'", "amount >= 10", "region IS NOT NULL", "region = 'west' AND amount > 0", "region = 'west' OR region = 'east'"} {
		if _, err := CompilePredicate(filter); err != nil {
			t.Fatalf("rejected a bounded predicate %q: %v", filter, err)
		}
	}
}
