package parser

import (
	"errors"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
)

func TestOpenGaussParserNarrowReadSubset(t *testing.T) {
	approved, err := NewParser(model.DialectOpenGauss)
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`SELECT id FROM ONLY public.accounts WHERE id = 1 LIMIT 10`,
		`SELECT a.id, a.phone FROM ONLY public.accounts AS a WHERE a.id > 0 LIMIT 20`,
	} {
		t.Run(sql, func(t *testing.T) {
			ast, err := approved.Parse(sql)
			if err != nil || ast == nil || ast.Dialect != model.DialectOpenGauss || ast.StmtType != "SELECT" {
				t.Fatalf("unexpected parse result: ast=%+v err=%v", ast, err)
			}
		})
	}
	for _, sql := range []string{
		`SELECT id FROM public.accounts WHERE id=1 LIMIT 10`,
		`SELECT id FROM accounts WHERE id = 1 LIMIT 10`,
		`SELECT id FROM public.accounts LIMIT 10`,
		`SELECT id FROM public.accounts WHERE 1=1 LIMIT 10`,
		`SELECT id FROM public.accounts WHERE id=1`,
		`SELECT * FROM public.accounts WHERE id=1 LIMIT 10`,
		`SELECT now() FROM public.accounts WHERE id=1 LIMIT 10`,
		`SELECT id FROM public.accounts WHERE id=1 ORDER BY id LIMIT 10`,
		`SELECT id FROM public.accounts WHERE id=1 LIMIT 10001`,
		`SELECT id FROM public.accounts WHERE id=1 LIMIT ALL`,
		`SELECT id FROM public.accounts WHERE id=1 LIMIT 10 OFFSET 1`,
		`SELECT id FROM public.accounts WHERE id=1 LIMIT $1`,
		`SELECT oid FROM pg_catalog.pg_class WHERE oid=1 LIMIT 10`,
		`SELECT oid FROM ONLY pg_toast.some_table WHERE oid=1 LIMIT 10`,
		`SELECT table_name FROM information_schema.tables WHERE table_name='accounts' LIMIT 10`,
		`SELECT id FROM public.accounts WHERE id=1 LIMIT 10 FOR UPDATE`,
		`SELECT id INTO public.backup FROM public.accounts WHERE id=1 LIMIT 10`,
		`WITH x AS (SELECT id FROM public.accounts) SELECT id FROM x WHERE id=1 LIMIT 10`,
		`SELECT a.id FROM public.accounts a JOIN public.other b ON a.id=b.id WHERE a.id=1 LIMIT 10`,
		`UPDATE public.accounts SET phone='changed' WHERE id=1`,
		`SELECT id FROM public.accounts WHERE id=1 LIMIT 10; DELETE FROM public.accounts`,
	} {
		t.Run(sql, func(t *testing.T) {
			ast, err := approved.Parse(sql)
			if !errors.Is(err, ErrUnparseable) || ast == nil || ast.Dialect != model.DialectOpenGauss {
				t.Fatalf("expected fail-closed parse, got ast=%+v err=%v", ast, err)
			}
		})
	}
}
