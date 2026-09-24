package businessdb

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/stretchr/testify/require"
)

func TestPostgresFrameCapRejectsHeaderBeforeBodyRead(t *testing.T) {
	header := []byte{'D', 0, 0, 0, 0}
	bodyLength := maxDatabaseFrameBytes - postgresFrameHeaderSize + 1
	binary.BigEndian.PutUint32(header[1:], uint32(bodyLength+4))
	frontend := newBoundedPostgresFrontend(bytes.NewReader(header), io.Discard)
	_, err := frontend.Receive()
	var exceeded *pgproto3.ExceededMaxBodyLenErr
	require.ErrorAs(t, err, &exceeded)
}

func TestParsePostgresExplainJSON(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		rows      int64
		cost      float64
		seqScan   bool
		usesIndex bool
	}{
		{
			name:      "sequential scan",
			raw:       `[{"Plan":{"Node Type":"Seq Scan","Plan Rows":120000,"Total Cost":4312.5}}]`,
			rows:      120000,
			cost:      4312.5,
			seqScan:   true,
			usesIndex: false,
		},
		{
			name:      "index scan",
			raw:       `[{"Plan":{"Node Type":"Index Scan","Plan Rows":1,"Total Cost":8.17,"Index Name":"orders_pkey"}}]`,
			rows:      1,
			cost:      8.17,
			usesIndex: true,
		},
		{
			name:      "bitmap heap scan",
			raw:       `[{"Plan":{"Node Type":"Bitmap Heap Scan","Plan Rows":42,"Total Cost":18.3}}]`,
			rows:      42,
			cost:      18.3,
			usesIndex: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			info, err := parsePostgresExplainJSON([]byte(test.raw))
			require.NoError(t, err)
			require.Equal(t, test.rows, info.EstScanRows)
			require.Equal(t, test.cost, info.EstCost)
			require.Equal(t, test.seqScan, info.SeqScan)
			require.Equal(t, test.usesIndex, info.UsesIndex)
			require.Equal(t, test.raw, info.Raw)
		})
	}
}

func TestParsePostgresExplainJSONRejectsMalformedInput(t *testing.T) {
	for _, raw := range []string{
		`not-json`,
		`[]`,
		`[{"Plan":{}}]`,
		`[{"Plan":{"Node Type":"Seq Scan","Plan Rows":-1,"Total Cost":1}}]`,
	} {
		_, err := parsePostgresExplainJSON([]byte(raw))
		require.Error(t, err)
	}
}

func TestPostgresDatabaseErrorClassification(t *testing.T) {
	tests := []struct {
		name     string
		cause    error
		sentinel error
	}{
		{name: "context deadline", cause: context.DeadlineExceeded, sentinel: ErrQueryTimeout},
		{name: "server timeout", cause: &pgconn.PgError{Code: "57014"}, sentinel: ErrQueryTimeout},
		{name: "server read only", cause: &pgconn.PgError{Code: "25006"}, sentinel: ErrReadOnlyViolated},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := postgresDatabaseError(context.Background(), DBStageQuery, "query", test.cause)
			require.True(t, errors.Is(err, test.sentinel))
		})
	}
}
