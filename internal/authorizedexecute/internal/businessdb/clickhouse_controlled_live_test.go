package businessdb

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestClickHouseScopedBudgetRealTLS(t *testing.T) {
	host := os.Getenv("AGENTSQL_TEST_CLICKHOUSE_HOST")
	if host == "" {
		t.Skip("set AGENTSQL_TEST_CLICKHOUSE_HOST for isolated ClickHouse budget testing")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ds := model.Datasource{ID: "clickhouse-budget-live", DBType: "clickhouse", Host: host, Port: 9440,
		Database: "analytics", Username: "agent_reader", TLSMode: "strict", TLSCAFile: os.Getenv("AGENTSQL_TEST_CLICKHOUSE_CA"),
		TLSServerName: host, StmtTimeoutMS: 2000,
		ReadControlJSON: `{"clickhouse":{"max_rows_to_read":100,"max_bytes_to_read":65536,"max_memory_usage":33554432}}`}
	password := os.Getenv("AGENTSQL_TEST_CLICKHOUSE_RO_PASSWORD")
	require.NotEmpty(t, password)
	request := ClickHouseReadRequest{Table: "events", Fields: []string{"name"}, Filter: map[string]string{"region": "CN"}, Limit: 2}
	result, err := ClickHouseReadControlled(ctx, ds, password, request)
	require.NoError(t, err)
	require.Equal(t, 2, result.RowCount)
	request.Table = "budget_events"
	request.Filter = map[string]string{"region": "bulk"}
	_, err = ClickHouseReadControlled(ctx, ds, password, request)
	require.Error(t, err, "a 2000-row scan must fail the 100-row server budget")
	t.Logf("server budget denial: %v", err)
}
