package parser

import (
	"errors"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
)

func TestVastbaseParserNarrowReadSubset(t *testing.T) {
	approved, err := NewParser(model.DialectVastbase)
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`SELECT id FROM public.accounts WHERE id = 1 LIMIT 10`,
		`SELECT a.id, a.name FROM public.accounts AS a WHERE a.id > 0 LIMIT 20`,
	} {
		t.Run(sql, func(t *testing.T) {
			ast, err := approved.Parse(sql)
			if err != nil {
				t.Fatal(err)
			}
			if ast.Dialect != model.DialectVastbase || ast.StmtType != "SELECT" {
				t.Fatalf("unexpected AST: %+v", ast)
			}
		})
	}
	for _, sql := range []string{
		`SELECT id FROM accounts WHERE id = 1 LIMIT 10`,
		`SELECT id FROM public.accounts LIMIT 10`,
		`SELECT id FROM public.accounts WHERE 1=1 LIMIT 10`,
		`SELECT id FROM public.accounts WHERE id=1`,
		`SELECT * FROM public.accounts WHERE id=1 LIMIT 10`,
		`SELECT now() FROM public.accounts WHERE id=1 LIMIT 10`,
		`SELECT id FROM public.accounts WHERE id=1 ORDER BY id LIMIT 10`,
		`SELECT id FROM public.accounts WHERE id=1 LIMIT 10 /* note */`,
		`SELECT id FROM public.accounts WHERE id=1 LIMIT 10 FOR UPDATE`,
		`SELECT id INTO public.backup FROM public.accounts WHERE id=1 LIMIT 10`,
		`WITH x AS (SELECT id FROM public.accounts) SELECT id FROM x WHERE id=1 LIMIT 10`,
		`SELECT a.id FROM public.accounts a JOIN public.other b ON a.id=b.id WHERE a.id=1 LIMIT 10`,
		`UPDATE public.accounts SET name='x' WHERE id=1`,
		`SELECT id FROM public.accounts WHERE id=1 LIMIT 10; DELETE FROM public.accounts`,
	} {
		t.Run(sql, func(t *testing.T) {
			ast, err := approved.Parse(sql)
			if !errors.Is(err, ErrUnparseable) || ast == nil || ast.Dialect != model.DialectVastbase {
				t.Fatalf("expected Vastbase fail-closed parse, got ast=%+v err=%v", ast, err)
			}
		})
	}
}
