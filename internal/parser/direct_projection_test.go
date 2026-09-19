package parser

import (
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestDirectProjectionRefs(t *testing.T) {
	tests := []struct {
		name     string
		sql      string
		expected []model.DirectProjectionRef
	}{
		{
			name: "bare column",
			sql:  "SELECT phone FROM customers",
			expected: []model.DirectProjectionRef{
				{Column: "phone", Offset: 0, Source: model.ObjectRef{Table: "customers"}},
			},
		},
		{
			name: "qualified column with alias",
			sql:  "SELECT c.phone AS mobile FROM customers AS c",
			expected: []model.DirectProjectionRef{
				{Column: "phone", Offset: 0, Source: model.ObjectRef{Table: "customers"}},
			},
		},
		{
			name: "three part qualified column",
			sql:  "SELECT sales.customers.phone AS mobile FROM sales.customers",
			expected: []model.DirectProjectionRef{
				{Column: "phone", Offset: 0, Source: model.ObjectRef{Schema: "sales", Table: "customers"}},
			},
		},
		{
			name: "direct column retains left offset among expressions",
			sql:  "SELECT 1, phone, upper(email) FROM customers",
			expected: []model.DirectProjectionRef{
				{Column: "phone", Offset: 1, Source: model.ObjectRef{Table: "customers"}},
			},
		},
		{
			name:     "literal is not direct",
			sql:      "SELECT 1 FROM customers",
			expected: nil,
		},
		{
			name:     "function is not direct",
			sql:      "SELECT lower(phone) FROM customers",
			expected: nil,
		},
		{
			name:     "operator is not direct",
			sql:      "SELECT phone || '' FROM customers",
			expected: nil,
		},
		{
			name:     "aggregate is not direct",
			sql:      "SELECT max(phone) FROM customers",
			expected: nil,
		},
		{
			name:     "cast is not direct",
			sql:      "SELECT CAST(phone AS CHAR) FROM customers",
			expected: nil,
		},
		{
			name:     "star is not direct",
			sql:      "SELECT * FROM customers",
			expected: nil,
		},
		{
			name:     "qualified star is not direct",
			sql:      "SELECT c.* FROM customers AS c",
			expected: nil,
		},
		{
			name: "column after one star is located from end",
			sql:  "SELECT *, phone AS mobile FROM customers",
			expected: []model.DirectProjectionRef{
				{Column: "phone", Offset: 0, FromEnd: true, Source: model.ObjectRef{Table: "customers"}},
			},
		},
		{
			name: "columns around one star retain safe positions",
			sql:  "SELECT id, *, phone AS mobile, email FROM customers",
			expected: []model.DirectProjectionRef{
				{Column: "id", Offset: 0, Source: model.ObjectRef{Table: "customers"}},
				{Column: "phone", Offset: 1, FromEnd: true, Source: model.ObjectRef{Table: "customers"}},
				{Column: "email", Offset: 0, FromEnd: true, Source: model.ObjectRef{Table: "customers"}},
			},
		},
		{
			name: "multiple stars leave middle direct slot unmapped",
			sql:  "SELECT id, *, phone AS mobile, c.*, email FROM customers AS c",
			expected: []model.DirectProjectionRef{
				{Column: "id", Offset: 0, Source: model.ObjectRef{Table: "customers"}},
				{Column: "email", Offset: 0, FromEnd: true, Source: model.ObjectRef{Table: "customers"}},
			},
		},
		{
			name:     "union is not mapped",
			sql:      "SELECT phone FROM customers UNION ALL SELECT phone FROM archived_customers",
			expected: nil,
		},
		{
			name:     "explain is not mapped",
			sql:      "EXPLAIN SELECT phone FROM customers",
			expected: nil,
		},
	}

	for _, dialect := range []model.DBDialect{"postgres", "mysql"} {
		t.Run(string(dialect), func(t *testing.T) {
			approved, err := NewParser(dialect)
			require.NoError(t, err)
			for _, test := range tests {
				t.Run(test.name, func(t *testing.T) {
					ast, err := approved.Parse(test.sql)
					require.NoError(t, err)
					require.Equal(t, test.expected, ast.DirectProjections)
				})
			}
		})
	}
}
