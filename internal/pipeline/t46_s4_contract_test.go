package pipeline

import (
	"context"
	"testing"

	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestT46S4PipelineScopedRuleContracts(t *testing.T) {
	t.Run("exact table match does not bleed across tables", func(t *testing.T) {
		fixture := newT46S4Fixture(t, []mask.Rule{
			{Table: "customers", Column: "phone", SensitiveType: mask.TypePhone, Algorithm: mask.AlgoMask},
			{Table: "public_phones", Column: "phone", SensitiveType: mask.TypeGeneric, Algorithm: mask.AlgoBlock},
		})
		response := runT46S4Query(t, fixture, "SELECT phone FROM customers", model.QueryResult{
			Columns: []string{"phone"}, Rows: [][]string{{"13812345678"}}, RowCount: 1,
		})
		require.Equal(t, "138****5678", response.Result.Rows[0][0])
		require.Empty(t, response.Redact.UnresolvedScopedColumns)
	})

	t.Run("exact schema wins over wildcard", func(t *testing.T) {
		fixture := newT46S4Fixture(t, []mask.Rule{
			{Schema: "public", Table: "customers", Column: "phone", SensitiveType: mask.TypeGeneric, Algorithm: mask.AlgoBlock},
			{Table: "customers", Column: "phone", SensitiveType: mask.TypePhone, Algorithm: mask.AlgoMask},
		})
		response := runT46S4Query(t, fixture, "SELECT phone FROM public.customers", model.QueryResult{
			Columns: []string{"phone"}, Rows: [][]string{{"13812345678"}}, RowCount: 1,
		})
		require.Equal(t, mask.BlockPlaceholder, response.Result.Rows[0][0])
		require.Equal(t, mask.TypeGeneric, response.Redact.TouchedColumns[0])
	})

	t.Run("source without schema does not match exact schema", func(t *testing.T) {
		fixture := newT46S4Fixture(t, []mask.Rule{
			{Schema: "public", Table: "customers", Column: "phone", SensitiveType: mask.TypeGeneric, Algorithm: mask.AlgoBlock},
		})
		response := runT46S4Query(t, fixture, "SELECT phone FROM customers", model.QueryResult{
			Columns: []string{"phone"}, Rows: [][]string{{"13812345678"}}, RowCount: 1,
		})
		require.Equal(t, "13812345678", response.Result.Rows[0][0])
		require.Zero(t, response.Redact.MaskedCells)
	})

	t.Run("unresolved join column fails closed", func(t *testing.T) {
		fixture := newT46S4Fixture(t, []mask.Rule{
			{Table: "customers", Column: "phone", SensitiveType: mask.TypePhone, Algorithm: mask.AlgoMask},
		})
		response := runT46S4Query(t, fixture, "SELECT phone FROM customers JOIN orders ON customers.id = orders.id", model.QueryResult{
			Columns: []string{"phone"}, Rows: [][]string{{"13812345678"}}, RowCount: 1,
		})
		require.Equal(t, mask.BlockPlaceholder, response.Result.Rows[0][0])
		require.Equal(t, map[int]mask.SensitiveType{0: mask.TypePhone}, response.Redact.UnresolvedScopedColumns)
	})

	t.Run("single relation star resolves runtime column exactly", func(t *testing.T) {
		fixture := newT46S4Fixture(t, []mask.Rule{
			{Table: "customers", Column: "phone", SensitiveType: mask.TypePhone, Algorithm: mask.AlgoMask},
		})
		response := runT46S4Query(t, fixture, "SELECT * FROM customers", model.QueryResult{
			Columns: []string{"phone"}, Rows: [][]string{{"13812345678"}}, RowCount: 1,
		})
		require.Equal(t, "138****5678", response.Result.Rows[0][0])
		require.Empty(t, response.Redact.UnresolvedScopedColumns)
	})

	t.Run("cte direct projection is exact", func(t *testing.T) {
		fixture := newT46S4Fixture(t, []mask.Rule{
			{Table: "customers", Column: "phone", SensitiveType: mask.TypePhone, Algorithm: mask.AlgoMask},
		})
		response := runT46S4Query(t, fixture, "WITH x AS (SELECT phone FROM customers) SELECT phone FROM x", model.QueryResult{
			Columns: []string{"phone"}, Rows: [][]string{{"13812345678"}}, RowCount: 1,
		})
		require.Equal(t, "138****5678", response.Result.Rows[0][0])
		require.Empty(t, response.Redact.UnresolvedScopedColumns)
	})

	t.Run("derived direct projection is exact", func(t *testing.T) {
		fixture := newT46S4Fixture(t, []mask.Rule{
			{Table: "customers", Column: "phone", SensitiveType: mask.TypePhone, Algorithm: mask.AlgoMask},
		})
		response := runT46S4Query(t, fixture, "SELECT phone FROM (SELECT phone FROM customers) x", model.QueryResult{
			Columns: []string{"phone"}, Rows: [][]string{{"13812345678"}}, RowCount: 1,
		})
		require.Equal(t, "138****5678", response.Result.Rows[0][0])
		require.Empty(t, response.Redact.UnresolvedScopedColumns)
	})

	t.Run("union projection fails closed", func(t *testing.T) {
		fixture := newT46S4Fixture(t, []mask.Rule{
			{Table: "customers", Column: "phone", SensitiveType: mask.TypePhone, Algorithm: mask.AlgoMask},
		})
		response := runT46S4Query(t, fixture, "SELECT phone FROM customers UNION SELECT phone FROM orders", model.QueryResult{
			Columns: []string{"phone"}, Rows: [][]string{{"13812345678"}}, RowCount: 1,
		})
		require.Equal(t, mask.BlockPlaceholder, response.Result.Rows[0][0])
		require.Empty(t, response.Redact.UnresolvedScopedColumns)
	})

	t.Run("unrelated derived table is not affected", func(t *testing.T) {
		fixture := newT46S4Fixture(t, []mask.Rule{
			{Table: "customers", Column: "phone", SensitiveType: mask.TypePhone, Algorithm: mask.AlgoMask},
		})
		response := runT46S4Query(t, fixture, "SELECT phone FROM (SELECT phone FROM orders) x", model.QueryResult{
			Columns: []string{"phone"}, Rows: [][]string{{"unchanged"}}, RowCount: 1,
		})
		require.Equal(t, "unchanged", response.Result.Rows[0][0])
		require.Zero(t, response.Redact.MaskedCells)
		require.Empty(t, response.Redact.UnresolvedScopedColumns)
	})
}

func TestT46S4PipelineGlobalRuleRegression(t *testing.T) {
	for _, test := range []struct {
		name string
		sql  string
	}{
		{name: "single table", sql: "SELECT phone FROM customers"},
		{name: "star", sql: "SELECT * FROM customers"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newT46S4Fixture(t, []mask.Rule{
				{Column: "phone", SensitiveType: mask.TypePhone, Algorithm: mask.AlgoMask},
			})
			response := runT46S4Query(t, fixture, test.sql, model.QueryResult{
				Columns: []string{"phone"}, Rows: [][]string{{"13812345678"}}, RowCount: 1,
			})
			require.Equal(t, "138****5678", response.Result.Rows[0][0])
			require.Empty(t, response.Redact.UnresolvedScopedColumns)
		})
	}

	t.Run("ambiguous join column blocks", func(t *testing.T) {
		fixture := newT46S4Fixture(t, []mask.Rule{
			{Column: "phone", SensitiveType: mask.TypePhone, Algorithm: mask.AlgoMask},
		})
		response := runT46S4Query(t, fixture,
			"SELECT phone FROM customers JOIN orders ON customers.id = orders.id",
			model.QueryResult{Columns: []string{"phone"}, Rows: [][]string{{"13812345678"}}, RowCount: 1},
		)
		require.Equal(t, mask.BlockPlaceholder, response.Result.Rows[0][0])
		require.Equal(t, map[int]mask.SensitiveType{0: mask.TypePhone}, response.Redact.UnresolvedScopedColumns)
	})

	t.Run("renamed global block is not weakened by scoped mask", func(t *testing.T) {
		fixture := newT46S4Fixture(t, []mask.Rule{
			{Column: "mobile", SensitiveType: mask.TypeGeneric, Algorithm: mask.AlgoBlock},
			{Table: "customers", Column: "phone", SensitiveType: mask.TypePhone, Algorithm: mask.AlgoMask},
		})
		response := runT46S4Query(t, fixture, "SELECT phone AS mobile FROM customers", model.QueryResult{
			Columns: []string{"mobile"}, Rows: [][]string{{"13812345678"}}, RowCount: 1,
		})
		require.Equal(t, mask.BlockPlaceholder, response.Result.Rows[0][0])
		require.Equal(t, mask.TypePhone, response.Redact.TouchedColumns[0])
		require.Empty(t, response.Redact.UnresolvedScopedColumns)
	})
}

func TestT46S4PipelineFallbackReportsOnlyChangedCells(t *testing.T) {
	for _, test := range []struct {
		name   string
		result model.QueryResult
	}{
		{name: "empty result", result: model.QueryResult{Columns: []string{"phone"}}},
		{name: "empty and null sentinels", result: model.QueryResult{
			Columns: []string{"phone"}, Rows: [][]string{{""}, {"  "}, {"NULL"}, {"<nil>"}}, RowCount: 4,
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newT46S4Fixture(t, []mask.Rule{
				{Table: "customers", Column: "phone", SensitiveType: mask.TypePhone, Algorithm: mask.AlgoMask},
			})
			response := runT46S4Query(t, fixture, "SELECT phone FROM customers JOIN orders ON customers.id = orders.id", test.result)
			require.Equal(t, test.result.Rows, response.Result.Rows)
			require.Zero(t, response.Redact.MaskedCells)
			require.Empty(t, response.Redact.UnresolvedScopedColumns)
		})
	}
}

func TestT46S4PipelineFallsBackToSourceAwareV1(t *testing.T) {
	fixture := newPipelineFixture(t)
	legacy := &legacySourceAwareRedactor{}
	fixture.redactors.redactor = legacy
	fixture.executor.queryResult = model.QueryResult{
		Columns: []string{"mobile"}, Rows: [][]string{{"unchanged"}}, RowCount: 1,
	}

	response, err := fixture.pipeline.Process(context.Background(), requestWithSQL("SELECT phone AS mobile FROM public.customers WHERE id = 1 LIMIT 1"))
	require.NoError(t, err)
	require.Equal(t, []string{"phone"}, legacy.sources)
	require.Equal(t, "source-aware-v1", response.Result.Rows[0][0])
}

func TestT46S4PipelineLegacyFallbackPrefersRelationAware(t *testing.T) {
	fixture := newPipelineFixture(t)
	legacy := &legacyRelationAwareRedactor{}
	fixture.redactors.redactor = legacy
	fixture.executor.queryResult = model.QueryResult{
		Columns: []string{"mobile"}, Rows: [][]string{{"unchanged"}}, RowCount: 1,
	}

	response, err := fixture.pipeline.Process(context.Background(), requestWithSQL(
		"SELECT phone AS mobile FROM public.customers WHERE id = 1 LIMIT 1",
	))
	require.NoError(t, err)
	require.Equal(t, 1, legacy.relationCalls)
	require.Zero(t, legacy.sourceCalls)
	require.Zero(t, legacy.applyCalls)
	require.Equal(t, []mask.ColumnSource{{
		Column: "phone", Source: model.ObjectRef{Schema: "public", Table: "customers"},
	}}, legacy.sources)
	require.Equal(t, []model.ObjectRef{{Schema: "public", Table: "customers"}}, legacy.relations)
	require.Equal(t, "relation-aware", response.Result.Rows[0][0])
}

type legacySourceAwareRedactor struct {
	sources []string
}

type legacyRelationAwareRedactor struct {
	applyCalls    int
	sourceCalls   int
	relationCalls int
	sources       []mask.ColumnSource
	relations     []model.ObjectRef
}

func (redactor *legacySourceAwareRedactor) Apply(result model.QueryResult) (model.QueryResult, mask.RedactReport) {
	return result, mask.RedactReport{}
}

func (redactor *legacySourceAwareRedactor) ApplyWithSourceColumns(result model.QueryResult, sources []string) (model.QueryResult, mask.RedactReport) {
	redactor.sources = append([]string(nil), sources...)
	result.Rows[0][0] = "source-aware-v1"
	return result, mask.RedactReport{MaskedCells: 1}
}

func (redactor *legacyRelationAwareRedactor) Apply(result model.QueryResult) (model.QueryResult, mask.RedactReport) {
	redactor.applyCalls++
	return result, mask.RedactReport{}
}

func (redactor *legacyRelationAwareRedactor) ApplyWithSourceColumns(result model.QueryResult, _ []string) (model.QueryResult, mask.RedactReport) {
	redactor.sourceCalls++
	return result, mask.RedactReport{}
}

func (redactor *legacyRelationAwareRedactor) ApplyWithColumnSources(
	result model.QueryResult,
	sources []mask.ColumnSource,
	possibleRelations []model.ObjectRef,
) (model.QueryResult, mask.RedactReport) {
	redactor.relationCalls++
	redactor.sources = append([]mask.ColumnSource(nil), sources...)
	redactor.relations = append([]model.ObjectRef(nil), possibleRelations...)
	result.Rows[0][0] = "relation-aware"
	return result, mask.RedactReport{MaskedCells: 1}
}

func newT46S4Fixture(t *testing.T, rules []mask.Rule) *pipelineFixture {
	t.Helper()
	fixture := newPipelineFixture(t)
	redactor, err := mask.NewRedactor(rules)
	require.NoError(t, err)
	fixture.redactors.redactor = redactor
	fixture.policies.policies = append(fixture.policies.policies,
		model.Policy{ID: "t46-customers", AgentID: "agent-1", DatasourceID: "datasource-1", ObjectType: "table", ObjectName: "customers", Action: "allow"},
		model.Policy{ID: "t46-orders", AgentID: "agent-1", DatasourceID: "datasource-1", ObjectType: "table", ObjectName: "orders", Action: "allow"},
	)
	return fixture
}

func runT46S4Query(t *testing.T, fixture *pipelineFixture, sql string, result model.QueryResult) Response {
	t.Helper()
	fixture.executor.queryResult = result
	response, err := fixture.pipeline.Process(context.Background(), requestWithSQL(sql))
	require.NoError(t, err)
	require.Equal(t, model.DecisionAllow, response.Decision)
	require.NotNil(t, response.Result)
	return response
}
