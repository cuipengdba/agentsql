package parser

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func BenchmarkParseProjectionLineage(b *testing.B) {
	for _, dialect := range []model.DBDialect{mysqlDialect, postgresDialect} {
		dialect := dialect
		b.Run(string(dialect), func(b *testing.B) {
			for _, depth := range []int{1, 8, 32, 64} {
				depth := depth
				b.Run(fmt.Sprintf("depth_%d", depth), func(b *testing.B) {
					benchmarkProjectionLineageSQL(b, dialect, projectionLineageDepthSQL(dialect, depth))
				})
			}
			for _, columns := range []int{10, 100, 1000} {
				columns := columns
				b.Run(fmt.Sprintf("columns_%d", columns), func(b *testing.B) {
					benchmarkProjectionLineageSQL(b, dialect, projectionLineageColumnsSQL(dialect, columns))
				})
			}
			for _, arms := range []int{1, 8, 64} {
				arms := arms
				b.Run(fmt.Sprintf("set_arms_%d", arms), func(b *testing.B) {
					benchmarkProjectionLineageSQL(b, dialect, projectionLineageSetSQL(dialect, arms, 1))
				})
			}
			b.Run("multiple_stars", func(b *testing.B) {
				benchmarkProjectionLineageSQL(b, dialect, projectionLineageMultipleStarsSQL(dialect))
			})
		})
	}
}

func benchmarkProjectionLineageSQL(b *testing.B, dialect model.DBDialect, sql string) {
	b.Helper()
	approved, err := NewParser(dialect)
	require.NoError(b, err)
	ast, err := approved.Parse(sql)
	require.NoError(b, err)
	require.NotEmpty(b, ast.ProjectionLineages)
	b.ReportAllocs()
	b.SetBytes(int64(len(sql)))
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		if _, parseErr := approved.Parse(sql); parseErr != nil {
			b.Fatal(parseErr)
		}
	}
}

func TestParseProjectionLineageP99Budget(t *testing.T) {
	if raceEnabled {
		t.Skip("hard latency budget is not meaningful under -race (instrumentation adds multi-x overhead); budget covered by non-race runs")
	}
	const (
		typicalIterations = 2000
		scaleIterations   = 200
		typicalP99Budget  = 5 * time.Millisecond
	)
	for _, dialect := range []model.DBDialect{mysqlDialect, postgresDialect} {
		dialect := dialect
		t.Run(string(dialect), func(t *testing.T) {
			approved, err := NewParser(dialect)
			require.NoError(t, err)

			typicalSQL := projectionLineageSetSQL(dialect, 8, 8)
			typical := measureProjectionLineageParse(t, approved, typicalSQL, typicalIterations)
			t.Logf("%s typical arms=8 columns=8 parses=%d P50=%s P90=%s P95=%s P99=%s", dialect, typicalIterations, typical.p50, typical.p90, typical.p95, typical.p99)
			require.LessOrEqual(t, typical.p99, typicalP99Budget, "parser total P99 is the available lineage budget proxy; the parser has no lineage-off mode")

			column100 := measureProjectionLineageParse(t, approved, projectionLineageColumnsSQL(dialect, 100), scaleIterations)
			column1000 := measureProjectionLineageParse(t, approved, projectionLineageColumnsSQL(dialect, 1000), scaleIterations)
			columnRatio := durationRatio(column1000.mean, column100.mean)
			t.Logf("%s columns 100 P50=%s P95=%s P99=%s mean=%s; 1000 P50=%s P95=%s P99=%s mean=%s; growth=%.2fx (input=10x)", dialect, column100.p50, column100.p95, column100.p99, column100.mean, column1000.p50, column1000.p95, column1000.p99, column1000.mean, columnRatio)
			require.LessOrEqual(t, columnRatio, 30.0, "column growth exceeded 3x the 10x input growth")

			arms8 := measureProjectionLineageParse(t, approved, projectionLineageSetSQL(dialect, 8, 1), scaleIterations)
			arms64 := measureProjectionLineageParse(t, approved, projectionLineageSetSQL(dialect, 64, 1), scaleIterations)
			armRatio := durationRatio(arms64.mean, arms8.mean)
			t.Logf("%s set arms 8 P50=%s P95=%s P99=%s mean=%s; 64 P50=%s P95=%s P99=%s mean=%s; growth=%.2fx (input=8x)", dialect, arms8.p50, arms8.p95, arms8.p99, arms8.mean, arms64.p50, arms64.p95, arms64.p99, arms64.mean, armRatio)
			require.LessOrEqual(t, armRatio, 24.0, "set-arm growth exceeded 3x the 8x input growth")

			depth8 := measureProjectionLineageParse(t, approved, projectionLineageDepthSQL(dialect, 8), scaleIterations)
			depth64 := measureProjectionLineageParse(t, approved, projectionLineageDepthSQL(dialect, 64), scaleIterations)
			depthRatio := durationRatio(depth64.mean, depth8.mean)
			t.Logf("%s depth 8 P50=%s P95=%s P99=%s mean=%s; 64 P50=%s P95=%s P99=%s mean=%s; growth=%.2fx (input=8x)", dialect, depth8.p50, depth8.p95, depth8.p99, depth8.mean, depth64.p50, depth64.p95, depth64.p99, depth64.mean, depthRatio)
			require.LessOrEqual(t, depthRatio, 24.0, "depth growth exceeded 3x the 8x input growth")

			assertProjectionLineageHardLimitsFast(t, approved, dialect)
		})
	}
}

type projectionLineageTimings struct {
	p50  time.Duration
	p90  time.Duration
	p95  time.Duration
	p99  time.Duration
	mean time.Duration
}

func measureProjectionLineageParse(t *testing.T, approved Parser, sql string, iterations int) projectionLineageTimings {
	t.Helper()
	for warmup := 0; warmup < 20; warmup++ {
		_, err := approved.Parse(sql)
		require.NoError(t, err)
	}
	durations := make([]time.Duration, iterations)
	var total time.Duration
	for iteration := range durations {
		started := time.Now()
		ast, err := approved.Parse(sql)
		durations[iteration] = time.Since(started)
		require.NoError(t, err)
		require.NotEmpty(t, ast.ProjectionLineages)
		total += durations[iteration]
	}
	sort.Slice(durations, func(left, right int) bool { return durations[left] < durations[right] })
	return projectionLineageTimings{
		p50:  percentileDuration(durations, 50),
		p90:  percentileDuration(durations, 90),
		p95:  percentileDuration(durations, 95),
		p99:  percentileDuration(durations, 99),
		mean: total / time.Duration(iterations),
	}
}

func percentileDuration(sorted []time.Duration, percentile int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	index := (len(sorted)*percentile + 99) / 100
	if index < 1 {
		index = 1
	}
	if index > len(sorted) {
		index = len(sorted)
	}
	return sorted[index-1]
}

func durationRatio(larger, smaller time.Duration) float64 {
	if smaller <= 0 {
		return 0
	}
	return float64(larger) / float64(smaller)
}

func assertProjectionLineageHardLimitsFast(t *testing.T, approved Parser, dialect model.DBDialect) {
	t.Helper()
	limitCases := []struct {
		name string
		sql  string
	}{
		{"projection slots", projectionLineageColumnsSQL(dialect, lineageMaxProjections+1)},
		{"set leaves", projectionLineageSetSQL(dialect, lineageMaxSetLeaves+1, 1)},
		{"nesting depth", projectionLineageDepthSQL(dialect, lineageMaxDepth+1)},
	}
	for _, test := range limitCases {
		started := time.Now()
		_, err := approved.Parse(test.sql)
		elapsed := time.Since(started)
		require.Error(t, err, test.name)
		require.True(t, errors.Is(err, ErrUnparseable), "%s: %v", test.name, err)
		require.Less(t, elapsed, 250*time.Millisecond, "%s hard-limit rejection degraded: %s", test.name, elapsed)
		t.Logf("%s hard limit %s rejected in %s", dialect, test.name, elapsed)
	}
}

func projectionLineageDepthSQL(dialect model.DBDialect, depth int) string {
	expression := "c0.phone"
	for level := 1; level <= depth; level++ {
		expression = fmt.Sprintf("(SELECT %s FROM customers c%d)", expression, level)
	}
	return "SELECT " + expression + " AS value FROM customers c0"
}

func projectionLineageColumnsSQL(_ model.DBDialect, columns int) string {
	projections := make([]string, columns)
	for column := range projections {
		projections[column] = fmt.Sprintf("c.phone AS phone_%d", column)
	}
	return "SELECT " + strings.Join(projections, ",") + " FROM customers c"
}

func projectionLineageSetSQL(_ model.DBDialect, arms, columns int) string {
	if arms < 1 {
		arms = 1
	}
	leaves := make([]string, arms)
	for arm := range leaves {
		projections := make([]string, columns)
		for column := range projections {
			projections[column] = fmt.Sprintf("c.phone AS phone_%d", column)
		}
		leaves[arm] = "SELECT " + strings.Join(projections, ",") + " FROM customers c"
	}
	return strings.Join(leaves, " UNION ALL ")
}

func projectionLineageMultipleStarsSQL(_ model.DBDialect) string {
	return "SELECT c.*, c.phone AS between_one, o.*, c.email AS between_two, c.* " +
		"FROM customers c JOIN orders o ON o.customer_id=c.id"
}
