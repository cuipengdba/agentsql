package businessdb

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

// TestVastbaseBuild9TypesRealE2E uses only the isolated v3.4 synthetic fixture.
// It is separate from the older G100 integration fixture so existing operators
// can still run the original test without changing their database.
func TestVastbaseBuild9TypesRealE2E(t *testing.T) {
	host := os.Getenv("AGENTSQL_TEST_G100_HOST")
	if host == "" {
		t.Skip("set AGENTSQL_TEST_G100_HOST for the seeded v3.4 G100 instance")
	}
	password := os.Getenv("AGENTSQL_TEST_G100_PASSWORD")
	require.NotEmpty(t, password)
	port := 5432
	if raw := os.Getenv("AGENTSQL_TEST_G100_PORT"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		require.NoError(t, err)
		port = parsed
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	executor, err := NewVastbaseExecutor(ctx, model.Datasource{
		ID: "g100-v340-types", DBType: "vastbase", Host: host, Port: port,
		Database: "vastbase", Username: "agentsql_ro", ConnLimit: 2,
		StmtTimeoutMS: 5_000, RowLimit: 10,
	}, password)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, executor.Close()) })

	columns, err := ListSchema(ctx, executor, "vastbase", []SchemaTable{{Schema: "agentsql_v060", Table: "types_probe"}})
	require.NoError(t, err)
	require.Len(t, columns, 9)
	for _, column := range columns {
		require.NotEmpty(t, column.DataType)
	}
	selectSQL := "SELECT id,count_big,amount,label,event_date,event_time,active,blob,metadata FROM agentsql_v060.types_probe WHERE id=1 LIMIT 1"
	result, err := executor.Query(ctx, selectSQL, 10)
	require.NoError(t, err)
	require.Equal(t, 1, result.RowCount)
	require.Len(t, result.Columns, 9)
	row := result.Rows[0]
	require.Equal(t, "9223372036854770000", row[1])
	require.Equal(t, "12345.67", row[2])
	require.Equal(t, "中文 UTF8", row[3])
	require.Contains(t, row[4], "2026-10-24")
	require.Contains(t, row[5], "09:30:00")
	require.Equal(t, "true", row[6])
	require.Equal(t, "AB", row[7])
	var metadata map[string]any
	require.NoError(t, json.Unmarshal([]byte(row[8]), &metadata))
	require.Equal(t, "sample", metadata["kind"])

	nulls, err := executor.Query(ctx,
		"SELECT id,count_big,amount,label,event_date,event_time,active,blob,metadata FROM agentsql_v060.types_probe WHERE id=2 LIMIT 1", 10)
	require.NoError(t, err)
	require.Equal(t, 1, nulls.RowCount)
	require.Equal(t, "2", nulls.Rows[0][0])
	for _, value := range nulls.Rows[0][1:] {
		require.True(t, strings.TrimSpace(value) == "", "NULL field was not empty")
	}
	plan, err := executor.Explain(ctx, selectSQL)
	require.NoError(t, err)
	require.Positive(t, plan.EstScanRows)
	require.Positive(t, plan.EstCost)
}
