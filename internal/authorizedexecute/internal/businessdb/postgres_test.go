package businessdb

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
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
	_, err := parsePostgresExplainJSON(bytes.Repeat([]byte{'x'}, maxExplainPlanBytes+1))
	require.Error(t, err)
}

func TestParseOpenTenBaseV2ExplainJSON(t *testing.T) {
	// Captured from domainlau/opentenbase:v2.5.0. The missing comma after
	// Node/s is emitted by the server, not removed from this fixture.
	raw := []byte(`[
  {
    "Plan": {
      "Node Type": "Remote Fast Query Execution",
      "Parallel Aware": false,
      "Startup Cost": 0.00,
      "Total Cost": 0.00,
      "Plan Rows": 0,
      "Plan Width": 0,
      "Node/s": "dn001"
      "Remote plan": [
        {
          "Plan": {
            "Node Type": "Limit",
            "Parallel Aware": false,
            "Startup Cost": 0.15,
            "Total Cost": 3.99,
            "Plan Rows": 10,
            "Plan Width": 174,
            "Plans": [
              {
                "Node Type": "Index Scan",
                "Parent Relationship": "Outer",
                "Parallel Aware": false,
                "Scan Direction": "Forward",
                "Index Name": "agentsql_v05_customers_pkey",
                "Relation Name": "agentsql_v05_customers",
                "Alias": "agentsql_v05_customers",
                "Startup Cost": 0.15,
                "Total Cost": 46.25,
                "Plan Rows": 120,
                "Plan Width": 174,
                "Index Cond": "(id > 0)"
              }
            ]
          }
        }
      ]
    }
  }
]`)

	info, err := parseOpenTenBaseExplainJSON(raw, openTenBaseV2Version)
	require.NoError(t, err)
	require.Equal(t, int64(120), info.EstScanRows)
	require.Equal(t, 46.25, info.EstCost)
	require.False(t, info.SeqScan)
	require.True(t, info.UsesIndex)
	require.Equal(t, "opentenbase-v2 nodes=2 scan_rows=120 cost=46.25 seq_scan=false uses_index=true", info.Raw)
	require.NotContains(t, info.Raw, "id > 0")
}

func TestParseOpenTenBaseV2StandardJSON(t *testing.T) {
	// CN-local statements do not use Remote Fast Query Execution. OpenTenBase
	// emits ordinary PostgreSQL JSON for these plans.
	raw := []byte(`[{"Plan":{"Node Type":"Limit","Plan Rows":1,"Total Cost":0.01,"Plans":[{"Node Type":"Result","Plan Rows":1,"Total Cost":0.01}]}}]`)

	info, err := parseOpenTenBaseExplainJSON(raw, openTenBaseV2Version)
	require.NoError(t, err)
	require.Equal(t, int64(1), info.EstScanRows)
	require.Equal(t, 0.01, info.EstCost)
	require.False(t, info.SeqScan)
	require.False(t, info.UsesIndex)
	require.Equal(t, "opentenbase-v2 nodes=2 scan_rows=1 cost=0.01 seq_scan=false uses_index=false", info.Raw)
}

func TestParseOpenTenBaseV2ExplainJSONFailsClosed(t *testing.T) {
	base := `[{"Plan":{"Node Type":"Remote Fast Query Execution","Plan Rows":0,"Total Cost":0,"Node/s":"dn001" "Remote plan":[{"Plan":{"Node Type":%q,"Plan Rows":1,"Total Cost":1}}]}}]`

	_, err := parseOpenTenBaseExplainJSON(
		[]byte(fmt.Sprintf(base, "Unknown Vendor Node")),
		openTenBaseV2Version,
	)
	require.Error(t, err)

	_, err = parseOpenTenBaseExplainJSON(
		[]byte(fmt.Sprintf(base, "Seq Scan")),
		"10.0 OpenTenBase V3",
	)
	require.Error(t, err)

	_, err = parseOpenTenBaseExplainJSON(
		bytes.Repeat([]byte{'x'}, maxExplainPlanBytes+1),
		openTenBaseV2Version,
	)
	require.Error(t, err)

	_, err = parseOpenTenBaseExplainJSON(
		[]byte(`[{"Plan":{"Node Type":"Seq Scan","Plan Rows":1}}]`),
		openTenBaseV2Version,
	)
	require.Error(t, err)

	_, err = parseOpenTenBaseExplainJSON(
		[]byte(`[{"Plan":{"Node Type":"Remote Fast Query Execution","Plan Rows":0,"Total Cost":0,"Remote plan":[{"Plan":{"Node Type":"Seq Scan","Plan Rows":1,"Total Cost":1}}]}}]`),
		openTenBaseV2Version,
	)
	require.Error(t, err)

	_, err = parseOpenTenBaseExplainJSON(
		[]byte(`[{"Plan":{"Node Type":"Result","Plan Rows":1,"Total Cost":1,"Remote plan":[{"Plan":{"Node Type":"Result","Plan Rows":1,"Total Cost":1}}]}}]`),
		openTenBaseV2Version,
	)
	require.Error(t, err)

	_, err = parseOpenTenBaseExplainJSON(
		[]byte(`[{"Plan":{"Node Type":"Remote Fast Query Execution","Plan Rows":0,"Total Cost":0,"Node/s":"dn001" "Remote plan":[],"Extra":{"Node/s":"dn002" "Remote plan":[]}}}]`),
		openTenBaseV2Version,
	)
	require.Error(t, err)
}

func TestParseOpenTenBaseV2ExplainJSONRejectsTooManyNodes(t *testing.T) {
	plan := `{"Node Type":"Result","Plan Rows":1,"Total Cost":1}`
	for range maxExplainPlanNodes {
		plan = `{"Node Type":"Result","Plan Rows":1,"Total Cost":1,"Plans":[` + plan + `]}`
	}
	raw := []byte(`[{"Plan":` + plan + `}]`)

	_, err := parseOpenTenBaseExplainJSON(raw, openTenBaseV2Version)
	require.Error(t, err)
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
