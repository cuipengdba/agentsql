package discovery

import (
	"context"
	"sort"
	"strings"
	"time"
)

// Scanner coordinates metadata classification and bounded candidate-only
// sampling. It has no database, HTTP, MCP, or raw-SQL dependency.
type Scanner struct {
	lister  SchemaLister
	querier LimitedQuerier
	now     func() time.Time
}

func NewScanner(lister SchemaLister, querier LimitedQuerier) *Scanner {
	return &Scanner{
		lister:  lister,
		querier: querier,
		now:     time.Now,
	}
}

// Scan fails closed on invalid scope, invisible tables, malformed adapter
// output, or any hard limit. Adapter error strings are never propagated.
func (scanner *Scanner) Scan(
	ctx context.Context,
	datasourceID string,
	request ScanRequest,
) (ScanResult, error) {
	result := ScanResult{Limits: fixedLimits(), Findings: []Finding{}}
	scope, allowed, err := normalizeRequest(datasourceID, request)
	if err != nil {
		return ScanResult{}, err
	}
	result.Scope = scope
	result.Stats.TablesRequested = len(scope.Tables)
	if scanner == nil || scanner.lister == nil {
		return ScanResult{}, classified(CodeInternal, ErrInternal)
	}

	metadata, listErr := scanner.lister.ListColumns(ctx, datasourceID, append([]TableRef(nil), scope.Tables...))
	if listErr != nil {
		return ScanResult{}, classified(CodeInternal, ErrInternal)
	}
	if len(metadata) > MaxMetadataColumns {
		return ScanResult{}, classified(CodeScopeLimit, ErrScopeLimitExceeded)
	}
	ordered, validationErr := validateAndOrderMetadata(scope.Tables, metadata)
	if validationErr != nil {
		return ScanResult{}, validationErr
	}
	result.Stats.TablesScanned = len(scope.Tables)
	result.Stats.ColumnsSeen = len(ordered)

	candidates := make([]columnCandidate, 0, len(ordered))
	for _, meta := range ordered {
		candidate, matched := classifyColumn(meta, allowed)
		if !matched {
			continue
		}
		candidates = append(candidates, candidate)
		if len(candidates) > MaxCandidateColumns {
			return ScanResult{}, classified(CodeCandidateLimit, ErrCandidateLimitExceeded)
		}
	}
	result.Stats.CandidateColumns = len(candidates)

	if !scope.Sampling {
		for _, candidate := range candidates {
			confidence := ConfidenceLow
			if candidate.strength == strengthStrong {
				confidence = ConfidenceMedium
			}
			finding, findingErr := findingFor(candidate, confidence, false, sampleEvidence{})
			if findingErr != nil {
				return ScanResult{}, findingErr
			}
			result.Findings = append(result.Findings, finding)
		}
		result.Stats.FindingsCount = len(result.Findings)
		return result, nil
	}

	if len(candidates) == 0 {
		return result, nil
	}
	if scanner.querier == nil {
		return ScanResult{}, classified(CodeInternal, ErrInternal)
	}
	plannedValues := len(candidates) * scope.SampleRows
	if plannedValues > MaxSampleValues {
		return ScanResult{}, classified(CodeSampleLimit, ErrSampleLimitExceeded)
	}

	evidence := make([]sampleEvidence, len(candidates))
	if err := scanner.sampleCandidates(ctx, datasourceID, scope.SampleRows, candidates, evidence, &result.Stats); err != nil {
		return ScanResult{}, err
	}
	result.Stats.SampledColumns = len(candidates)

	for index, candidate := range candidates {
		confidence, include := confidenceFor(candidate.strength, evidence[index])
		if !include {
			continue
		}
		finding, findingErr := findingFor(candidate, confidence, true, evidence[index])
		if findingErr != nil {
			return ScanResult{}, findingErr
		}
		result.Findings = append(result.Findings, finding)
	}
	result.Stats.FindingsCount = len(result.Findings)
	return result, nil
}

func normalizeRequest(
	datasourceID string,
	request ScanRequest,
) (Scope, map[Category]bool, error) {
	if strings.TrimSpace(datasourceID) == "" || datasourceID != strings.TrimSpace(datasourceID) {
		return Scope{}, nil, classified(CodeInvalidRequest, ErrInvalidRequest)
	}
	if len(request.Tables) < MinTables || len(request.Tables) > MaxTables {
		return Scope{}, nil, classified(CodeScopeLimit, ErrScopeLimitExceeded)
	}
	tables := append([]TableRef(nil), request.Tables...)
	seenTables := make(map[TableRef]struct{}, len(tables))
	for _, table := range tables {
		if strings.TrimSpace(table.Table) == "" || table.Table != strings.TrimSpace(table.Table) ||
			table.Schema != strings.TrimSpace(table.Schema) {
			return Scope{}, nil, classified(CodeInvalidRequest, ErrInvalidRequest)
		}
		if _, duplicate := seenTables[table]; duplicate {
			return Scope{}, nil, classified(CodeInvalidRequest, ErrInvalidRequest)
		}
		seenTables[table] = struct{}{}
	}
	allowed, categories, err := normalizeCategories(request.Categories)
	if err != nil {
		return Scope{}, nil, err
	}
	sampling := true
	if request.Sampling != nil {
		sampling = *request.Sampling
	}
	sampleRows := request.SampleRows
	if sampling {
		if sampleRows == 0 {
			sampleRows = DefaultSampleRows
		}
		if sampleRows < 1 || sampleRows > MaxSampleRows {
			return Scope{}, nil, classified(CodeScopeLimit, ErrScopeLimitExceeded)
		}
	} else {
		sampleRows = 0
	}
	return Scope{
		DatasourceID: datasourceID,
		Tables:       tables,
		Sampling:     sampling,
		SampleRows:   sampleRows,
		Categories:   append([]Category(nil), categories...),
	}, allowed, nil
}

func validateAndOrderMetadata(tables []TableRef, metadata []ColumnMeta) ([]ColumnMeta, error) {
	tableOrder := make(map[TableRef]int, len(tables))
	seenTable := make(map[TableRef]bool, len(tables))
	for index, table := range tables {
		tableOrder[table] = index
	}
	seenColumns := make(map[ColumnRef]struct{}, len(metadata))
	ordered := append([]ColumnMeta(nil), metadata...)
	for _, meta := range ordered {
		table := TableRef{Schema: meta.Schema, Table: meta.Table}
		if _, requested := tableOrder[table]; !requested {
			return nil, classified(CodeScopeNotVisible, ErrScopeNotVisible)
		}
		if strings.TrimSpace(meta.Column) == "" || meta.Column != strings.TrimSpace(meta.Column) ||
			meta.Ordinal < 1 {
			return nil, classified(CodeInternal, ErrInternal)
		}
		column := ColumnRef{Schema: meta.Schema, Table: meta.Table, Column: meta.Column}
		if _, duplicate := seenColumns[column]; duplicate {
			return nil, classified(CodeInternal, ErrInternal)
		}
		seenColumns[column] = struct{}{}
		seenTable[table] = true
	}
	for _, table := range tables {
		if !seenTable[table] {
			return nil, classified(CodeScopeNotVisible, ErrScopeNotVisible)
		}
	}
	sort.SliceStable(ordered, func(left, right int) bool {
		leftTable := TableRef{Schema: ordered[left].Schema, Table: ordered[left].Table}
		rightTable := TableRef{Schema: ordered[right].Schema, Table: ordered[right].Table}
		if tableOrder[leftTable] != tableOrder[rightTable] {
			return tableOrder[leftTable] < tableOrder[rightTable]
		}
		if ordered[left].Ordinal != ordered[right].Ordinal {
			return ordered[left].Ordinal < ordered[right].Ordinal
		}
		return ordered[left].Column < ordered[right].Column
	})
	return ordered, nil
}

func (scanner *Scanner) sampleCandidates(
	ctx context.Context,
	datasourceID string,
	limit int,
	candidates []columnCandidate,
	evidence []sampleEvidence,
	stats *Stats,
) error {
	referenceDate := time.Now()
	if scanner.now != nil {
		referenceDate = scanner.now()
	}
	for start := 0; start < len(candidates); {
		table := TableRef{Schema: candidates[start].meta.Schema, Table: candidates[start].meta.Table}
		tableEnd := start + 1
		for tableEnd < len(candidates) && candidates[tableEnd].meta.Schema == table.Schema &&
			candidates[tableEnd].meta.Table == table.Table {
			tableEnd++
		}
		for batchStart := start; batchStart < tableEnd; batchStart += MaxColumnsPerSampleQuery {
			batchEnd := batchStart + MaxColumnsPerSampleQuery
			if batchEnd > tableEnd {
				batchEnd = tableEnd
			}
			columns := make([]ColumnRef, batchEnd-batchStart)
			for index := batchStart; index < batchEnd; index++ {
				columns[index-batchStart] = ColumnRef{
					Schema: candidates[index].meta.Schema,
					Table:  candidates[index].meta.Table,
					Column: candidates[index].meta.Column,
				}
			}
			batch, err := scanner.querier.QueryColumns(ctx, datasourceID, table, append([]ColumnRef(nil), columns...), limit)
			if err != nil {
				return classified(CodeInternal, ErrInternal)
			}
			if !sameColumns(columns, batch.columns) || len(batch.rows) > limit {
				return classified(CodeInternal, ErrInternal)
			}
			values := make([][]*string, len(columns))
			for _, row := range batch.rows {
				if len(row) != len(columns) {
					return classified(CodeInternal, ErrInternal)
				}
				stats.SampledValuesCount += len(row)
				if stats.SampledValuesCount > MaxSampleValues {
					return classified(CodeSampleLimit, ErrSampleLimitExceeded)
				}
				for columnIndex, value := range row {
					values[columnIndex] = append(values[columnIndex], value)
				}
			}
			for columnIndex := range columns {
				evidence[batchStart+columnIndex] = evaluateSamples(
					candidates[batchStart+columnIndex].category,
					values[columnIndex],
					referenceDate,
				)
			}
		}
		start = tableEnd
	}
	return nil
}

func sameColumns(expected, actual []ColumnRef) bool {
	if len(expected) != len(actual) {
		return false
	}
	for index := range expected {
		if expected[index] != actual[index] {
			return false
		}
	}
	return true
}

func confidenceFor(strength signalStrength, evidence sampleEvidence) (Confidence, bool) {
	if strength == strengthStrong {
		if evidence.supports() {
			return ConfidenceHigh, true
		}
		if evidence.contradicts() {
			return "", false
		}
		return ConfidenceMedium, true
	}
	if evidence.supports() {
		return ConfidenceMedium, true
	}
	return "", false
}

func findingFor(
	candidate columnCandidate,
	confidence Confidence,
	sampled bool,
	evidence sampleEvidence,
) (Finding, error) {
	rule, applicable, reason, err := Advise(candidate.category)
	if err != nil {
		return Finding{}, err
	}
	signals := []Signal{candidate.signal}
	if sampled {
		signals = append(signals,
			Signal{Name: "sample_eligible", Count: evidence.eligible},
			Signal{Name: "sample_match", Count: evidence.matched},
		)
	}
	return Finding{
		Schema:          candidate.meta.Schema,
		Table:           candidate.meta.Table,
		Column:          candidate.meta.Column,
		DataType:        candidate.meta.DataType,
		Category:        candidate.category,
		Signals:         signals,
		Confidence:      confidence,
		Sampled:         sampled,
		MatchedSamples:  evidence.matched,
		EligibleSamples: evidence.eligible,
		RecommendedRule: rule,
		Applicable:      applicable,
		ExistingRule:    false,
		Reason:          reason,
	}, nil
}
