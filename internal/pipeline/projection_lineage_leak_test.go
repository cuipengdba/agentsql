package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/rules"
	"github.com/stretchr/testify/require"
)

type lineageLeakExpectation string

const (
	lineageLeakExact      lineageLeakExpectation = "exact"
	lineageLeakBlock      lineageLeakExpectation = "block"
	lineageLeakParseError lineageLeakExpectation = "parse_error"
)

type lineageLeakCase struct {
	name        string
	sql         string
	expect      lineageLeakExpectation
	sentinel    string
	columns     []string
	wantMasked  int
	wantTouched map[int]mask.SensitiveType
}

func TestProjectionLineageLeakNegativeSet(t *testing.T) {
	for _, dialect := range []string{"mysql", "postgres"} {
		dialect := dialect
		t.Run(dialect, func(t *testing.T) {
			cases := projectionLineageLeakCases(dialect)
			seen := make(map[string]string, len(cases))
			for _, test := range cases {
				test := test
				t.Run(test.name, func(t *testing.T) {
					if previous, exists := seen[test.sentinel]; exists {
						t.Fatalf("sentinel %q reused by %s and %s", test.sentinel, previous, test.name)
					}
					seen[test.sentinel] = test.name
					runProjectionLineageLeakCase(t, dialect, test)
				})
			}
		})
	}
}

func runProjectionLineageLeakCase(t *testing.T, dialect string, test lineageLeakCase) {
	t.Helper()
	rows := [][]string{make([]string, len(test.columns))}
	for column := range rows[0] {
		rows[0][column] = test.sentinel
	}
	delegate := &scriptedLineageLeakExecutor{
		dialect: dialect,
		result: model.QueryResult{
			Columns:  append([]string(nil), test.columns...),
			Rows:     rows,
			RowCount: 1,
		},
	}
	counted := &countingDatabaseExecutor{delegate: &fakeExecutorProvider{executor: delegate}}
	datasource := lineageLeakDatasource(dialect)
	flow, ports := newDatabaseE2EPipelineWithRules(
		t,
		datasource,
		counted,
		"dml",
		lineageLeakAllowedTables(dialect),
		lineageLeakRules(),
		nil,
	)
	response, err := flow.Process(context.Background(), databaseE2ERequest(datasource.ID, test.sql))
	if test.expect == lineageLeakParseError {
		require.NoError(t, err, "%s sentinel=%s", test.name, test.sentinel)
		require.Equal(t, model.DecisionError, response.Decision)
		require.Equal(t, string(executor.DBErrorCodeSyntax), response.ErrorCode)
		require.Equal(t, string(executor.DBStageParse), response.ErrorStage)
		require.Nil(t, response.Result, "%s sentinel=%s", test.name, test.sentinel)
		require.Zero(t, delegate.queryCalls, "%s must fail before execution", test.name)
		require.Equal(t, "error", ports.audit.last().Decision)
		require.NotNil(t, ports.audit.last().ErrorCode)
		require.Equal(t, string(executor.DBErrorCodeSyntax), *ports.audit.last().ErrorCode)
		assertLineageSentinelAbsent(t, test.name, test.sentinel, response, ports.audit)
		return
	}

	require.NoError(t, err, "%s sentinel=%s", test.name, test.sentinel)
	require.Equal(t, model.DecisionAllow, response.Decision, "%s sentinel=%s", test.name, test.sentinel)
	require.NotNil(t, response.Result, "%s sentinel=%s", test.name, test.sentinel)
	require.Equal(t, test.wantMasked, response.Redact.MaskedCells, "%s sentinel=%s", test.name, test.sentinel)
	require.Equal(t, test.wantTouched, response.Redact.TouchedColumns, "%s sentinel=%s", test.name, test.sentinel)
	for rowIndex, row := range response.Result.Rows {
		for columnIndex, cell := range row {
			require.NotContains(t, cell, test.sentinel, "%s sentinel=%s row=%d column=%d", test.name, test.sentinel, rowIndex, columnIndex)
			switch test.expect {
			case lineageLeakExact:
				require.Equal(t, maskPhoneSentinel(test.sentinel), cell, "%s sentinel=%s", test.name, test.sentinel)
			case lineageLeakBlock:
				require.Equal(t, mask.BlockPlaceholder, cell, "%s sentinel=%s", test.name, test.sentinel)
			default:
				t.Fatalf("%s: unsupported expectation %q", test.name, test.expect)
			}
		}
	}
	assertLineageSentinelAbsent(t, test.name, test.sentinel, response, ports.audit)
}

func projectionLineageLeakCases(dialect string) []lineageLeakCase {
	qualifier := "test"
	if dialect == "postgres" {
		qualifier = "public"
	}
	customers := qualifier + ".customers"
	orders := qualifier + ".orders"
	archive := qualifier + ".archive"
	view := qualifier + ".customer_view"

	phoneCounter := 0
	emailCounter := 0
	genericCounter := 0
	phone := func() string {
		phoneCounter++
		prefix := "13910"
		if dialect == "postgres" {
			prefix = "13720"
		}
		return fmt.Sprintf("%s%06d", prefix, phoneCounter)
	}
	email := func() string {
		emailCounter++
		return fmt.Sprintf("%s-unique-%03d@example.test", dialect, emailCounter)
	}
	generic := func() string {
		genericCounter++
		return fmt.Sprintf("%s_UNIQUE_SECRET_%03d", strings.ToUpper(dialect), genericCounter)
	}
	one := func(name, sql string, expect lineageLeakExpectation, sentinel string, sensitiveType mask.SensitiveType) lineageLeakCase {
		return lineageLeakCase{
			name: name, sql: sql, expect: expect, sentinel: sentinel,
			columns: []string{"leak"}, wantMasked: 1,
			wantTouched: map[int]mask.SensitiveType{0: sensitiveType},
		}
	}

	var cases []lineageLeakCase
	if dialect == "mysql" {
		cases = append(cases,
			one("transparent concat empty", fmt.Sprintf("SELECT CONCAT(c.phone, '') AS leak FROM %s c LIMIT 1", customers), lineageLeakExact, phone(), mask.TypePhone),
			one("upper email", fmt.Sprintf("SELECT UPPER(c.email) AS leak FROM %s c LIMIT 1", customers), lineageLeakBlock, email(), mask.TypeEmail),
			one("lower email", fmt.Sprintf("SELECT LOWER(c.email) AS leak FROM %s c LIMIT 1", customers), lineageLeakBlock, email(), mask.TypeEmail),
			one("trim email", fmt.Sprintf("SELECT TRIM(c.email) AS leak FROM %s c LIMIT 1", customers), lineageLeakBlock, email(), mask.TypeEmail),
			one("cast phone", fmt.Sprintf("SELECT CAST(c.phone AS CHAR) AS leak FROM %s c LIMIT 1", customers), lineageLeakBlock, phone(), mask.TypePhone),
			one("substring phone", fmt.Sprintf("SELECT SUBSTRING(c.phone, 2) AS leak FROM %s c LIMIT 1", customers), lineageLeakBlock, phone(), mask.TypePhone),
			one("left phone", fmt.Sprintf("SELECT LEFT(c.phone, 5) AS leak FROM %s c LIMIT 1", customers), lineageLeakBlock, phone(), mask.TypePhone),
			one("right phone", fmt.Sprintf("SELECT RIGHT(c.phone, 5) AS leak FROM %s c LIMIT 1", customers), lineageLeakBlock, phone(), mask.TypePhone),
			one("arithmetic phone", fmt.Sprintf("SELECT c.phone + 1 AS leak FROM %s c LIMIT 1", customers), lineageLeakBlock, phone(), mask.TypePhone),
			one("date operation phone", fmt.Sprintf("SELECT DATE_ADD(c.phone, INTERVAL 1 DAY) AS leak FROM %s c LIMIT 1", customers), lineageLeakBlock, phone(), mask.TypePhone),
			one("nonempty concat", fmt.Sprintf("SELECT CONCAT(c.name, c.phone) AS leak FROM %s c LIMIT 1", customers), lineageLeakBlock, generic(), mask.TypePhone),
			one("repeated phone", fmt.Sprintf("SELECT CONCAT(c.phone, c.phone) AS leak FROM %s c LIMIT 1", customers), lineageLeakBlock, phone(), mask.TypePhone),
			one("json extract", fmt.Sprintf("SELECT JSON_UNQUOTE(JSON_EXTRACT(c.payload, '$.phone')) AS leak FROM %s c LIMIT 1", customers), lineageLeakBlock, generic(), mask.TypeGeneric),
			one("json arrow", fmt.Sprintf("SELECT c.payload->'$.phone' AS leak FROM %s c LIMIT 1", customers), lineageLeakBlock, generic(), mask.TypeGeneric),
			one("json text arrow", fmt.Sprintf("SELECT c.payload->>'$.phone' AS leak FROM %s c LIMIT 1", customers), lineageLeakBlock, generic(), mask.TypeGeneric),
		)
	} else {
		cases = append(cases,
			one("transparent concat empty", fmt.Sprintf("SELECT c.phone || '' AS leak FROM %s c LIMIT 1", customers), lineageLeakExact, phone(), mask.TypePhone),
			one("upper email", fmt.Sprintf("SELECT UPPER(c.email) AS leak FROM %s c LIMIT 1", customers), lineageLeakBlock, email(), mask.TypeEmail),
			one("lower email", fmt.Sprintf("SELECT LOWER(c.email) AS leak FROM %s c LIMIT 1", customers), lineageLeakBlock, email(), mask.TypeEmail),
			one("trim email", fmt.Sprintf("SELECT TRIM(c.email) AS leak FROM %s c LIMIT 1", customers), lineageLeakBlock, email(), mask.TypeEmail),
			one("cast phone", fmt.Sprintf("SELECT c.phone::text AS leak FROM %s c LIMIT 1", customers), lineageLeakBlock, phone(), mask.TypePhone),
			one("substring phone", fmt.Sprintf("SELECT SUBSTRING(c.phone FROM 2) AS leak FROM %s c LIMIT 1", customers), lineageLeakBlock, phone(), mask.TypePhone),
			one("left phone", fmt.Sprintf("SELECT LEFT(c.phone, 5) AS leak FROM %s c LIMIT 1", customers), lineageLeakBlock, phone(), mask.TypePhone),
			one("right phone", fmt.Sprintf("SELECT RIGHT(c.phone, 5) AS leak FROM %s c LIMIT 1", customers), lineageLeakBlock, phone(), mask.TypePhone),
			one("arithmetic phone", fmt.Sprintf("SELECT c.phone::bigint + 1 AS leak FROM %s c LIMIT 1", customers), lineageLeakBlock, phone(), mask.TypePhone),
			one("date operation phone", fmt.Sprintf("SELECT c.phone::date + INTERVAL '1 day' AS leak FROM %s c LIMIT 1", customers), lineageLeakBlock, phone(), mask.TypePhone),
			one("nonempty concat", fmt.Sprintf("SELECT c.name || c.phone AS leak FROM %s c LIMIT 1", customers), lineageLeakBlock, generic(), mask.TypePhone),
			one("repeated phone", fmt.Sprintf("SELECT c.phone || c.phone AS leak FROM %s c LIMIT 1", customers), lineageLeakBlock, phone(), mask.TypePhone),
			one("json arrow", fmt.Sprintf("SELECT c.payload->'phone' AS leak FROM %s c LIMIT 1", customers), lineageLeakBlock, generic(), mask.TypeGeneric),
			one("json text arrow", fmt.Sprintf("SELECT c.payload->>'phone' AS leak FROM %s c LIMIT 1", customers), lineageLeakBlock, generic(), mask.TypeGeneric),
		)
	}

	cases = append(cases,
		one("case value dependency", fmt.Sprintf("SELECT CASE WHEN c.id > 0 THEN c.phone ELSE '' END AS leak FROM %s c LIMIT 1", customers), lineageLeakBlock, phone(), mask.TypePhone),
		one("case control dependency", fmt.Sprintf("SELECT CASE WHEN c.phone <> '' THEN 'yes' ELSE 'no' END AS leak FROM %s c LIMIT 1", customers), lineageLeakBlock, generic(), mask.TypePhone),
		one("count phone", fmt.Sprintf("SELECT COUNT(c.phone) AS leak FROM %s c", customers), lineageLeakBlock, generic(), mask.TypePhone),
		one("count distinct phone", fmt.Sprintf("SELECT COUNT(DISTINCT c.phone) AS leak FROM %s c", customers), lineageLeakBlock, generic(), mask.TypePhone),
		one("max phone", fmt.Sprintf("SELECT MAX(c.phone) AS leak FROM %s c", customers), lineageLeakBlock, phone(), mask.TypePhone),
		one("min phone", fmt.Sprintf("SELECT MIN(c.phone) AS leak FROM %s c", customers), lineageLeakBlock, phone(), mask.TypePhone),
		one("sum phone", fmt.Sprintf("SELECT SUM(c.phone) AS leak FROM %s c", customers), lineageLeakBlock, generic(), mask.TypePhone),
		one("avg phone", fmt.Sprintf("SELECT AVG(c.phone) AS leak FROM %s c", customers), lineageLeakBlock, generic(), mask.TypePhone),
	)
	if dialect == "mysql" {
		cases = append(cases,
			one("group concat phone", fmt.Sprintf("SELECT GROUP_CONCAT(c.phone) AS leak FROM %s c", customers), lineageLeakBlock, phone(), mask.TypePhone),
			one("aggregate sensitive filter equivalent", fmt.Sprintf("SELECT COUNT(CASE WHEN c.phone <> '' THEN 1 END) AS leak FROM %s c", customers), lineageLeakBlock, generic(), mask.TypePhone),
		)
	} else {
		cases = append(cases,
			one("string agg phone", fmt.Sprintf("SELECT STRING_AGG(c.phone, ',') AS leak FROM %s c", customers), lineageLeakBlock, phone(), mask.TypePhone),
			one("count star sensitive filter", fmt.Sprintf("SELECT COUNT(*) FILTER (WHERE c.phone <> '') AS leak FROM %s c", customers), lineageLeakBlock, generic(), mask.TypePhone),
		)
	}
	cases = append(cases,
		one("window sensitive value", fmt.Sprintf("SELECT MAX(c.phone) OVER () AS leak FROM %s c LIMIT 1", customers), lineageLeakBlock, phone(), mask.TypePhone),
		one("window partition sensitive", fmt.Sprintf("SELECT COUNT(*) OVER (PARTITION BY c.phone) AS leak FROM %s c LIMIT 1", customers), lineageLeakBlock, generic(), mask.TypePhone),
		one("window order sensitive", fmt.Sprintf("SELECT COUNT(*) OVER (ORDER BY c.phone) AS leak FROM %s c LIMIT 1", customers), lineageLeakBlock, generic(), mask.TypePhone),
		one("row number partition sensitive", fmt.Sprintf("SELECT ROW_NUMBER() OVER (PARTITION BY c.phone) AS leak FROM %s c LIMIT 1", customers), lineageLeakBlock, generic(), mask.TypePhone),
	)
	if dialect == "postgres" {
		cases = append(cases,
			one("distinct on sensitive", fmt.Sprintf("SELECT DISTINCT ON (c.phone) c.name AS leak FROM %s c ORDER BY c.phone LIMIT 1", customers), lineageLeakBlock, generic(), mask.TypePhone),
		)
	}

	sameSet := fmt.Sprintf("SELECT c.phone AS leak FROM %s c UNION ALL SELECT c.phone AS leak FROM %s c LIMIT 1", customers, customers)
	differentSet := fmt.Sprintf("SELECT c.phone AS leak FROM %s c UNION ALL SELECT o.phone AS leak FROM %s o LIMIT 1", customers, orders)
	constantSet := fmt.Sprintf("SELECT c.phone AS leak FROM %s c UNION ALL SELECT 'safe' AS leak LIMIT 1", customers)
	cases = append(cases,
		one("same table union exact", sameSet, lineageLeakExact, phone(), mask.TypePhone),
		one("different table union blocked", differentSet, lineageLeakBlock, phone(), mask.TypePhone),
		one("constant union arm blocked", constantSet, lineageLeakBlock, phone(), mask.TypePhone),
	)
	if dialect == "mysql" {
		cases = append(cases,
			one("intersect rejected by dialect", fmt.Sprintf("SELECT c.phone AS leak FROM %s c INTERSECT SELECT a.phone AS leak FROM %s a", customers, archive), lineageLeakParseError, phone(), mask.TypePhone),
			one("except rejected by dialect", fmt.Sprintf("SELECT c.phone AS leak FROM %s c EXCEPT SELECT a.phone AS leak FROM %s a", customers, archive), lineageLeakParseError, phone(), mask.TypePhone),
		)
	} else {
		cases = append(cases,
			one("different table intersect blocked", fmt.Sprintf("SELECT c.phone AS leak FROM %s c INTERSECT SELECT a.phone AS leak FROM %s a LIMIT 1", customers, archive), lineageLeakBlock, phone(), mask.TypePhone),
			one("different table except blocked", fmt.Sprintf("SELECT c.phone AS leak FROM %s c EXCEPT SELECT a.phone AS leak FROM %s a LIMIT 1", customers, archive), lineageLeakBlock, phone(), mask.TypePhone),
		)
	}
	multiSentinel := phone()
	cases = append(cases, lineageLeakCase{
		name: "multi column union exact", sentinel: multiSentinel, expect: lineageLeakExact,
		sql:     fmt.Sprintf("SELECT c.phone AS p1, c.phone AS p2 FROM %s c UNION ALL SELECT c.phone, c.phone FROM %s c LIMIT 1", customers, customers),
		columns: []string{"p1", "p2"}, wantMasked: 2,
		wantTouched: map[int]mask.SensitiveType{0: mask.TypePhone, 1: mask.TypePhone},
	})
	nestedSet := fmt.Sprintf("SELECT n.phone AS leak FROM (SELECT c.phone FROM %s c UNION ALL SELECT c.phone FROM %s c) n UNION ALL SELECT a.phone FROM %s a LIMIT 1", customers, customers, archive)
	cases = append(cases, one("nested set different source blocked", nestedSet, lineageLeakBlock, phone(), mask.TypePhone))

	deepCTEParts := []string{fmt.Sprintf("x1(phone) AS (SELECT c.phone FROM %s c)", customers)}
	for level := 2; level <= 8; level++ {
		deepCTEParts = append(deepCTEParts, fmt.Sprintf("x%d(phone) AS (SELECT phone FROM x%d)", level, level-1))
	}
	deepCTE := "WITH " + strings.Join(deepCTEParts, ",") + " SELECT phone AS leak FROM x8 LIMIT 1"

	cases = append(cases,
		one("join unqualified star", fmt.Sprintf("SELECT * FROM %s c JOIN %s o ON c.id=o.id LIMIT 1", customers, orders), lineageLeakBlock, phone(), mask.TypePhone),
		one("natural join merged key", fmt.Sprintf("SELECT phone AS leak FROM %s c NATURAL JOIN %s o LIMIT 1", customers, orders), lineageLeakBlock, phone(), mask.TypePhone),
		one("using join merged key", fmt.Sprintf("SELECT phone AS leak FROM %s c JOIN %s o USING (phone) LIMIT 1", customers, orders), lineageLeakBlock, phone(), mask.TypePhone),
		one("multiple stars around expression", fmt.Sprintf("SELECT c.*, CONCAT(c.phone, 'x') AS leak, o.* FROM %s c JOIN %s o ON c.id=o.id LIMIT 1", customers, orders), lineageLeakBlock, phone(), mask.TypePhone),
		one("cte direct", fmt.Sprintf("WITH x AS (SELECT c.phone FROM %s c) SELECT x.phone AS leak FROM x LIMIT 1", customers), lineageLeakExact, phone(), mask.TypePhone),
		one("cte column override", fmt.Sprintf("WITH x(mobile) AS (SELECT c.phone FROM %s c) SELECT x.mobile AS leak FROM x LIMIT 1", customers), lineageLeakExact, phone(), mask.TypePhone),
		one("deep cte direct", deepCTE, lineageLeakExact, phone(), mask.TypePhone),
		one("cte column mismatch", fmt.Sprintf("WITH x(a,b) AS (SELECT c.phone FROM %s c) SELECT x.a AS leak FROM x LIMIT 1", customers), lineageLeakBlock, phone(), mask.TypePhone),
		one("cte duplicate output alias", fmt.Sprintf("WITH x AS (SELECT c.phone AS value, c.email AS value FROM %s c) SELECT x.value AS leak FROM x LIMIT 1", customers), lineageLeakBlock, phone(), mask.TypePhone),
		one("derived direct", fmt.Sprintf("SELECT x.phone AS leak FROM (SELECT c.phone FROM %s c) x LIMIT 1", customers), lineageLeakExact, phone(), mask.TypePhone),
		one("correlated scalar", fmt.Sprintf("SELECT (SELECT o.phone FROM %s o WHERE o.id=c.id) AS leak FROM %s c LIMIT 1", orders, customers), lineageLeakBlock, phone(), mask.TypePhone),
		one("noncorrelated scalar direct", fmt.Sprintf("SELECT (SELECT c.phone FROM %s c) AS leak FROM %s o LIMIT 1", customers, orders), lineageLeakExact, phone(), mask.TypePhone),
		one("catalog view explicitly configured", fmt.Sprintf("SELECT v.phone AS leak FROM %s v LIMIT 1", view), lineageLeakExact, phone(), mask.TypePhone),
		one("unknown named relation composite", fmt.Sprintf("SELECT mystery(v.phone) AS leak FROM %s.unknown_relation v LIMIT 1", qualifier), lineageLeakBlock, phone(), mask.TypePhone),
		one("ambiguous unqualified column", fmt.Sprintf("SELECT phone AS leak FROM %s c JOIN %s o ON c.id=o.id LIMIT 1", customers, orders), lineageLeakBlock, phone(), mask.TypePhone),
	)
	if dialect == "postgres" {
		cases = append(cases,
			one("lateral direct", fmt.Sprintf("SELECT x.phone AS leak FROM %s c JOIN LATERAL (SELECT c.phone) x ON true LIMIT 1", customers), lineageLeakExact, phone(), mask.TypePhone),
			one("lateral rows from", fmt.Sprintf("SELECT f.x AS leak FROM %s c, LATERAL ROWS FROM (unnest(ARRAY[c.phone])) AS f(x) LIMIT 1", customers), lineageLeakBlock, phone(), mask.TypePhone),
		)
	} else {
		cases = append(cases,
			one("lateral direct", fmt.Sprintf("SELECT x.phone AS leak FROM %s c JOIN LATERAL (SELECT c.phone) x ON TRUE LIMIT 1", customers), lineageLeakExact, phone(), mask.TypePhone),
		)
	}
	return cases
}

func TestProjectionLineageLeakPrecisionReverseAssertions(t *testing.T) {
	for _, dialect := range []string{"mysql", "postgres"} {
		dialect := dialect
		t.Run(dialect, func(t *testing.T) {
			qualifier := "test"
			if dialect == "postgres" {
				qualifier = "public"
			}
			customers := qualifier + ".customers"
			visibleCases := []struct {
				name, sql, visible string
			}{
				{"count star", fmt.Sprintf("SELECT COUNT(*) AS visible FROM %s c", customers), "17"},
				{"count constant", fmt.Sprintf("SELECT COUNT(1) AS visible FROM %s c", customers), "23"},
				{"unrelated note", fmt.Sprintf("SELECT c.note AS visible FROM %s c LIMIT 1", customers), dialect + "_VISIBLE_NOTE"},
				{"constant", fmt.Sprintf("SELECT 42 AS visible FROM %s c LIMIT 1", customers), dialect + "_VISIBLE_CONSTANT_RESULT"},
				{"unrelated arithmetic", fmt.Sprintf("SELECT c.score + 1 AS visible FROM %s c LIMIT 1", customers), "101"},
			}
			for _, test := range visibleCases {
				t.Run(test.name, func(t *testing.T) {
					response, audit := runVisibleLineageValue(t, dialect, test.sql, "visible", [][]string{{test.visible}})
					require.Equal(t, test.visible, response.Result.Rows[0][0])
					require.NotEqual(t, mask.BlockPlaceholder, response.Result.Rows[0][0])
					require.Empty(t, response.Redact.TouchedColumns)
					require.Zero(t, response.Redact.MaskedCells)
					assertLineageRawAbsentFromAudit(t, test.name, test.visible, audit)
				})
			}

			t.Run("empty values remain unchanged", func(t *testing.T) {
				query := fmt.Sprintf("SELECT c.phone AS phone FROM %s c LIMIT 4", customers)
				values := [][]string{{""}, {"   "}, {"NULL"}, {"<nil>"}}
				response, _ := runVisibleLineageValue(t, dialect, query, "phone", values)
				require.Equal(t, values, response.Result.Rows)
				require.Equal(t, map[int]mask.SensitiveType{0: mask.TypePhone}, response.Redact.TouchedColumns)
				require.Zero(t, response.Redact.MaskedCells)
				require.Empty(t, response.Redact.UnresolvedScopedColumns)
			})
		})
	}
}

func runVisibleLineageValue(t *testing.T, dialect, sql, column string, rows [][]string) (Response, model.AuditLog) {
	t.Helper()
	delegate := &scriptedLineageLeakExecutor{
		dialect: dialect,
		result:  model.QueryResult{Columns: []string{column}, Rows: rows, RowCount: len(rows)},
	}
	datasource := lineageLeakDatasource(dialect)
	flow, ports := newDatabaseE2EPipelineWithRules(
		t, datasource, &countingDatabaseExecutor{delegate: &fakeExecutorProvider{executor: delegate}}, "dml",
		lineageLeakAllowedTables(dialect), lineageLeakRules(), nil,
	)
	response, err := flow.Process(context.Background(), databaseE2ERequest(datasource.ID, sql))
	require.NoError(t, err)
	require.Equal(t, model.DecisionAllow, response.Decision)
	require.NotNil(t, response.Result)
	require.Positive(t, ports.audit.calls())
	return response, ports.audit.last()
}

func TestProjectionLineageLeakInvalidAlignmentBlocks(t *testing.T) {
	customers := []model.ObjectRef{{Table: "customers"}}
	invalid := []struct {
		name     string
		lineages []model.ProjectionLineage
		columns  []string
	}{
		{"negative span", []model.ProjectionLineage{projection(0, fixedArm("customers", "id")), variadicProjection(1, wildcardArm("customers")), projection(2, fixedArm("customers", "phone"))}, []string{"phone"}},
		{"out of bounds", []model.ProjectionLineage{{SelectIndex: 3, Arms: []model.LineageArm{fixedArm("customers", "phone")}}}, []string{"phone"}},
		{"overlap duplicate index", []model.ProjectionLineage{projection(0, fixedArm("customers", "phone")), projection(0, fixedArm("customers", "email"))}, []string{"phone"}},
		{"sorting conflict", []model.ProjectionLineage{projection(1, fixedArm("customers", "phone")), projection(0, fixedArm("customers", "email")), projection(1, fixedArm("customers", "phone"))}, []string{"email", "phone"}},
		{"arm count mismatch", []model.ProjectionLineage{projection(0, fixedArm("customers", "phone")), {SelectIndex: 1, Arms: []model.LineageArm{fixedArm("customers", "phone"), fixedArm("customers", "phone")}}}, []string{"phone", "phone2"}},
		{"fixed column count mismatch", []model.ProjectionLineage{projection(0, fixedArm("customers", "phone"))}, []string{"phone", "extra"}},
	}
	redactor, err := mask.NewRedactor(lineageLeakRules())
	require.NoError(t, err)
	lineageAware, ok := redactor.(mask.ProjectionLineageAwareRedactor)
	require.True(t, ok)
	for index, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			sentinel := fmt.Sprintf("13630%06d", index+1)
			aligned := resolveProjectionLineages(test.lineages, test.columns)
			require.Len(t, aligned, len(test.columns))
			for _, arms := range aligned {
				require.Len(t, arms, 1)
				require.Equal(t, model.LineageOpaque, arms[0].Kind)
				require.Equal(t, customers, arms[0].PossibleRelations)
			}
			rows := [][]string{make([]string, len(test.columns))}
			for column := range rows[0] {
				rows[0][column] = sentinel
			}
			result, report := lineageAware.ApplyWithProjectionLineages(
				model.QueryResult{Columns: test.columns, Rows: rows, RowCount: 1}, aligned,
			)
			response := Response{Decision: model.DecisionAllow, Result: &result, Redact: report}
			encoded, marshalErr := json.Marshal(response)
			require.NoError(t, marshalErr)
			require.NotContains(t, string(encoded), sentinel, "%s sentinel=%s", test.name, sentinel)
			for _, cell := range result.Rows[0] {
				require.Equal(t, mask.BlockPlaceholder, cell, "%s sentinel=%s", test.name, sentinel)
			}
		})
	}
}

func TestProjectionLineageLeakRectangleFailuresClearResult(t *testing.T) {
	for _, dialect := range []string{"mysql", "postgres"} {
		dialect := dialect
		t.Run(dialect, func(t *testing.T) {
			qualifier := "test"
			if dialect == "postgres" {
				qualifier = "public"
			}
			query := fmt.Sprintf("SELECT c.phone, c.email FROM %s.customers c LIMIT 1", qualifier)
			for index, test := range []struct {
				name string
				rows [][]string
			}{
				{"short row", [][]string{{"sentinel"}}},
				{"long row", [][]string{{"sentinel", "value", "extra"}}},
				{"nil row", [][]string{nil}},
			} {
				t.Run(test.name, func(t *testing.T) {
					sentinel := fmt.Sprintf("%s_RECTANGLE_SECRET_%03d", strings.ToUpper(dialect), index+1)
					rows := test.rows
					if len(rows) > 0 && len(rows[0]) > 0 {
						rows[0][0] = sentinel
					}
					delegate := &scriptedLineageLeakExecutor{dialect: dialect, result: model.QueryResult{
						Columns: []string{"phone", "email"}, Rows: rows, RowCount: 1,
					}}
					datasource := lineageLeakDatasource(dialect)
					flow, ports := newDatabaseE2EPipelineWithRules(
						t, datasource, &countingDatabaseExecutor{delegate: &fakeExecutorProvider{executor: delegate}}, "dml",
						lineageLeakAllowedTables(dialect), lineageLeakRules(), nil,
					)
					response, err := flow.Process(context.Background(), databaseE2ERequest(datasource.ID, query))
					require.Error(t, err, "%s/%s sentinel=%s", dialect, test.name, sentinel)
					require.Nil(t, response.Result, "%s/%s sentinel=%s", dialect, test.name, sentinel)
					assertLineageSentinelAbsent(t, test.name, sentinel, response, ports.audit)
				})
			}
		})
	}
}

func assertLineageSentinelAbsent(t *testing.T, name, sentinel string, response Response, audit *fakeAuditRecorder) {
	t.Helper()
	responseJSON, err := json.Marshal(response)
	require.NoError(t, err)
	require.NotContains(t, string(responseJSON), sentinel, "%s sentinel=%s response", name, sentinel)
	require.Positive(t, audit.calls(), "%s sentinel=%s audit was not recorded", name, sentinel)
	assertLineageRawAbsentFromAudit(t, name, sentinel, audit.last())
}

func assertLineageRawAbsentFromAudit(t *testing.T, name, sentinel string, audit model.AuditLog) {
	t.Helper()
	auditJSON, err := json.Marshal(audit)
	require.NoError(t, err)
	require.NotContains(t, string(auditJSON), sentinel, "%s sentinel=%s audit", name, sentinel)
}

func maskPhoneSentinel(raw string) string {
	if len(raw) != 11 {
		return mask.RedactedFallback
	}
	return raw[:3] + "****" + raw[7:]
}

func lineageLeakRules() []mask.Rule {
	return []mask.Rule{
		{Table: "customers", Column: "phone", SensitiveType: mask.TypePhone, Algorithm: mask.AlgoMask},
		{Table: "customers", Column: "email", SensitiveType: mask.TypeEmail, Algorithm: mask.AlgoMask},
		{Table: "customers", Column: "name", SensitiveType: mask.TypeGeneric, Algorithm: mask.AlgoBlock},
		{Table: "customers", Column: "payload", SensitiveType: mask.TypeGeneric, Algorithm: mask.AlgoBlock},
		{Table: "orders", Column: "phone", SensitiveType: mask.TypePhone, Algorithm: mask.AlgoMask},
		{Table: "archive", Column: "phone", SensitiveType: mask.TypePhone, Algorithm: mask.AlgoMask},
		{Table: "customer_view", Column: "phone", SensitiveType: mask.TypePhone, Algorithm: mask.AlgoMask},
		{Table: "unknown_relation", Column: "phone", SensitiveType: mask.TypePhone, Algorithm: mask.AlgoMask},
	}
}

func lineageLeakDatasource(dialect string) model.Datasource {
	return model.Datasource{
		ID: "lineage-leak-" + dialect, DBType: dialect, Database: "test",
		StmtTimeoutMS: 5_000, RowLimit: 100,
	}
}

func lineageLeakAllowedTables(dialect string) []string {
	qualifier := "test"
	if dialect == "postgres" {
		qualifier = "public"
	}
	tables := []string{"customers", "orders", "archive", "customer_view", "unknown_relation"}
	allowed := make([]string, len(tables))
	for index, table := range tables {
		allowed[index] = qualifier + "." + table
	}
	return allowed
}

type scriptedLineageLeakExecutor struct {
	dialect    string
	result     model.QueryResult
	queryCalls int
}

func (scripted *scriptedLineageLeakExecutor) Dialect() string            { return scripted.dialect }
func (scripted *scriptedLineageLeakExecutor) Ping(context.Context) error { return nil }
func (scripted *scriptedLineageLeakExecutor) OpenSession(context.Context, string) (rawTestSession, error) {
	return nil, errors.New("sessions are not used by projection lineage leak tests")
}
func (scripted *scriptedLineageLeakExecutor) BeginWriteTx(context.Context) (rawTestWriteTx, error) {
	return nil, errors.New("write transactions are not used by projection lineage leak tests")
}
func (scripted *scriptedLineageLeakExecutor) Explain(context.Context, string) (model.ExplainInfo, error) {
	return model.ExplainInfo{EstScanRows: 1, UsesIndex: true}, nil
}
func (scripted *scriptedLineageLeakExecutor) Query(context.Context, string, int) (model.QueryResult, error) {
	scripted.queryCalls++
	return scripted.result, nil
}
func (scripted *scriptedLineageLeakExecutor) Execute(context.Context, string) (model.QueryResult, error) {
	return model.QueryResult{}, errors.New("execute is not used by projection lineage leak tests")
}
func (scripted *scriptedLineageLeakExecutor) Close() error { return nil }
func (scripted *scriptedLineageLeakExecutor) TableHasIndex(string, string) (bool, error) {
	return true, nil
}
func (scripted *scriptedLineageLeakExecutor) TableRowCount(string, string) (int64, error) {
	return 1, nil
}
func (scripted *scriptedLineageLeakExecutor) TransactionState() (rules.TransactionState, error) {
	return rules.TransactionState{}, nil
}
func (scripted *scriptedLineageLeakExecutor) MysqlTransactionState() (rules.MysqlTransactionState, error) {
	return rules.MysqlTransactionState{}, nil
}
