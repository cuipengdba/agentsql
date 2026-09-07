package parser

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRedactPostgresStringConstants(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "copy program command",
			input:    "COPY public.orders TO PROGRAM 'cat > /tmp/orders'",
			expected: "COPY public.orders TO PROGRAM ?",
		},
		{
			name:     "copy file path",
			input:    "COPY public.orders FROM '/var/lib/postgresql/import/orders.csv'",
			expected: "COPY public.orders FROM ?",
		},
		{
			name:     "notify payload",
			input:    "NOTIFY audit_events, 'sensitive payload'",
			expected: "NOTIFY audit_events, ?",
		},
		{
			name:     "comment text",
			input:    "COMMENT ON TABLE public.orders IS 'internal description'",
			expected: "COMMENT ON TABLE public.orders IS ?",
		},
		{
			name:     "role password",
			input:    "ALTER ROLE report_user PASSWORD 'secret password'",
			expected: "ALTER ROLE report_user PASSWORD ?",
		},
		{
			name:     "native DML placeholders unchanged",
			input:    "SELECT id FROM users WHERE name = $1 AND tenant_id = $2",
			expected: "SELECT id FROM users WHERE name = $1 AND tenant_id = $2",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual, err := redactPostgresStringConstants(test.input)
			require.NoError(t, err)
			require.Equal(t, test.expected, actual)
		})
	}
}
