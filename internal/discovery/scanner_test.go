package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/mask"
)

type fakeSchemaLister struct {
	columns []ColumnMeta
	err     error
	calls   int
}

func (fake *fakeSchemaLister) ListColumns(
	_ context.Context,
	_ string,
	_ []TableRef,
) ([]ColumnMeta, error) {
	fake.calls++
	return append([]ColumnMeta(nil), fake.columns...), fake.err
}

type queryCall struct {
	table   TableRef
	columns []ColumnRef
	limit   int
}

type fakeLimitedQuerier struct {
	values       map[ColumnRef][]*string
	err          error
	calls        []queryCall
	fullRows     bool
	defaultValue string
	extraRows    int
	wrongColumns bool
}

func (fake *fakeLimitedQuerier) QueryColumns(
	_ context.Context,
	_ string,
	table TableRef,
	columns []ColumnRef,
	limit int,
) (SampleBatch, error) {
	fake.calls = append(fake.calls, queryCall{
		table:   table,
		columns: append([]ColumnRef(nil), columns...),
		limit:   limit,
	})
	if fake.err != nil {
		return SampleBatch{}, fake.err
	}
	rowsCount := 0
	if fake.fullRows {
		rowsCount = limit
	}
	for _, column := range columns {
		if len(fake.values[column]) > rowsCount {
			rowsCount = len(fake.values[column])
		}
	}
	rowsCount += fake.extraRows
	rows := make([][]*string, rowsCount)
	for rowIndex := range rows {
		rows[rowIndex] = make([]*string, len(columns))
		for columnIndex, column := range columns {
			values := fake.values[column]
			if rowIndex < len(values) {
				rows[rowIndex][columnIndex] = values[rowIndex]
				continue
			}
			if fake.fullRows {
				value := fake.defaultValue
				rows[rowIndex][columnIndex] = &value
			}
		}
	}
	returnedColumns := columns
	if fake.wrongColumns {
		returnedColumns = nil
	}
	return NewSampleBatch(returnedColumns, rows), nil
}

func TestScannerConfidenceStateMachine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		column     string
		values     []string
		sampling   bool
		wantCount  int
		confidence Confidence
		queryCalls int
	}{
		{name: "sampled strong support high", column: "phone", values: []string{"13800138000", "13900139000", "13700137000", "13600136000", "product"}, sampling: true, wantCount: 1, confidence: ConfidenceHigh, queryCalls: 1},
		{name: "sampled strong insufficient medium", column: "phone", values: []string{"13800138000", "product"}, sampling: true, wantCount: 1, confidence: ConfidenceMedium, queryCalls: 1},
		{name: "sampled strong weak medium", column: "phone", values: []string{"13800138000", "13900139000", "product"}, sampling: true, wantCount: 1, confidence: ConfidenceMedium, queryCalls: 1},
		{name: "sampled strong contradict omitted", column: "phone", values: []string{"13800138000", "sku-1", "sku-2"}, sampling: true, queryCalls: 1},
		{name: "sampled medium support medium", column: "tel", values: []string{"13800138000", "13900139000", "13700137000", "13600136000", "product"}, sampling: true, wantCount: 1, confidence: ConfidenceMedium, queryCalls: 1},
		{name: "sampled medium non-support omitted", column: "tel", values: []string{"13800138000", "13900139000", "product"}, sampling: true, queryCalls: 1},
		{name: "unsampled strong medium", column: "phone", sampling: false, wantCount: 1, confidence: ConfidenceMedium},
		{name: "unsampled medium low", column: "tel", sampling: false, wantCount: 1, confidence: ConfidenceLow},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			table := TableRef{Schema: "public", Table: "customers"}
			column := ColumnRef{Schema: table.Schema, Table: table.Table, Column: test.column}
			lister := &fakeSchemaLister{columns: []ColumnMeta{{
				Schema: table.Schema, Table: table.Table, Column: test.column, DataType: "varchar", Ordinal: 1,
			}}}
			querier := &fakeLimitedQuerier{values: map[ColumnRef][]*string{
				column: stringPointers(test.values...),
			}}
			scanner := NewScanner(lister, querier)
			scanner.now = fixedNow
			result, err := scanner.Scan(context.Background(), "ds-1", ScanRequest{
				Tables:   []TableRef{table},
				Sampling: boolPointer(test.sampling),
			})
			if err != nil {
				t.Fatalf("Scan() error = %v", err)
			}
			if len(result.Findings) != test.wantCount {
				t.Fatalf("findings = %#v, want count %d", result.Findings, test.wantCount)
			}
			if len(querier.calls) != test.queryCalls {
				t.Fatalf("query calls = %d, want %d", len(querier.calls), test.queryCalls)
			}
			if test.wantCount == 1 && result.Findings[0].Confidence != test.confidence {
				t.Fatalf("confidence = %q, want %q", result.Findings[0].Confidence, test.confidence)
			}
			if !test.sampling && result.Stats.SampledValuesCount != 0 {
				t.Fatalf("sampling=false sampled values = %d", result.Stats.SampledValuesCount)
			}
		})
	}
}

func TestScannerNegativeColumnsAreNotSampledOrReported(t *testing.T) {
	t.Parallel()
	table := TableRef{Schema: "public", Table: "products"}
	columns := []string{"processing_time_ms", "response_time", "total_pages", "product_code", "uuid"}
	metadata := make([]ColumnMeta, len(columns))
	for index, column := range columns {
		metadata[index] = ColumnMeta{Schema: table.Schema, Table: table.Table, Column: column, Ordinal: index + 1}
	}
	querier := &fakeLimitedQuerier{}
	result, err := NewScanner(&fakeSchemaLister{columns: metadata}, querier).Scan(
		context.Background(), "ds-1", ScanRequest{Tables: []TableRef{table}},
	)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if result.Stats.CandidateColumns != 0 || len(result.Findings) != 0 || len(querier.calls) != 0 {
		t.Fatalf("negative scan stats/findings/calls = %#v %#v %d", result.Stats, result.Findings, len(querier.calls))
	}
}

func TestScannerEnhancedGateProducesNewFindings(t *testing.T) {
	t.Parallel()
	table := TableRef{Schema: "public", Table: "enhanced_columns"}
	columns := []string{"price", "stock", "amount", "created_at", "ordered_at", "full_name", "name"}
	metadata := make([]ColumnMeta, len(columns))
	for index, column := range columns {
		metadata[index] = ColumnMeta{
			Schema: table.Schema, Table: table.Table, Column: column, DataType: "text", Ordinal: index + 1,
		}
	}
	values := make(map[ColumnRef][]*string)
	for _, column := range []string{"price", "stock", "amount"} {
		values[ColumnRef{Schema: table.Schema, Table: table.Table, Column: column}] = stringPointers("42", "43", "44")
	}
	for _, column := range []string{"created_at", "ordered_at"} {
		values[ColumnRef{Schema: table.Schema, Table: table.Table, Column: column}] = stringPointers("2026-09-01", "2026-09-02", "2026-09-03")
	}
	querier := &fakeLimitedQuerier{values: values}
	result, err := NewScanner(&fakeSchemaLister{columns: metadata}, querier).Scan(
		context.Background(), "ds-1", ScanRequest{Tables: []TableRef{table}},
	)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if result.Stats.CandidateColumns != 7 || result.Stats.SampledColumns != 5 ||
		result.Stats.FindingsCount != 7 || len(result.Findings) != 7 || len(querier.calls) != 1 {
		t.Fatalf("enhanced scan stats/findings/calls = %#v %#v %d", result.Stats, result.Findings, len(querier.calls))
	}
	want := map[string]struct {
		category Category
		algo     mask.Algorithm
	}{
		"price":      {CategoryNumber, mask.AlgoBlock},
		"stock":      {CategoryNumber, mask.AlgoBlock},
		"amount":     {CategoryNumber, mask.AlgoBlock},
		"created_at": {CategoryDate, mask.AlgoRange},
		"ordered_at": {CategoryDate, mask.AlgoRange},
		"full_name":  {CategoryGeneric, mask.AlgoBlock},
		"name":       {CategoryGeneric, ""},
	}
	for _, finding := range result.Findings {
		expected := want[finding.Column]
		if finding.Category != expected.category {
			t.Fatalf("%s category = %q, want %q", finding.Column, finding.Category, expected.category)
		}
		if expected.algo == "" {
			if finding.Applicable || finding.RecommendedRule != nil || finding.Reason != GenericReviewReason {
				t.Fatalf("broad generic finding = %#v", finding)
			}
		} else if !finding.Applicable || finding.RecommendedRule == nil || finding.RecommendedRule.Algo != expected.algo {
			t.Fatalf("applicable finding = %#v, want algo %q", finding, expected.algo)
		}
	}
}

func TestEnhancedGenericFindingsIgnoreSamplesAndBroadIsDiscoveryOnly(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		strength   signalStrength
		confidence Confidence
		applicable bool
	}{
		{name: "strong", strength: strengthStrong, confidence: ConfidenceMedium, applicable: true},
		{name: "broad", strength: strengthMedium, confidence: ConfidenceLow},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			candidate := columnCandidate{
				meta:     ColumnMeta{Schema: "public", Table: "customers", Column: test.name, DataType: "text"},
				category: CategoryGeneric,
				strength: test.strength,
				signal:   Signal{Name: "column_name_" + test.name + "_generic", Count: 1},
			}
			finding, err := findingForMode(candidate, test.confidence, false, sampleEvidence{}, true)
			if err != nil {
				t.Fatal(err)
			}
			if finding.Sampled || finding.EligibleSamples != 0 || finding.MatchedSamples != 0 || len(finding.Signals) != 1 {
				t.Fatalf("generic finding contains sample evidence: %#v", finding)
			}
			if finding.Confidence != test.confidence || finding.Applicable != test.applicable {
				t.Fatalf("generic finding confidence/applicable = %q/%v", finding.Confidence, finding.Applicable)
			}
			if test.applicable {
				if finding.RecommendedRule == nil || finding.RecommendedRule.Algo != mask.AlgoBlock || finding.Reason != "" {
					t.Fatalf("strong generic advice = %#v/%q", finding.RecommendedRule, finding.Reason)
				}
			} else if finding.RecommendedRule != nil || finding.Reason != GenericReviewReason {
				t.Fatalf("broad generic advice = %#v/%q", finding.RecommendedRule, finding.Reason)
			}
		})
	}
}

func TestEnhancedFindingDoesNotLeakRawSamples(t *testing.T) {
	t.Parallel()
	const sentinel = "RAW-SAMPLE-MUST-NOT-LEAK-9917"
	values := stringPointers(sentinel)
	evidence := evaluateSamples(CategoryNumber, values, fixedNow())
	candidate, matched := classifyColumnNameGlobal("risk_score", true)
	if !matched {
		t.Fatal("risk_score did not classify")
	}
	candidate.meta = ColumnMeta{Schema: "public", Table: "risk", Column: "risk_score", DataType: "numeric"}
	confidence, include := confidenceFor(candidate.category, candidate.strength, evidence)
	if !include {
		t.Fatal("strong number candidate was unexpectedly omitted")
	}
	finding, err := findingForMode(candidate, confidence, true, evidence, true)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(finding)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), sentinel) {
		t.Fatalf("sample leaked through finding: %s", encoded)
	}
}

func TestScannerContradictoryProductCodesInPhoneColumnAreOmitted(t *testing.T) {
	t.Parallel()
	table := TableRef{Schema: "public", Table: "products"}
	column := ColumnRef{Schema: table.Schema, Table: table.Table, Column: "phone"}
	products := make([]string, 10)
	for index := range products {
		products[index] = fmt.Sprintf("SKU-%04d", index)
	}
	result, err := NewScanner(
		&fakeSchemaLister{columns: []ColumnMeta{{Schema: table.Schema, Table: table.Table, Column: column.Column, Ordinal: 1}}},
		&fakeLimitedQuerier{values: map[ColumnRef][]*string{column: stringPointers(products...)}},
	).Scan(context.Background(), "ds-1", ScanRequest{Tables: []TableRef{table}})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(result.Findings) != 0 {
		t.Fatalf("contradictory findings = %#v", result.Findings)
	}
}

func TestAddingInvalidSamplesCannotIncreaseConfidence(t *testing.T) {
	t.Parallel()
	base := []string{"13800138000", "13900139000", "13700137000", "13600136000"}
	withOneInvalid := append(append([]string(nil), base...), "sku")
	withTwoInvalid := append(append([]string(nil), withOneInvalid...), "sku-2")
	if got := scanPhoneConfidence(t, base); got != ConfidenceHigh {
		t.Fatalf("base confidence = %q", got)
	}
	if got := scanPhoneConfidence(t, withOneInvalid); got != ConfidenceHigh {
		t.Fatalf("one invalid confidence = %q", got)
	}
	if got := scanPhoneConfidence(t, withTwoInvalid); got != ConfidenceMedium {
		t.Fatalf("two invalid confidence = %q, want medium", got)
	}
}

func TestScannerSamplesOnlyFilteredCandidatesInBatches(t *testing.T) {
	t.Parallel()
	table := TableRef{Schema: "public", Table: "customers"}
	metadata := []ColumnMeta{
		{Schema: table.Schema, Table: table.Table, Column: "phone", Ordinal: 1},
		{Schema: table.Schema, Table: table.Table, Column: "email_address", Ordinal: 2},
		{Schema: table.Schema, Table: table.Table, Column: "ip_address", Ordinal: 3},
		{Schema: table.Schema, Table: table.Table, Column: "description", Ordinal: 4},
	}
	querier := &fakeLimitedQuerier{}
	result, err := NewScanner(&fakeSchemaLister{columns: metadata}, querier).Scan(
		context.Background(), "ds-1", ScanRequest{
			Tables:     []TableRef{table},
			Categories: []Category{CategoryPhone, CategoryEmail},
		},
	)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if result.Stats.CandidateColumns != 2 || len(querier.calls) != 1 {
		t.Fatalf("candidate count/calls = %d/%d", result.Stats.CandidateColumns, len(querier.calls))
	}
	want := []ColumnRef{
		{Schema: table.Schema, Table: table.Table, Column: "phone"},
		{Schema: table.Schema, Table: table.Table, Column: "email_address"},
	}
	if !sameColumns(want, querier.calls[0].columns) {
		t.Fatalf("sample projection = %#v, want %#v", querier.calls[0].columns, want)
	}
	if querier.calls[0].limit != DefaultSampleRows {
		t.Fatalf("sample limit = %d, want %d", querier.calls[0].limit, DefaultSampleRows)
	}
}

func TestScannerBatchesAtTenCandidateColumns(t *testing.T) {
	t.Parallel()
	table := TableRef{Schema: "public", Table: "customers"}
	metadata := make([]ColumnMeta, 12)
	for index := range metadata {
		metadata[index] = ColumnMeta{
			Schema: table.Schema, Table: table.Table, Column: fmt.Sprintf("phone_%02d", index), Ordinal: index + 1,
		}
	}
	querier := &fakeLimitedQuerier{}
	_, err := NewScanner(&fakeSchemaLister{columns: metadata}, querier).Scan(
		context.Background(), "ds-1", ScanRequest{Tables: []TableRef{table}},
	)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(querier.calls) != 2 || len(querier.calls[0].columns) != 10 || len(querier.calls[1].columns) != 2 {
		t.Fatalf("batch calls = %#v", querier.calls)
	}
}

func TestScannerHardLimitsAndVisibilityFailClosed(t *testing.T) {
	t.Parallel()
	baseTable := TableRef{Schema: "public", Table: "t"}
	tests := []struct {
		name    string
		request ScanRequest
		columns []ColumnMeta
		wantErr error
	}{
		{name: "empty tables", request: ScanRequest{}, wantErr: ErrScopeLimitExceeded},
		{name: "too many tables", request: ScanRequest{Tables: makeTables(21)}, wantErr: ErrScopeLimitExceeded},
		{name: "sample rows negative", request: ScanRequest{Tables: []TableRef{baseTable}, SampleRows: -1}, wantErr: ErrScopeLimitExceeded},
		{name: "sample rows over max", request: ScanRequest{Tables: []TableRef{baseTable}, SampleRows: 21}, wantErr: ErrScopeLimitExceeded},
		{name: "metadata over max", request: ScanRequest{Tables: []TableRef{baseTable}}, columns: makeColumns(baseTable, 501, "description"), wantErr: ErrScopeLimitExceeded},
		{name: "candidate over max", request: ScanRequest{Tables: []TableRef{baseTable}}, columns: makeColumns(baseTable, 51, "phone"), wantErr: ErrCandidateLimitExceeded},
		{name: "missing table", request: ScanRequest{Tables: []TableRef{baseTable}}, wantErr: ErrScopeNotVisible},
		{name: "extra table", request: ScanRequest{Tables: []TableRef{baseTable}}, columns: []ColumnMeta{{Schema: "public", Table: "other", Column: "phone", Ordinal: 1}}, wantErr: ErrScopeNotVisible},
		{name: "unknown category", request: ScanRequest{Tables: []TableRef{baseTable}, Categories: []Category{"future"}}, wantErr: ErrUnknownCategory},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewScanner(&fakeSchemaLister{columns: test.columns}, &fakeLimitedQuerier{}).Scan(
				context.Background(), "ds-1", test.request,
			)
			if !isError(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestScannerBoundaryLimitsSucceedAtTwentyFiveHundredAndOneThousand(t *testing.T) {
	t.Parallel()
	tables := makeTables(MaxTables)
	metadata := make([]ColumnMeta, 0, MaxMetadataColumns)
	for tableIndex, table := range tables {
		for columnIndex := 0; columnIndex < MaxMetadataColumns/MaxTables; columnIndex++ {
			prefix := "product_code"
			if tableIndex < 2 {
				prefix = "phone"
			}
			metadata = append(metadata, ColumnMeta{
				Schema:  table.Schema,
				Table:   table.Table,
				Column:  fmt.Sprintf("%s_%03d", prefix, columnIndex),
				Ordinal: columnIndex + 1,
			})
		}
	}
	querier := &fakeLimitedQuerier{fullRows: true, defaultValue: "13800138000"}
	result, err := NewScanner(&fakeSchemaLister{columns: metadata}, querier).Scan(
		context.Background(), "ds-1", ScanRequest{Tables: tables, SampleRows: MaxSampleRows},
	)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if result.Stats.TablesScanned != MaxTables || result.Stats.ColumnsSeen != MaxMetadataColumns ||
		result.Stats.CandidateColumns != MaxCandidateColumns || result.Stats.SampledValuesCount != MaxSampleValues {
		t.Fatalf("boundary stats = %#v", result.Stats)
	}
	for _, call := range querier.calls {
		if len(call.columns) > MaxColumnsPerSampleQuery || call.limit != MaxSampleRows {
			t.Fatalf("unsafe query call = %#v", call)
		}
	}
}

func TestScannerRejectsMalformedOrOversizedSampleBatch(t *testing.T) {
	t.Parallel()
	table := TableRef{Schema: "public", Table: "customers"}
	metadata := []ColumnMeta{{Schema: table.Schema, Table: table.Table, Column: "phone", Ordinal: 1}}
	for _, querier := range []*fakeLimitedQuerier{
		{wrongColumns: true},
		{extraRows: DefaultSampleRows + 1},
	} {
		_, err := NewScanner(&fakeSchemaLister{columns: metadata}, querier).Scan(
			context.Background(), "ds-1", ScanRequest{Tables: []TableRef{table}},
		)
		if !isError(err, ErrInternal) {
			t.Fatalf("error = %v, want ErrInternal", err)
		}
	}
}

func TestScannerDoesNotLeakSamplesOrAdapterErrors(t *testing.T) {
	t.Parallel()
	const sentinel = "UNIQUE-RAW-SAMPLE-7a6d"
	table := TableRef{Schema: "public", Table: "customers"}
	column := ColumnRef{Schema: table.Schema, Table: table.Table, Column: "email"}
	scanner := NewScanner(
		&fakeSchemaLister{columns: []ColumnMeta{{Schema: table.Schema, Table: table.Table, Column: column.Column, Ordinal: 1}}},
		&fakeLimitedQuerier{values: map[ColumnRef][]*string{column: stringPointers(
			sentinel+"@example.com", sentinel+"2@example.com", sentinel+"3@example.com",
		)}},
	)
	result, err := scanner.Scan(context.Background(), "ds-1", ScanRequest{Tables: []TableRef{table}})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if strings.Contains(string(encoded), sentinel) {
		t.Fatalf("finding leaked raw sample: %s", encoded)
	}

	_, err = NewScanner(
		&fakeSchemaLister{columns: []ColumnMeta{{Schema: table.Schema, Table: table.Table, Column: "phone", Ordinal: 1}}},
		&fakeLimitedQuerier{err: errors.New("adapter failed near " + sentinel)},
	).Scan(context.Background(), "ds-1", ScanRequest{Tables: []TableRef{table}})
	if !isError(err, ErrInternal) || strings.Contains(err.Error(), sentinel) {
		t.Fatalf("unsafe classified error = %v", err)
	}
}

func scanPhoneConfidence(t *testing.T, values []string) Confidence {
	t.Helper()
	table := TableRef{Schema: "public", Table: "customers"}
	column := ColumnRef{Schema: table.Schema, Table: table.Table, Column: "phone"}
	scanner := NewScanner(
		&fakeSchemaLister{columns: []ColumnMeta{{Schema: table.Schema, Table: table.Table, Column: column.Column, Ordinal: 1}}},
		&fakeLimitedQuerier{values: map[ColumnRef][]*string{column: stringPointers(values...)}},
	)
	scanner.now = fixedNow
	result, err := scanner.Scan(context.Background(), "ds-1", ScanRequest{Tables: []TableRef{table}})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(result.Findings) != 1 {
		t.Fatalf("findings = %#v", result.Findings)
	}
	return result.Findings[0].Confidence
}

func makeTables(count int) []TableRef {
	tables := make([]TableRef, count)
	for index := range tables {
		tables[index] = TableRef{Schema: "public", Table: fmt.Sprintf("table_%02d", index)}
	}
	return tables
}

func makeColumns(table TableRef, count int, prefix string) []ColumnMeta {
	columns := make([]ColumnMeta, count)
	for index := range columns {
		columns[index] = ColumnMeta{
			Schema: table.Schema, Table: table.Table, Column: fmt.Sprintf("%s_%03d", prefix, index), Ordinal: index + 1,
		}
	}
	return columns
}

func stringPointers(values ...string) []*string {
	pointers := make([]*string, len(values))
	for index := range values {
		value := values[index]
		pointers[index] = &value
	}
	return pointers
}

func boolPointer(value bool) *bool {
	return &value
}

func fixedNow() time.Time {
	return time.Date(2026, time.September, 19, 0, 0, 0, 0, time.UTC)
}

func isError(err, target error) bool {
	return errors.Is(err, target)
}
