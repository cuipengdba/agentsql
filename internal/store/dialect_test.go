package store

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseDialect(t *testing.T) {
	t.Parallel()

	dialect, err := ParseDialect("  PoStGrEs  ")
	require.NoError(t, err)
	require.Equal(t, DialectPostgres, dialect)

	dialect, err = ParseDialect("SQLITE")
	require.NoError(t, err)
	require.Equal(t, DialectSQLite, dialect)

	_, err = ParseDialect("mysql")
	require.Error(t, err)
}

func TestRebind(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		dialect Dialect
		query   string
		want    string
		wantErr bool
	}{
		{
			name:    "ordinary parameters preserve order",
			dialect: DialectPostgres,
			query:   "SELECT * FROM agents WHERE id = ? AND status = ? AND owner = ?",
			want:    "SELECT * FROM agents WHERE id = $1 AND status = $2 AND owner = $3",
		},
		{
			name:    "single quoted strings and escaped quotes",
			dialect: DialectPostgres,
			query:   "SELECT '?', 'it''s ?' WHERE id = ?",
			want:    "SELECT '?', 'it''s ?' WHERE id = $1",
		},
		{
			name:    "double quoted identifiers and escaped quotes",
			dialect: DialectPostgres,
			query:   `SELECT "?", "odd""?name" FROM agents WHERE id = ?`,
			want:    `SELECT "?", "odd""?name" FROM agents WHERE id = $1`,
		},
		{
			name:    "line and block comments",
			dialect: DialectPostgres,
			query:   "SELECT ? -- leave ? alone\n/* outer ? /* nested ? */ done ? */ WHERE id = ?",
			want:    "SELECT $1 -- leave ? alone\n/* outer ? /* nested ? */ done ? */ WHERE id = $2",
		},
		{
			name:    "dollar quoted strings",
			dialect: DialectPostgres,
			query:   "SELECT $$?$$, $tag$body ? $tag$, $tag_2$more ?$tag_2$, ?",
			want:    "SELECT $$?$$, $tag$body ? $tag$, $tag_2$more ?$tag_2$, $1",
		},
		{
			name:    "consecutive placeholders",
			dialect: DialectPostgres,
			query:   "SELECT ??",
			want:    "SELECT $1$2",
		},
		{
			name:    "no placeholders",
			dialect: DialectPostgres,
			query:   "SELECT 1",
			want:    "SELECT 1",
		},
		{
			name:    "sqlite returns bytes unchanged",
			dialect: DialectSQLite,
			query:   "SELECT ? -- ?\r\nWHERE value = '？'",
			want:    "SELECT ? -- ?\r\nWHERE value = '？'",
		},
		{
			name:    "unknown dialect fails closed",
			dialect: Dialect("mysql"),
			query:   "SELECT ?",
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := rebind(test.dialect, test.query)
			if test.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}
}
