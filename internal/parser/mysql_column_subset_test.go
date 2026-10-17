package parser

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseMySQLColumnSelectShape(t *testing.T) {
	allowed := []struct {
		sql  string
		uses int
	}{
		{"SELECT id, phone AS contact FROM customers", 2},
		{"SELECT c.id, c.phone FROM app.customers AS c WHERE c.id = 1 AND c.phone IS NOT NULL ORDER BY c.id LIMIT 10", 5},
		{"SELECT id FROM customers WHERE id IN (1, 2, 3) OR phone = '123'", 3},
	}
	for _, test := range allowed {
		t.Run(test.sql, func(t *testing.T) {
			shape, err := ParseMySQLColumnSelectShape(test.sql)
			require.NoError(t, err)
			require.Equal(t, "customers", shape.Table)
			require.Len(t, shape.Uses, test.uses)
		})
	}
	denied := []string{
		"SELECT * FROM customers",
		"SELECT id FROM customers; SELECT phone FROM customers",
		"SELECT id FROM customers JOIN secrets USING (id)",
		"SELECT id FROM customers WHERE phone = secret",
		"SELECT id FROM customers WHERE id = ?",
		"SELECT id FROM customers WHERE EXISTS (SELECT 1 FROM secrets)",
		"SELECT id FROM customers WHERE id = 1 OR 1 = 1",
		"SELECT id FROM customers WHERE id IN (SELECT id FROM secrets)",
		"SELECT id FROM customers ORDER BY RAND()",
		"SELECT phone AS id FROM customers ORDER BY id",
		"SELECT id FROM customers LIMIT 10001",
		"SELECT id FROM customers LIMIT 0,10",
		"SELECT id FROM customers FOR UPDATE",
		"SELECT DISTINCT id FROM customers",
		"SELECT id FROM customers GROUP BY id",
		"SELECT id FROM customers INTO OUTFILE '/tmp/x'",
		"WITH c AS (SELECT id FROM customers) SELECT id FROM c",
		"SELECT id FROM customers /* hint */",
		"SELECT id FROM customers WHERE id = @user_var",
		"SELECT id FROM customers WHERE id = 1 UNION SELECT id FROM secrets",
	}
	for _, sql := range denied {
		t.Run(sql, func(t *testing.T) {
			_, err := ParseMySQLColumnSelectShape(sql)
			require.Error(t, err)
		})
	}
}
