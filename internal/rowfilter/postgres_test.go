package rowfilter

import (
	"errors"
	"strings"
	"testing"

	pg_query "github.com/pganalyze/pg_query_go/v5"
)

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
		"other.region = 'west'", "region = 'west' OR amount > 0",
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
	for _, filter := range []string{"region = 'west'", "amount >= 10", "region IS NOT NULL", "region = 'west' AND amount > 0"} {
		if _, err := CompilePredicate(filter); err != nil {
			t.Fatalf("rejected a bounded predicate %q: %v", filter, err)
		}
	}
}
