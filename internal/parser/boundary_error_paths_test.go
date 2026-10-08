package parser

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestParserMalformedBoundaryErrorsRemainStable(t *testing.T) {
	for _, dialect := range []model.DBDialect{mysqlDialect, dmDialect, sqlserverDialect} {
		t.Run(string(dialect), func(t *testing.T) {
			approved, err := NewParser(dialect)
			require.NoError(t, err)
			for _, test := range []struct {
				name string
				sql  string
			}{
				{name: "empty", sql: ""},
				{name: "whitespace", sql: " \t\r\n "},
				{name: "unclosed string", sql: "SELECT 'unfinished"},
				{name: "empty order by", sql: "SELECT id FROM customers ORDER BY"},
				{name: "stacked statement", sql: "SELECT id FROM customers; DROP TABLE customers"},
			} {
				t.Run(test.name, func(t *testing.T) {
					var firstError string
					for attempt := 0; attempt < 3; attempt++ {
						var ast *model.AST
						var parseErr error
						require.NotPanics(t, func() { ast, parseErr = approved.Parse(test.sql) })
						require.NotNil(t, ast)
						require.Equal(t, dialect, ast.Dialect)
						require.Equal(t, test.sql, ast.RawSQL)
						require.ErrorIs(t, parseErr, ErrUnparseable)
						if attempt == 0 {
							firstError = parseErr.Error()
						} else {
							require.Equal(t, firstError, parseErr.Error())
						}
					}
				})
			}
			// A failed parse must not poison the next request on the same parser.
			ast, err := approved.Parse("SELECT id FROM customers WHERE id = 1")
			require.NoError(t, err)
			require.Equal(t, model.StmtType("SELECT"), ast.StmtType)
			require.False(t, ast.IsMulti)
		})
	}
}

func TestOracleCompatibleMalformedBytesAndSizeFailClosed(t *testing.T) {
	for _, dialect := range []model.DBDialect{dmDialect, sqlserverDialect} {
		t.Run(string(dialect), func(t *testing.T) {
			approved, err := NewParser(dialect)
			require.NoError(t, err)
			for _, sql := range []string{
				"SELECT id FROM customers WHERE id = '\xff'",
				"SELECT id FROM customers /* unfinished",
				strings.Repeat("x", oracleCompatibleMaxSQLBytes+1),
			} {
				ast, parseErr := approved.Parse(sql)
				require.NotNil(t, ast)
				require.Equal(t, sql, ast.RawSQL)
				require.True(t, errors.Is(parseErr, ErrUnparseable), fmt.Sprintf("%v", parseErr))
			}
		})
	}
}

func TestParserConcurrentDialectFailuresStayIsolated(t *testing.T) {
	const attempts = 12
	var group sync.WaitGroup
	start := make(chan struct{})
	type outcome struct {
		dialect model.DBDialect
		ast     *model.AST
		err     error
	}
	results := make(chan outcome, attempts*3)
	for _, dialect := range []model.DBDialect{mysqlDialect, dmDialect, sqlserverDialect} {
		for attempt := 0; attempt < attempts; attempt++ {
			group.Add(1)
			go func(dialect model.DBDialect) {
				defer group.Done()
				approved, err := NewParser(dialect)
				if err != nil {
					results <- outcome{dialect: dialect, err: err}
					return
				}
				<-start
				ast, err := approved.Parse("SELECT id FROM customers ORDER BY")
				results <- outcome{dialect: dialect, ast: ast, err: err}
			}(dialect)
		}
	}
	close(start)
	group.Wait()
	close(results)
	for result := range results {
		require.NotNil(t, result.ast)
		require.Equal(t, result.dialect, result.ast.Dialect)
		require.ErrorIs(t, result.err, ErrUnparseable)
	}
}
