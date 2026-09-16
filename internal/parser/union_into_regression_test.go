package parser

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Regression for the G1-c review question "does INTO OUTFILE/DUMPFILE attached
// to a UNION slip past the R201 signal?". Vitess exposes it on Union.Into, so the
// dangerous operation must always be signalled (R201 fires/deny) and the output
// file name must never survive normalization.
//
// Known v0.1 normalization limitation (not fail-open, deferred to v0.2): for the
// UNION shapes the INTO clause is dropped from Normalized rather than rendered as
// "into outfile ?". The decision still comes from Operations, and the file name
// is not leaked, so interception and sensitive-string redaction are correct.
func TestMySQLUnionIntoOutfileIsSignaledAndFileNameStripped(t *testing.T) {
	p, err := NewParser(mysqlDialect)
	require.NoError(t, err)

	cases := []struct {
		name     string
		sql      string
		op       string
		fileName string
	}{
		{
			name:     "union select into outfile",
			sql:      "SELECT id FROM users UNION SELECT id FROM admins INTO OUTFILE '/tmp/leak.csv'",
			op:       "INTO OUTFILE",
			fileName: "/tmp/leak.csv",
		},
		{
			name:     "parenthesized union into outfile",
			sql:      "(SELECT id FROM users UNION SELECT id FROM admins) INTO OUTFILE '/tmp/b.csv'",
			op:       "INTO OUTFILE",
			fileName: "/tmp/b.csv",
		},
		{
			name:     "union select into dumpfile",
			sql:      "SELECT id FROM users UNION SELECT id FROM admins INTO DUMPFILE '/tmp/leak.so'",
			op:       "INTO DUMPFILE",
			fileName: "/tmp/leak.so",
		},
		{
			name:     "plain select into outfile still redacted",
			sql:      "SELECT id FROM users INTO OUTFILE '/tmp/a.csv'",
			op:       "INTO OUTFILE",
			fileName: "/tmp/a.csv",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			ast, parseErr := p.Parse(tc.sql)
			require.NoError(t, parseErr)
			require.Contains(t, ast.Operations, tc.op,
				"dangerous INTO operation must be signalled so R201 denies it")
			require.NotContains(t, ast.Normalized, tc.fileName,
				"output file name must not survive normalization")
			require.NotContains(t, ast.Normalized, "'/tmp",
				"no quoted output path may survive normalization")
		})
	}
}
