package parser

import (
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestDirectProjectionSourceScopeAndJoinMatrix(t *testing.T) {
	physical := func(table string) model.ObjectRef { return model.ObjectRef{Table: table} }
	unresolved := model.ObjectRef{}
	tests := []struct {
		name     string
		postgres string
		mysql    string
		expected []model.ObjectRef
		columns  []string
	}{
		{
			name:     "unused CTE does not pollute root scope",
			postgres: "WITH unused AS (SELECT id FROM orders) SELECT phone FROM customers",
			mysql:    "WITH unused AS (SELECT id FROM orders) SELECT phone FROM customers",
			columns:  []string{"phone"}, expected: []model.ObjectRef{physical("customers")},
		},
		{
			name:     "EXISTS subquery does not pollute root scope",
			postgres: "SELECT phone FROM customers WHERE EXISTS (SELECT 1 FROM orders)",
			mysql:    "SELECT phone FROM customers WHERE EXISTS (SELECT 1 FROM orders)",
			columns:  []string{"phone"}, expected: []model.ObjectRef{physical("customers")},
		},
		{
			name:     "scalar subquery does not pollute root scope",
			postgres: "SELECT phone, (SELECT id FROM orders) AS order_id FROM customers",
			mysql:    "SELECT phone, (SELECT id FROM orders) AS order_id FROM customers",
			columns:  []string{"phone"}, expected: []model.ObjectRef{physical("customers")},
		},
		{
			name:     "physical plus derived makes bare column unresolved",
			postgres: "SELECT phone FROM customers, (SELECT id FROM orders) x",
			mysql:    "SELECT phone FROM customers, (SELECT id FROM orders) x",
			columns:  []string{"phone"}, expected: []model.ObjectRef{unresolved},
		},
		{
			name:     "nested derived remains non physical",
			postgres: "SELECT x.phone FROM (SELECT y.phone FROM (SELECT phone FROM customers) y) x",
			mysql:    "SELECT x.phone FROM (SELECT y.phone FROM (SELECT phone FROM customers) y) x",
			columns:  []string{"phone"}, expected: []model.ObjectRef{unresolved},
		},
		{
			name:     "single derived bare column is unresolved",
			postgres: "SELECT phone FROM (SELECT phone FROM customers) x",
			mysql:    "SELECT phone FROM (SELECT phone FROM customers) x",
			columns:  []string{"phone"}, expected: []model.ObjectRef{unresolved},
		},
		{
			name:     "CTE name shadows physical relation name",
			postgres: "WITH customers AS (SELECT phone FROM archive) SELECT customers.phone FROM customers",
			mysql:    "WITH customers AS (SELECT phone FROM archive) SELECT customers.phone FROM customers",
			columns:  []string{"phone"}, expected: []model.ObjectRef{unresolved},
		},
		{
			name:     "physical plus table function makes bare column unresolved",
			postgres: "SELECT phone, g.n FROM customers, generate_series(1, 2) AS g(n)",
			mysql:    "SELECT phone, jt.n FROM customers, JSON_TABLE('[1]', '$[*]' COLUMNS(n INT PATH '$')) AS jt",
			columns:  []string{"phone", "n"}, expected: []model.ObjectRef{unresolved, unresolved},
		},
		{
			name:     "aliases resolve same named columns",
			postgres: "SELECT c.phone, o.phone FROM customers c JOIN orders o ON c.id = o.id",
			mysql:    "SELECT c.phone, o.phone FROM customers c JOIN orders o ON c.id = o.id",
			columns:  []string{"phone", "phone"}, expected: []model.ObjectRef{physical("customers"), physical("orders")},
		},
		{
			name:     "bare column across physical join is unresolved",
			postgres: "SELECT phone FROM customers c JOIN orders o ON c.id = o.id",
			mysql:    "SELECT phone FROM customers c JOIN orders o ON c.id = o.id",
			columns:  []string{"phone"}, expected: []model.ObjectRef{unresolved},
		},
		{
			name:     "USING merged column unresolved but qualifiers resolve",
			postgres: "SELECT id, c.id, o.id FROM customers c JOIN orders o USING (id)",
			mysql:    "SELECT id, c.id, o.id FROM customers c JOIN orders o USING (id)",
			columns:  []string{"id", "id", "id"}, expected: []model.ObjectRef{unresolved, physical("customers"), physical("orders")},
		},
		{
			name:     "NATURAL JOIN bare column is unresolved",
			postgres: "SELECT id FROM customers c NATURAL JOIN orders o",
			mysql:    "SELECT id FROM customers c NATURAL JOIN orders o",
			columns:  []string{"id"}, expected: []model.ObjectRef{unresolved},
		},
		{
			name:     "LEFT JOIN does not change qualified ownership",
			postgres: "SELECT c.phone, o.phone FROM customers c LEFT JOIN orders o ON c.id = o.id",
			mysql:    "SELECT c.phone, o.phone FROM customers c LEFT JOIN orders o ON c.id = o.id",
			columns:  []string{"phone", "phone"}, expected: []model.ObjectRef{physical("customers"), physical("orders")},
		},
		{
			name:     "RIGHT JOIN does not change qualified ownership",
			postgres: "SELECT c.phone, o.phone FROM customers c RIGHT JOIN orders o ON c.id = o.id",
			mysql:    "SELECT c.phone, o.phone FROM customers c RIGHT JOIN orders o ON c.id = o.id",
			columns:  []string{"phone", "phone"}, expected: []model.ObjectRef{physical("customers"), physical("orders")},
		},
		{
			name:     "one physical one derived keeps qualified physical only",
			postgres: "SELECT phone, c.phone, x.phone FROM customers c JOIN (SELECT phone FROM orders) x ON true",
			mysql:    "SELECT phone, c.phone, x.phone FROM customers c JOIN (SELECT phone FROM orders) x ON 1 = 1",
			columns:  []string{"phone", "phone", "phone"}, expected: []model.ObjectRef{unresolved, physical("customers"), unresolved},
		},
		{
			name:     "LATERAL derived is non physical while outer qualifier resolves",
			postgres: "SELECT x.phone, c.phone FROM customers c JOIN LATERAL (SELECT c.phone) x ON true",
			mysql:    "SELECT x.phone, c.phone FROM customers c JOIN LATERAL (SELECT c.phone) x ON 1 = 1",
			columns:  []string{"phone", "phone"}, expected: []model.ObjectRef{unresolved, physical("customers")},
		},
		{
			name:     "alias takes precedence over same named physical table",
			postgres: "SELECT orders.phone FROM customers orders JOIN orders ON orders.id = orders.id",
			mysql:    "SELECT orders.phone FROM customers orders JOIN orders ON orders.id = orders.id",
			columns:  []string{"phone"}, expected: []model.ObjectRef{physical("customers")},
		},
		{
			name:     "non physical alias takes precedence over same named physical table",
			postgres: "SELECT orders.phone FROM (SELECT phone FROM archive) orders JOIN orders ON true",
			mysql:    "SELECT orders.phone FROM (SELECT phone FROM archive) orders JOIN orders ON 1 = 1",
			columns:  []string{"phone"}, expected: []model.ObjectRef{unresolved},
		},
		{
			name:     "original table name is hidden by alias",
			postgres: "SELECT customers.phone FROM customers c",
			mysql:    "SELECT customers.phone FROM customers c",
			columns:  []string{"phone"}, expected: []model.ObjectRef{unresolved},
		},
		{
			name:     "schema qualified original table name is hidden by alias",
			postgres: "SELECT crm.customers.phone FROM crm.customers c",
			mysql:    "SELECT crm.customers.phone FROM crm.customers c",
			columns:  []string{"phone"}, expected: []model.ObjectRef{unresolved},
		},
		{
			name:     "self join aliases resolve to same physical table",
			postgres: "SELECT c1.phone, c2.phone FROM customers c1 JOIN customers c2 ON c1.id = c2.id",
			mysql:    "SELECT c1.phone, c2.phone FROM customers c1 JOIN customers c2 ON c1.id = c2.id",
			columns:  []string{"phone", "phone"}, expected: []model.ObjectRef{physical("customers"), physical("customers")},
		},
	}

	for _, dialect := range []model.DBDialect{postgresDialect, mysqlDialect} {
		t.Run(string(dialect), func(t *testing.T) {
			approved, err := NewParser(dialect)
			require.NoError(t, err)
			for _, test := range tests {
				t.Run(test.name, func(t *testing.T) {
					sql := test.postgres
					if dialect == mysqlDialect {
						sql = test.mysql
					}
					ast, err := approved.Parse(sql)
					require.NoError(t, err)
					requireProjectionColumnsAndSources(t, ast.DirectProjections, test.columns, test.expected)
				})
			}
		})
	}
}

func TestDirectProjectionSourceCTEAndDerivedMatrix(t *testing.T) {
	tests := []struct {
		name     string
		postgres string
		mysql    string
		columns  []string
	}{
		{
			name:     "CTE output column alias is non physical",
			postgres: "WITH x(mobile) AS (SELECT phone FROM customers) SELECT x.mobile FROM x",
			mysql:    "WITH x(mobile) AS (SELECT phone FROM customers) SELECT x.mobile FROM x",
			columns:  []string{"mobile"},
		},
		{
			name:     "derived output column alias is non physical",
			postgres: "SELECT x.mobile FROM (SELECT phone FROM customers) x(mobile)",
			mysql:    "SELECT x.mobile FROM (SELECT phone FROM customers) x(mobile)",
			columns:  []string{"mobile"},
		},
		{
			name:     "recursive CTE is non physical",
			postgres: "WITH RECURSIVE x(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM x WHERE n < 2) SELECT x.n FROM x",
			mysql:    "WITH RECURSIVE x(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM x WHERE n < 2) SELECT x.n FROM x",
			columns:  []string{"n"},
		},
		{
			name:     "CTE containing join remains one non physical outer source",
			postgres: "WITH x AS (SELECT c.phone FROM customers c JOIN orders o ON c.id = o.id) SELECT x.phone FROM x",
			mysql:    "WITH x AS (SELECT c.phone FROM customers c JOIN orders o ON c.id = o.id) SELECT x.phone FROM x",
			columns:  []string{"phone"},
		},
	}
	for _, dialect := range []model.DBDialect{postgresDialect, mysqlDialect} {
		t.Run(string(dialect), func(t *testing.T) {
			approved, err := NewParser(dialect)
			require.NoError(t, err)
			for _, test := range tests {
				t.Run(test.name, func(t *testing.T) {
					sql := test.postgres
					if dialect == mysqlDialect {
						sql = test.mysql
					}
					ast, err := approved.Parse(sql)
					require.NoError(t, err)
					expected := make([]model.ObjectRef, len(test.columns))
					requireProjectionColumnsAndSources(t, ast.DirectProjections, test.columns, expected)
				})
			}
		})
	}
}

func TestDirectProjectionSourceStarAndResultShapeMatrix(t *testing.T) {
	tests := []struct {
		name             string
		sql              string
		expected         []model.DirectProjectionRef
		mysqlUnsupported bool
	}{
		{name: "single star", sql: "SELECT * FROM customers"},
		{name: "qualified star", sql: "SELECT c.* FROM customers c"},
		{name: "join qualified star", sql: "SELECT c.* FROM customers c JOIN orders o ON c.id = o.id"},
		{
			name: "direct columns around star keep safe positions",
			sql:  "SELECT id, *, phone FROM customers",
			expected: []model.DirectProjectionRef{
				{Column: "id", Offset: 0, Source: model.ObjectRef{Table: "customers"}},
				{Column: "phone", Offset: 0, FromEnd: true, Source: model.ObjectRef{Table: "customers"}},
			},
		},
		{
			name: "multiple stars omit unsafe middle direct column",
			sql:  "SELECT id, *, phone, customers.*, email FROM customers",
			expected: []model.DirectProjectionRef{
				{Column: "id", Offset: 0, Source: model.ObjectRef{Table: "customers"}},
				{Column: "email", Offset: 0, FromEnd: true, Source: model.ObjectRef{Table: "customers"}},
			},
		},
		{name: "UNION", sql: "SELECT phone FROM customers UNION SELECT phone FROM orders"},
		{name: "INTERSECT", sql: "SELECT phone FROM customers INTERSECT SELECT phone FROM orders", mysqlUnsupported: true},
		{name: "EXCEPT", sql: "SELECT phone FROM customers EXCEPT SELECT phone FROM orders", mysqlUnsupported: true},
	}
	for _, dialect := range []model.DBDialect{postgresDialect, mysqlDialect} {
		t.Run(string(dialect), func(t *testing.T) {
			approved, err := NewParser(dialect)
			require.NoError(t, err)
			for _, test := range tests {
				t.Run(test.name, func(t *testing.T) {
					ast, err := approved.Parse(test.sql)
					if dialect == mysqlDialect && test.mysqlUnsupported {
						require.Error(t, err)
						return
					}
					require.NoError(t, err)
					require.Equal(t, test.expected, ast.DirectProjections)
				})
			}
		})
	}
}

func TestDirectProjectionSourceDialectIdentifiersAndStatements(t *testing.T) {
	t.Run("postgres", func(t *testing.T) {
		approved, err := NewParser(postgresDialect)
		require.NoError(t, err)
		tests := []struct {
			name    string
			sql     string
			columns []string
			sources []model.ObjectRef
		}{
			{name: "unquoted lower case", sql: "SELECT customers.phone FROM customers", columns: []string{"phone"}, sources: []model.ObjectRef{{Table: "customers"}}},
			{name: "quoted lower case", sql: `SELECT "customers".phone FROM "customers"`, columns: []string{"phone"}, sources: []model.ObjectRef{{Table: "customers"}}},
			{name: "quoted mixed case", sql: `SELECT "Customers".phone FROM "Customers"`, columns: []string{"phone"}, sources: []model.ObjectRef{{Table: "Customers"}}},
			{name: "quoted table case mismatch is unresolved", sql: `SELECT "Customers".phone FROM customers`, columns: []string{"phone"}, sources: []model.ObjectRef{{}}},
			{name: "unicode dotted spaced and quoted identifiers", sql: `SELECT "模式.一"."客户 表"."电""话" FROM "模式.一"."客户 表"`, columns: []string{`电"话`}, sources: []model.ObjectRef{{Schema: "模式.一", Table: "客户 表"}}},
			{name: "same table name across schemas", sql: "SELECT a.phone, b.phone FROM crm.customers a JOIN archive.customers b ON a.id = b.id", columns: []string{"phone", "phone"}, sources: []model.ObjectRef{{Schema: "crm", Table: "customers"}, {Schema: "archive", Table: "customers"}}},
			{name: "explicit schemas disambiguate same table name", sql: "SELECT crm.customers.phone, archive.customers.phone FROM crm.customers JOIN archive.customers ON true", columns: []string{"phone", "phone"}, sources: []model.ObjectRef{{Schema: "crm", Table: "customers"}, {Schema: "archive", Table: "customers"}}},
			{name: "FULL JOIN qualified ownership", sql: "SELECT c.phone, o.phone FROM customers c FULL JOIN orders o ON c.id = o.id", columns: []string{"phone", "phone"}, sources: []model.ObjectRef{{Table: "customers"}, {Table: "orders"}}},
			{name: "duplicate result names", sql: "SELECT c.phone AS value, o.phone AS value FROM customers c JOIN orders o ON c.id = o.id", columns: []string{"phone", "phone"}, sources: []model.ObjectRef{{Table: "customers"}, {Table: "orders"}}},
			{name: "DISTINCT direct column", sql: "SELECT DISTINCT phone FROM customers", columns: []string{"phone"}, sources: []model.ObjectRef{{Table: "customers"}}},
			{name: "view reference is physical by parser contract", sql: "SELECT phone FROM customer_view", columns: []string{"phone"}, sources: []model.ObjectRef{{Table: "customer_view"}}},
			{name: "materialized view reference is physical by parser contract", sql: "SELECT phone FROM customer_materialized", columns: []string{"phone"}, sources: []model.ObjectRef{{Table: "customer_materialized"}}},
			{name: "foreign table reference is physical by parser contract", sql: "SELECT phone FROM foreign_customers", columns: []string{"phone"}, sources: []model.ObjectRef{{Table: "foreign_customers"}}},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				ast, err := approved.Parse(test.sql)
				require.NoError(t, err)
				requireProjectionColumnsAndSources(t, ast.DirectProjections, test.columns, test.sources)
			})
		}

		noProjection := []struct{ name, sql string }{
			{name: "TABLE", sql: "TABLE customers"},
			{name: "VALUES", sql: "VALUES (1), (2)"},
			{name: "INSERT RETURNING", sql: "INSERT INTO customers(phone) VALUES ('x') RETURNING phone"},
			{name: "UPDATE RETURNING", sql: "UPDATE customers SET phone = 'x' RETURNING phone"},
			{name: "DELETE RETURNING", sql: "DELETE FROM customers RETURNING phone"},
		}
		for _, test := range noProjection {
			t.Run(test.name, func(t *testing.T) {
				ast, err := approved.Parse(test.sql)
				require.NoError(t, err)
				require.Nil(t, ast.DirectProjections)
			})
		}
	})

	t.Run("mysql", func(t *testing.T) {
		approved, err := NewParser(mysqlDialect)
		require.NoError(t, err)
		tests := []struct {
			name    string
			sql     string
			columns []string
			sources []model.ObjectRef
		}{
			{name: "database table column", sql: "SELECT crm.customers.phone FROM crm.customers", columns: []string{"phone"}, sources: []model.ObjectRef{{Schema: "crm", Table: "customers"}}},
			{name: "table column", sql: "SELECT customers.phone FROM customers", columns: []string{"phone"}, sources: []model.ObjectRef{{Table: "customers"}}},
			{name: "alias column", sql: "SELECT c.phone FROM customers c", columns: []string{"phone"}, sources: []model.ObjectRef{{Table: "customers"}}},
			{name: "table case mismatch is unresolved", sql: "SELECT Customers.phone FROM customers", columns: []string{"phone"}, sources: []model.ObjectRef{{}}},
			{name: "unqualified table schema stays empty", sql: "SELECT phone FROM customers", columns: []string{"phone"}, sources: []model.ObjectRef{{Schema: "", Table: "customers"}}},
			{name: "quoted unicode dotted and spaced identifiers", sql: "SELECT `库.一`.`客户 表`.`电话` FROM `库.一`.`客户 表`", columns: []string{"电话"}, sources: []model.ObjectRef{{Schema: "库.一", Table: "客户 表"}}},
			{name: "quoted embedded backtick identifiers", sql: "SELECT `库``一`.`客户``表`.`电``话` FROM `库``一`.`客户``表`", columns: []string{"电`话"}, sources: []model.ObjectRef{{Schema: "库`一", Table: "客户`表"}}},
			{name: "duplicate result names", sql: "SELECT c.phone AS value, o.phone AS value FROM customers c JOIN orders o ON c.id = o.id", columns: []string{"phone", "phone"}, sources: []model.ObjectRef{{Table: "customers"}, {Table: "orders"}}},
			{name: "DISTINCT direct column", sql: "SELECT DISTINCT phone FROM customers", columns: []string{"phone"}, sources: []model.ObjectRef{{Table: "customers"}}},
			{name: "view reference is physical by parser contract", sql: "SELECT phone FROM customer_view", columns: []string{"phone"}, sources: []model.ObjectRef{{Table: "customer_view"}}},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				ast, err := approved.Parse(test.sql)
				require.NoError(t, err)
				requireProjectionColumnsAndSources(t, ast.DirectProjections, test.columns, test.sources)
			})
		}

		unsupported := []struct{ name, sql string }{
			{name: "INTERSECT", sql: "SELECT phone FROM customers INTERSECT SELECT phone FROM orders"},
			{name: "EXCEPT", sql: "SELECT phone FROM customers EXCEPT SELECT phone FROM orders"},
			{name: "TABLE", sql: "TABLE customers"},
			{name: "standalone VALUES", sql: "VALUES ROW(1), ROW(2)"},
			{name: "FULL OUTER JOIN", sql: "SELECT c.phone FROM customers c FULL OUTER JOIN orders o ON c.id = o.id"},
		}
		for _, test := range unsupported {
			t.Run(test.name+" unsupported by Vitess", func(t *testing.T) {
				_, err := approved.Parse(test.sql)
				require.Error(t, err)
			})
		}
	})
}

func requireProjectionColumnsAndSources(
	t *testing.T,
	projections []model.DirectProjectionRef,
	columns []string,
	sources []model.ObjectRef,
) {
	t.Helper()
	require.Len(t, projections, len(columns))
	require.Len(t, sources, len(columns))
	for index := range columns {
		require.Equal(t, columns[index], projections[index].Column)
		require.Equal(t, sources[index], projections[index].Source)
		require.Empty(t, projections[index].Source.Alias)
	}
}
