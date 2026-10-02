package parser

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestPostgresBoundaryEmptyAndInvalidInputFailsClosed(t *testing.T) {
	approved := &postgresParser{}
	tests := []struct {
		name string
		sql  string
	}{
		{name: "empty", sql: ""},
		{name: "whitespace only", sql: " \t\r\n"},
		{name: "truncated expression", sql: "SELECT ("},
		{name: "misspelled keyword", sql: "SELECT * FRM customers"},
		{name: "unknown syntax", sql: "FROBULATE customers"},
		{name: "invalid byte", sql: "\x00SELECT 1"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var ast *model.AST
			var err error
			require.NotPanics(t, func() {
				ast, err = approved.Parse(test.sql)
			})
			require.NotNil(t, ast)
			require.Equal(t, postgresDialect, ast.Dialect)
			require.Equal(t, test.sql, ast.RawSQL)
			require.Error(t, err)
			require.True(t, errors.Is(err, ErrUnparseable), err)
		})
	}
}

func TestPostgresBoundaryNullASTFailsClosed(t *testing.T) {
	document, err := decodePostgresDocument(`{"stmts":[{"stmt":{"SelectStmt":null}}]}`)
	require.NoError(t, err)
	require.Len(t, document.Statements, 1)

	nodeType, node, err := postgresRoot(document.Statements[0])
	require.Empty(t, nodeType)
	require.Nil(t, node)
	require.ErrorContains(t, err, "root node is null")
}

func TestPostgresBoundaryLongAndDeepInput(t *testing.T) {
	approved := &postgresParser{}

	longLiteral := strings.Repeat("多字节DROP关键字", 16*1024)
	longSQL := "SELECT '" + longLiteral + "' AS payload FROM public.events"
	longAST, err := approved.Parse(longSQL)
	require.NoError(t, err)
	require.NotNil(t, longAST)
	require.Equal(t, model.StmtType("SELECT"), longAST.StmtType)
	require.Equal(t, []model.ObjectRef{{Schema: "public", Table: "events"}}, longAST.Tables)
	require.NotContains(t, longAST.Normalized, longLiteral)

	deepSQL := projectionLineageDepthSQL(postgresDialect, lineageMaxDepth+1)
	deepAST, err := approved.Parse(deepSQL)
	require.NotNil(t, deepAST)
	require.Equal(t, deepSQL, deepAST.RawSQL)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrUnparseable), err)
}

func TestPostgresBoundaryCommentsStringsCaseWhitespaceAndUnicode(t *testing.T) {
	approved := &postgresParser{}
	keywordLiteral := "DROP TABLE hidden; -- SELECT UPDATE"
	sql := "  SeLeCt\n \"客\".\"电话\", '" + keywordLiteral + "' AS \"说明\" " +
		"FrOm \"业务\".\"客户\" AS \"客\" /* DELETE FROM audit */  "

	ast, err := approved.Parse(sql)
	require.NoError(t, err)
	require.Equal(t, model.StmtType("SELECT"), ast.StmtType)
	require.False(t, ast.IsMulti)
	require.Equal(t, []model.ObjectRef{{Schema: "业务", Table: "客户", Alias: "客"}}, ast.Tables)
	require.Contains(t, ast.Columns, "电话")
	require.Contains(t, ast.Operations, "SELECT")
	require.Contains(t, ast.Operations, sqlCommentOperation)
	require.NotContains(t, ast.Operations, "DROP TABLE")
	require.NotContains(t, ast.Operations, "DELETE")
	require.NotContains(t, ast.Normalized, keywordLiteral)

	variants := []string{
		"SELECT c.phone FROM public.customers AS c",
		" \n SeLeCt\tc.phone\nFROM\tpublic.customers\tc \n",
	}
	first, err := approved.Parse(variants[0])
	require.NoError(t, err)
	second, err := approved.Parse(variants[1])
	require.NoError(t, err)
	require.Equal(t, first.StmtType, second.StmtType)
	require.Equal(t, first.Tables, second.Tables)
	require.Equal(t, first.Columns, second.Columns)
	require.Equal(t, first.DirectProjections, second.DirectProjections)
	require.Equal(t, first.ProjectionLineages, second.ProjectionLineages)
}

func TestPostgresBoundaryConcurrentParseIsConsistent(t *testing.T) {
	const (
		workers    = 12
		iterations = 25
	)
	approved := &postgresParser{}
	sql := projectionLineageSetSQL(postgresDialect, 4, 4)
	expected, err := approved.Parse(sql)
	require.NoError(t, err)

	type parseResult struct {
		ast *model.AST
		err error
	}
	results := make(chan parseResult, workers*iterations)
	var group sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for iteration := 0; iteration < iterations; iteration++ {
				ast, parseErr := approved.Parse(sql)
				results <- parseResult{ast: ast, err: parseErr}
			}
		}()
	}
	group.Wait()
	close(results)

	count := 0
	for result := range results {
		count++
		require.NoError(t, result.err)
		require.Equal(t, expected, result.ast)
	}
	require.Equal(t, workers*iterations, count)
}
