package controlledread

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/discovery"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestPolarDBXDiscoveryE2E(t *testing.T) {
	if testing.Short() || os.Getenv("AGENTSQL_POLARDBX_E2E") != "1" {
		t.Skip("set AGENTSQL_POLARDBX_E2E=1 and POLARDBX_E2E_PASSWORD to run")
	}
	password := os.Getenv("POLARDBX_E2E_PASSWORD")
	if password == "" {
		t.Fatal("POLARDBX_E2E_PASSWORD is required")
	}
	port, err := strconv.Atoi(polarDBXEnvOrDefault("POLARDBX_E2E_PORT", "8527"))
	require.NoError(t, err)
	database := polarDBXEnvOrDefault("POLARDBX_E2E_DATABASE", "agentsql_polardbx")
	datasource := model.Datasource{
		ID: "polardbx-discovery-e2e", DBType: "mysql",
		Host: polarDBXEnvOrDefault("POLARDBX_E2E_HOST", "127.0.0.1"), Port: port,
		Database: database, Username: polarDBXEnvOrDefault("POLARDBX_E2E_USERNAME", "polardbx_root"),
		ConnLimit: 2, StmtTimeoutMS: 10_000, RowLimit: 100,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	writer := newControlledReadE2EDatabase(t, datasource, password, false)

	const table = "agentsql_polardbx_discovery_e2e"
	_, _ = writer.Execute(ctx, "DROP TABLE IF EXISTS "+table)
	t.Cleanup(func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		_, _ = writer.Execute(cleanup, "DROP TABLE IF EXISTS "+table)
	})
	_, err = writer.Execute(ctx, "CREATE TABLE "+table+" (id BIGINT PRIMARY KEY, phone VARCHAR(32), email VARCHAR(128), note VARCHAR(128))")
	require.NoError(t, err)
	_, err = writer.Execute(ctx, "INSERT INTO "+table+" VALUES (1,'13800138000','polar@example.com','POLARDBX_RAW_SENTINEL')")
	require.NoError(t, err)

	service := newDatabaseDiscoveryService(t, datasource, password)
	result, err := service.Discover(ctx, "polardbx-admin", datasource.ID, discovery.ScanRequest{
		Tables: []discovery.TableRef{{Schema: database, Table: table}}, SampleRows: 1,
		Categories: []discovery.Category{discovery.CategoryPhone, discovery.CategoryEmail},
	})
	require.NoError(t, err)
	require.Equal(t, 4, result.Stats.ColumnsSeen)
	require.Equal(t, 2, result.Stats.CandidateColumns)
	requireDiscoveryCategories(t, result, map[discovery.Category]int{
		discovery.CategoryPhone: 1, discovery.CategoryEmail: 1,
	})
	require.NotContains(t, fmt.Sprintf("%v", result), "POLARDBX_RAW_SENTINEL")

	query := "SELECT `phone` FROM `" + database + "`.`" + table + "` WHERE id=1 LIMIT 1"
	statement, err := writer.gateway.AuthorizedExecute(ctx, writer.datasource, writer.secret, query, "")
	require.NoError(t, err)
	_, err = statement.Explain(ctx)
	require.NoError(t, err)
	require.NoError(t, statement.Close())
}

func polarDBXEnvOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
