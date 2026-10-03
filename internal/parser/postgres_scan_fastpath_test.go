package parser

import (
	"testing"

	pg_query "github.com/pganalyze/pg_query_go/v5"
	"github.com/stretchr/testify/require"
)

func TestPostgresTokenScanFastPath(t *testing.T) {
	fastPath := []string{
		"SELECT c.phone AS phone_0 FROM customers c",
		"SELECT TRUE, FALSE, NULL FROM customers_8",
		"SELECT customer_1.phone_2 FROM customer_1",
		"SELECT \"phone_8\" FROM \"customers_8\"",
	}
	for _, sql := range fastPath {
		t.Run(sql, func(t *testing.T) {
			require.False(t, postgresNeedsTokenScan(sql))
			scanResult, err := pg_query.Scan(sql)
			require.NoError(t, err)
			require.False(t, postgresScanContainsLiteral(scanResult))
			require.False(t, postgresScanHasComment(scanResult))
			normalized, err := normalizePostgresScanResult(sql, scanResult)
			require.NoError(t, err)
			require.Equal(t, sql, normalized)
		})
	}

	fallback := []string{
		"SELECT 1",
		"SELECT .5",
		"SELECT 'secret'",
		"SELECT E'secret'",
		"SELECT B'1010'",
		"SELECT $$secret$$",
		"SELECT $1",
		"SELECT value -- comment\nFROM customers",
		"SELECT value /* comment */ FROM customers",
	}
	for _, sql := range fallback {
		t.Run(sql, func(t *testing.T) {
			require.True(t, postgresNeedsTokenScan(sql))
		})
	}
}
