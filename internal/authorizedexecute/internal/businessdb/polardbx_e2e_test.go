package businessdb

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

// TestPolarDBXMySQLCompatibilityE2E exercises the bounded MySQL protocol path
// against a caller-provided PolarDB-X CN. It is deliberately opt-in because a
// representative PolarDB-X topology is too heavy for the default unit suite.
func TestPolarDBXMySQLCompatibilityE2E(t *testing.T) {
	if testing.Short() || os.Getenv("AGENTSQL_POLARDBX_E2E") != "1" {
		t.Skip("set AGENTSQL_POLARDBX_E2E=1 and POLARDBX_E2E_PASSWORD to run")
	}
	password := os.Getenv("POLARDBX_E2E_PASSWORD")
	if password == "" {
		t.Fatal("POLARDBX_E2E_PASSWORD is required")
	}
	port, err := strconv.Atoi(polarDBXEnvOrDefault("POLARDBX_E2E_PORT", "8527"))
	require.NoError(t, err)
	datasource := model.Datasource{
		ID: "polardbx-e2e", DBType: "mysql",
		Host: polarDBXEnvOrDefault("POLARDBX_E2E_HOST", "127.0.0.1"), Port: port,
		Database:  polarDBXEnvOrDefault("POLARDBX_E2E_DATABASE", "agentsql_polardbx"),
		Username:  polarDBXEnvOrDefault("POLARDBX_E2E_USERNAME", "polardbx_root"),
		ConnLimit: 3, StmtTimeoutMS: 10_000, RowLimit: 100,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	executor, err := NewMySQLExecutor(ctx, datasource, password, false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, executor.Close()) })

	version, err := executor.Query(ctx, "SELECT VERSION()", 1)
	require.NoError(t, err)
	require.Len(t, version.Rows, 1)
	serverVersion := strings.ToLower(version.Rows[0][0])
	require.True(t, strings.Contains(serverVersion, "tddl") || strings.Contains(serverVersion, "polardb-x") || strings.Contains(serverVersion, "pxc"),
		"E2E endpoint is not identifiable as PolarDB-X: %q", version.Rows[0][0])

	const table = "agentsql_polardbx_executor_e2e"
	_, _ = executor.Execute(ctx, "DROP TABLE IF EXISTS "+table)
	t.Cleanup(func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		_, _ = executor.Execute(cleanup, "DROP TABLE IF EXISTS "+table)
	})
	_, err = executor.Execute(ctx, "CREATE TABLE "+table+" (id BIGINT PRIMARY KEY, value BIGINT NOT NULL, KEY value_idx(value))")
	require.NoError(t, err)

	_, err = executor.Execute(ctx, "INSERT INTO "+table+" (id,value) VALUES (1,10),(2,20),(3,30)")
	require.NoError(t, err)
	_, err = executor.Execute(ctx, "UPDATE "+table+" SET value=21 WHERE id=2")
	require.NoError(t, err)
	_, err = executor.Execute(ctx, "DELETE FROM "+table+" WHERE id=3")
	require.NoError(t, err)

	result, err := executor.Query(ctx, "SELECT id,value FROM "+table+" WHERE id >= 1 ORDER BY id LIMIT 10", 10)
	require.NoError(t, err)
	require.Equal(t, [][]string{{"1", "10"}, {"2", "21"}}, result.Rows)

	plan, err := executor.Explain(ctx, "SELECT id,value FROM "+table+" WHERE value=21 LIMIT 10")
	require.NoError(t, err)
	require.True(t, plan.UsesIndex)
	require.GreaterOrEqual(t, plan.EstScanRows, int64(0))

	tx, err := executor.BeginWriteTx(ctx)
	require.NoError(t, err)
	_, err = tx.Execute(ctx, "INSERT INTO "+table+" (id,value) VALUES (4,40)")
	require.NoError(t, err)
	require.NoError(t, tx.Rollback(ctx))
	rolledBack, err := executor.Query(ctx, "SELECT COUNT(*) FROM "+table+" WHERE id=4", 1)
	require.NoError(t, err)
	require.Equal(t, "0", rolledBack.Rows[0][0])

	readOnly, err := NewMySQLExecutor(ctx, datasource, password, true)
	require.NoError(t, err)
	defer func() { require.NoError(t, readOnly.Close()) }()
	_, err = readOnly.Execute(ctx, "UPDATE "+table+" SET value=0 WHERE id=1")
	require.True(t, errors.Is(err, ErrReadOnlyViolated))
}

func polarDBXEnvOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
