package main

import (
	"fmt"
	"os"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/parser"
)

func main() {
	p, err := parser.NewParser(model.DialectPostgres)
	if err != nil {
		panic(err)
	}
	ast, err := p.Parse("SELECT id, name, phone FROM public.agentsql_batch70_verify WHERE id=1 LIMIT 10")
	if err != nil {
		panic(err)
	}
	for i, expected := range []string{"id", "name", "phone"} {
		if len(ast.ProjectionLineages) <= i || len(ast.ProjectionLineages[i].Arms) != 1 ||
			len(ast.ProjectionLineages[i].Arms[0].Dependencies) != 1 {
			fmt.Fprintln(os.Stderr, "lineage shape mismatch", i)
			os.Exit(1)
		}
		origin := ast.ProjectionLineages[i].Arms[0].Dependencies[0].Origin
		if origin.Relation.Schema != "public" || origin.Relation.Table != "agentsql_batch70_verify" || origin.Column != expected {
			fmt.Fprintln(os.Stderr, "lineage origin mismatch", i)
			os.Exit(1)
		}
		fmt.Printf("ProjectionLineages[%d]=%s <- %s.%s.%s\n", i,
			ast.ProjectionLineages[i].OutputName, origin.Relation.Schema, origin.Relation.Table, origin.Column)
	}
}
