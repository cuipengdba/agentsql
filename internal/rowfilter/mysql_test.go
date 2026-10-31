package rowfilter

import (
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/parser"
	"github.com/stretchr/testify/require"
)

func TestRewriteMySQLSingleTableConjoinsOriginalOrAndAllRules(t *testing.T) {
	raw := "SELECT status FROM agentsql_demo.orders WHERE status = 'paid' OR status = 'shipped' ORDER BY orders.status LIMIT 10"
	rewritten, digest, err := RewriteMySQLSingleTable(raw, "agentsql_demo", "orders", []string{
		"`status` = 'paid'", "`id` > '5'",
	})
	require.NoError(t, err)
	require.NotEmpty(t, digest)
	require.Contains(t, strings.ToLower(rewritten), "where (`status` = 'paid' or `status` = 'shipped') and")
	shape, err := parser.ParseMySQLColumnSelectShape(rewritten)
	require.NoError(t, err)
	require.Equal(t, "agentsql_demo", shape.Schema)
	require.Equal(t, "orders", shape.Table)
	require.Len(t, shape.Uses, 6)
}

func TestRewriteMySQLSingleTableAcceptsBoundedOrAndRejectsUnsafeBranches(t *testing.T) {
	base := "SELECT status FROM agentsql_demo.orders"
	rewritten, _, err := RewriteMySQLSingleTable(base, "agentsql_demo", "orders", []string{
		"(`status` = 'paid' OR `status` = 'shipped') AND `id` > '5'",
	})
	require.NoError(t, err)
	require.Contains(t, strings.ToLower(rewritten), "(`status` = 'paid' or `status` = 'shipped') and id > '5'")
	for _, filter := range []string{
		"`status` = 'paid' OR 1 = 1",
		"`status` = 'paid' OR sleep(1) = 1",
		"`status` = 'paid' OR (SELECT 1) = 1",
	} {
		_, _, err := RewriteMySQLSingleTable(base, "agentsql_demo", "orders", []string{filter})
		require.ErrorIs(t, err, ErrUnsupported, filter)
	}
}

func TestRewriteMySQLSingleTableRejectsUnprovenScopeAndShapes(t *testing.T) {
	for _, query := range []string{
		"SELECT status FROM orders",
		"SELECT status FROM other.orders",
		"SELECT status FROM agentsql_demo.other",
		"SELECT * FROM agentsql_demo.orders",
		"SELECT status FROM agentsql_demo.orders JOIN agentsql_demo.users ON true",
		"SELECT COUNT(*) FROM agentsql_demo.orders",
		"SELECT status FROM agentsql_demo.orders UNION SELECT status FROM agentsql_demo.orders",
		"SELECT status FROM agentsql_demo.orders WHERE id IN (SELECT id FROM agentsql_demo.orders)",
		"SELECT status FROM agentsql_demo.orders; DELETE FROM agentsql_demo.orders",
	} {
		t.Run(query, func(t *testing.T) {
			_, _, err := RewriteMySQLSingleTable(query, "agentsql_demo", "orders", []string{"`status` = 'paid'"})
			require.ErrorIs(t, err, ErrUnsupported)
		})
	}
	for _, filter := range []string{"", "`status` = 'paid' OR 1=1", "`status` = 'paid'; SELECT 1", "`status` = 'paid' -- x", "`status` = sleep(1)"} {
		t.Run(filter, func(t *testing.T) {
			_, _, err := RewriteMySQLSingleTable("SELECT status FROM agentsql_demo.orders", "agentsql_demo", "orders", []string{filter})
			require.ErrorIs(t, err, ErrUnsupported)
		})
	}
}
