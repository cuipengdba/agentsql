package pipeline

import (
	"context"
	"fmt"
	"testing"

	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func runMaskScopeScenarios(
	t *testing.T,
	ctx context.Context,
	dialect string,
	datasource model.Datasource,
	databaseExecutor *pipelineE2EDatabase,
	counted *countingDatabaseExecutor,
	allowedTables []string,
) {
	t.Helper()
	customers, orders := pipelineE2ETableNames(dialect, datasource.Database)
	baselineSQL := fmt.Sprintf(
		"SELECT c.phone AS phone, c.name AS customer_name, o.note AS order_note "+
			"FROM %s c JOIN %s o ON o.customer_id=c.id ORDER BY o.id",
		customers,
		orders,
	)
	baseline, err := databaseExecutor.Query(ctx, baselineSQL, 10)
	require.NoError(t, err)
	require.Equal(t, 2, baseline.RowCount)
	require.Equal(t, [][]string{
		{"13812345678", "Alice", "first note"},
		{"13987654321", "Bob", "second note"},
	}, baseline.Rows)

	var first *model.QueryResult
	for _, aliases := range []struct {
		name     string
		customer string
		order    string
	}{
		{name: "short table aliases", customer: "c", order: "o"},
		{name: "long table aliases", customer: "cust", order: "ord"},
	} {
		aliases := aliases
		t.Run("mask join "+aliases.name, func(t *testing.T) {
			query := fmt.Sprintf(
				"SELECT %[1]s.phone AS phone, %[1]s.name AS customer_name, %[2]s.note AS order_note "+
					"FROM %[3]s %[1]s JOIN %[4]s %[2]s ON %[2]s.customer_id=%[1]s.id ORDER BY %[2]s.id",
				aliases.customer,
				aliases.order,
				customers,
				orders,
			)
			flow, _ := newDatabaseE2EPipeline(t, datasource, counted, "dml", allowedTables)
			response, err := flow.Process(ctx, databaseE2ERequest(datasource.ID, query))
			require.NoError(t, err)
			require.Equal(t, model.DecisionAllow, response.Decision)
			require.NotNil(t, response.Result)
			require.Equal(t, baseline.RowCount, response.Result.RowCount)
			require.Len(t, response.Result.Rows, len(baseline.Rows))
			require.Equal(t, []string{"phone", "customer_name", "order_note"}, response.Result.Columns)
			require.Equal(t, "138****5678", response.Result.Rows[0][0])
			require.Equal(t, "139****4321", response.Result.Rows[1][0])
			for rowIndex := range baseline.Rows {
				require.Equal(t, baseline.Rows[rowIndex][1], response.Result.Rows[rowIndex][1])
				require.Equal(t, baseline.Rows[rowIndex][2], response.Result.Rows[rowIndex][2])
			}
			require.Equal(t, 2, response.Redact.MaskedCells)
			require.Equal(t, map[int]mask.SensitiveType{0: mask.TypePhone}, response.Redact.TouchedColumns)
			// LatencyMS is non-deterministic timing data; alias-length equivalence
			// is about columns/rows/redaction, so normalize it before comparing.
			if first == nil {
				copyOfResult := *response.Result
				copyOfResult.LatencyMS = 0
				first = &copyOfResult
			} else {
				second := *response.Result
				second.LatencyMS = 0
				require.Equal(t, *first, second)
			}
		})
	}
}

func runDirectSourceFallbackScenario(
	t *testing.T,
	ctx context.Context,
	dialect string,
	datasource model.Datasource,
	counted *countingDatabaseExecutor,
	allowedTables []string,
) {
	t.Helper()
	customers, _ := pipelineE2ETableNames(dialect, datasource.Database)
	t.Run("a0 direct aliased source fallback", func(t *testing.T) {
		flow, _ := newDatabaseE2EPipeline(t, datasource, counted, "dml", allowedTables)
		query := fmt.Sprintf("SELECT c.phone AS mobile FROM %s c ORDER BY c.id", customers)
		response, err := flow.Process(ctx, databaseE2ERequest(datasource.ID, query))
		require.NoError(t, err)
		require.Equal(t, model.DecisionAllow, response.Decision)
		require.Equal(t, []string{"mobile"}, response.Result.Columns)
		require.Equal(t, [][]string{{"138****5678"}, {"139****4321"}}, response.Result.Rows)
		require.Equal(t, 2, response.Redact.MaskedCells)
		require.Equal(t, map[int]mask.SensitiveType{0: mask.TypePhone}, response.Redact.TouchedColumns)
	})
}

func runExpressionLineageNowMaskedScenario(
	t *testing.T,
	ctx context.Context,
	dialect string,
	datasource model.Datasource,
	counted *countingDatabaseExecutor,
	allowedTables []string,
) {
	t.Helper()
	customers, _ := pipelineE2ETableNames(dialect, datasource.Database)
	t.Run("expression lineage now masked", func(t *testing.T) {
		var query string
		if dialect == "postgres" {
			query = fmt.Sprintf("SELECT phone || '' AS p FROM %s ORDER BY id", customers)
		} else {
			query = fmt.Sprintf("SELECT CONCAT(phone, '') AS p FROM %s ORDER BY id", customers)
		}
		flow, _ := newDatabaseE2EPipeline(t, datasource, counted, "dml", allowedTables)
		response, err := flow.Process(ctx, databaseE2ERequest(datasource.ID, query))
		require.NoError(t, err)
		require.Equal(t, model.DecisionAllow, response.Decision)
		require.Equal(t, []string{"p"}, response.Result.Columns)
		require.Equal(t, [][]string{{"138****5678"}, {"139****4321"}}, response.Result.Rows)
		require.Equal(t, 2, response.Redact.MaskedCells)
		require.Equal(t, map[int]mask.SensitiveType{0: mask.TypePhone}, response.Redact.TouchedColumns)
		require.Empty(t, response.Redact.UnresolvedScopedColumns)
	})
}

func pipelineE2ETableNames(dialect, database string) (customers, orders string) {
	if dialect == "postgres" {
		return "public.customers", "public.orders"
	}
	return database + ".customers", database + ".orders"
}
