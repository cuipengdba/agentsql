package parser

import (
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestPostgresOneMiBLiteralIsRedacted(t *testing.T) {
	secret := strings.Repeat("private_value_", (1<<20)/len("private_value_")+1)
	sql := "SELECT '" + secret + "' AS payload FROM public.events"
	ast, err := (&postgresParser{}).Parse(sql)
	require.NoError(t, err)
	require.Equal(t, model.StmtType("SELECT"), ast.StmtType)
	require.Equal(t, sql, ast.RawSQL)
	require.NotContains(t, ast.Normalized, "private_value_")
	require.Contains(t, ast.Normalized, "$1")
}

func TestPostgresSemicolonInLiteralAndMultipleStatements(t *testing.T) {
	approved := &postgresParser{}
	single, err := approved.Parse("SELECT ';' AS marker")
	require.NoError(t, err)
	require.False(t, single.IsMulti)

	multiSQL := "SELECT 1; SELECT 2"
	multiple, err := approved.Parse(multiSQL)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrUnparseable)
	require.Equal(t, multiSQL, multiple.RawSQL)
	require.True(t, multiple.IsMulti)
}
