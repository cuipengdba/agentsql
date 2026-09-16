package store

import (
	"database/sql"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDialectControlledSQLFragments(t *testing.T) {
	t.Parallel()
	require.Equal(t, " INDEXED BY idx_agents_keyhash", indexHint(DialectSQLite, " INDEXED BY idx_agents_keyhash"))
	require.Empty(t, indexHint(DialectPostgres, " INDEXED BY idx_agents_keyhash"))
	require.Equal(t, "LIKE", likeOperator(DialectSQLite))
	require.Equal(t, "ILIKE", likeOperator(DialectPostgres))
	require.Equal(t, "date(substr(ts,1,19))", auditDaySelect(DialectSQLite))
	require.Equal(t, "date(substr(ts,1,19))", auditDayGroupBy(DialectSQLite))
	require.Equal(t, "to_char((ts AT TIME ZONE 'UTC')::date,'YYYY-MM-DD')", auditDaySelect(DialectPostgres))
	require.Equal(t, "(ts AT TIME ZONE 'UTC')::date", auditDayGroupBy(DialectPostgres))
}

func TestDatabaseScannersNormalizeDriverValues(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		source   any
		expected bool
	}{
		{true, true}, {false, false}, {int64(1), true}, {int64(0), false},
		{"true", true}, {"FALSE", false}, {[]byte("1"), true}, {[]byte("0"), false},
	} {
		var value databaseBool
		require.NoError(t, value.Scan(test.source))
		require.Equal(t, test.expected, value.value)
	}
	for _, source := range []any{nil, int64(2), "yes", []byte("-1")} {
		var value databaseBool
		require.Error(t, value.Scan(source))
	}

	location := time.FixedZone("UTC+8", 8*60*60)
	original := time.Date(2026, time.September, 17, 12, 0, 0, 0, location)
	var timestamp databaseTimestamp
	require.NoError(t, timestamp.Scan(original))
	require.Equal(t, time.UTC, timestamp.time.Location())
	require.True(t, timestamp.time.Equal(original))

	if strconv.IntSize == 32 {
		_, err := intPointer(sql.NullInt64{Int64: math.MaxInt32 + 1, Valid: true})
		require.Error(t, err)
	}
}
