package mask

import (
	"fmt"

	"github.com/cuipengdba/agentsql/internal/model"
)

// NormalizeColumnName returns the canonical result-column key used by masking.
// It does not mutate the supplied string or any stored rule.
func NormalizeColumnName(name string) string {
	return normalizeColumnName(name)
}

// Redactor applies configured masking rules to a query result.
type Redactor interface {
	Apply(result model.QueryResult) (model.QueryResult, RedactReport)
}

// ProjectionLineageAwareRedactor applies rules using projection lineage that
// has already been aligned to result positions. Each outer entry corresponds
// to one result column and contains all set-operation arms for that position.
type ProjectionLineageAwareRedactor interface {
	Redactor
	ApplyWithProjectionLineages(result model.QueryResult, aligned [][]model.LineageArm) (model.QueryResult, RedactReport)
}

// SourceAwareRedactor optionally matches positional top-level direct source
// columns after the final result-column name has failed to match.
type SourceAwareRedactor interface {
	Redactor
	ApplyWithSourceColumns(result model.QueryResult, sources []string) (model.QueryResult, RedactReport)
}

// ColumnSource identifies a result column's physical source column and its
// uniquely resolved physical relation. Source.Table is empty when unresolved.
type ColumnSource struct {
	Column string
	Source model.ObjectRef
}

// RelationSourceAwareRedactor applies rules using both physical column sources
// and all physical relations that may contribute to the statement.
type RelationSourceAwareRedactor interface {
	Redactor
	ApplyWithColumnSources(result model.QueryResult, sources []ColumnSource, possibleRelations []model.ObjectRef) (model.QueryResult, RedactReport)
}

type redactorRule struct {
	schema           string
	table            string
	column           string
	sensitiveType    SensitiveType
	algorithm        Algorithm
	rangeWidth       int64
	rangeOffset      int64
	rangeGranularity RangeGranularity
}

type resultRedactor struct {
	globalRules         map[string]redactorRule
	exactScopedRules    map[string]redactorRule
	wildcardScopedRules map[string]redactorRule
	scopedRulesByColumn map[string][]redactorRule
	hasher              *hasher
}

type redactorOptions struct {
	hashKey []byte
}

// Option configures a Redactor without changing the frozen rule set.
type Option func(*redactorOptions)

// WithHashKey supplies the dedicated HMAC key used by hash rules. The key is
// copied immediately so later caller mutations cannot affect the option.
func WithHashKey(key []byte) Option {
	copiedKey := append([]byte(nil), key...)
	return func(options *redactorOptions) {
		options.hashKey = append([]byte(nil), copiedKey...)
	}
}

// ValidateRule validates the column, sensitive type, and algorithm combination.
// Hash key availability is intentionally a NewRedactor construction concern.
func ValidateRule(rule Rule) error {
	if normalizeColumnName(rule.Column) == "" {
		return fmt.Errorf("mask rule column is required")
	}
	switch rule.Algorithm {
	case AlgoMask, AlgoHash, AlgoBlock:
		if rule.Range != nil {
			return fmt.Errorf("mask rule %q has range parameters for %q algorithm: %w", rule.Column, rule.Algorithm, ErrInvalidRangeParams)
		}
	}
	switch rule.Algorithm {
	case AlgoMask:
		if !isMaskSensitiveType(rule.SensitiveType) {
			return fmt.Errorf("mask rule %q: %w", rule.Column, ErrUnsupportedType)
		}
	case AlgoHash, AlgoBlock:
		if !isKnownSensitiveType(rule.SensitiveType) {
			return fmt.Errorf("mask rule %q: %w", rule.Column, ErrUnsupportedType)
		}
	case AlgoRange:
		if !isRangeSensitiveType(rule.SensitiveType) {
			return fmt.Errorf("mask rule %q: %w", rule.Column, ErrUnsupportedType)
		}
		if err := validateRangeParams(rule); err != nil {
			return fmt.Errorf("mask rule %q: %w", rule.Column, err)
		}
	default:
		return fmt.Errorf("mask rule %q: %w", rule.Column, ErrUnsupportedAlgorithm)
	}
	return nil
}

func validateRangeParams(rule Rule) error {
	switch rule.SensitiveType {
	case TypeNumber:
		if rule.Range == nil || rule.Range.BucketWidth == nil {
			return fmt.Errorf("number range bucket width is required: %w", ErrInvalidRangeParams)
		}
		if width := *rule.Range.BucketWidth; width < 1 || width > 1_000_000_000 {
			return fmt.Errorf("number range bucket width is out of bounds: %w", ErrInvalidRangeParams)
		}
		if rule.Range.BucketOffset != nil {
			if offset := *rule.Range.BucketOffset; offset < -1_000_000_000 || offset > 1_000_000_000 {
				return fmt.Errorf("number range bucket offset is out of bounds: %w", ErrInvalidRangeParams)
			}
		}
		if rule.Range.Granularity != nil {
			return fmt.Errorf("number range granularity is not allowed: %w", ErrInvalidRangeParams)
		}
	case TypeDate:
		if rule.Range == nil {
			return nil
		}
		if rule.Range.BucketWidth != nil || rule.Range.BucketOffset != nil {
			return fmt.Errorf("date range bucket parameters are not allowed: %w", ErrInvalidRangeParams)
		}
		if rule.Range.Granularity != nil {
			switch *rule.Range.Granularity {
			case RangeYear, RangeQuarter, RangeMonth:
			default:
				return fmt.Errorf("date range granularity is invalid: %w", ErrInvalidRangeParams)
			}
		}
	default:
		return fmt.Errorf("range rule type %q: %w", rule.SensitiveType, ErrUnsupportedType)
	}
	return nil
}

// NewRedactor validates and freezes the supplied rules for deterministic use.
func NewRedactor(rules []Rule, opts ...Option) (Redactor, error) {
	globalRules := make(map[string]redactorRule)
	exactScopedRules := make(map[string]redactorRule)
	wildcardScopedRules := make(map[string]redactorRule)
	scopedRulesByColumn := make(map[string][]redactorRule)
	hasHashRule := false
	for index, rule := range rules {
		if err := ValidateRule(rule); err != nil {
			return nil, fmt.Errorf("mask rule %d: %w", index, err)
		}
		if rule.Schema != "" && rule.Table == "" {
			return nil, fmt.Errorf("mask rule %d: schema requires table", index)
		}
		compiled := redactorRule{
			schema:        rule.Schema,
			table:         rule.Table,
			column:        normalizeColumnName(rule.Column),
			sensitiveType: rule.SensitiveType,
			algorithm:     rule.Algorithm,
		}
		if rule.Algorithm == AlgoRange {
			switch rule.SensitiveType {
			case TypeNumber:
				compiled.rangeWidth = *rule.Range.BucketWidth
				if rule.Range.BucketOffset != nil {
					compiled.rangeOffset = *rule.Range.BucketOffset
				}
			case TypeDate:
				compiled.rangeGranularity = RangeYear
				if rule.Range != nil && rule.Range.Granularity != nil {
					compiled.rangeGranularity = *rule.Range.Granularity
				}
			}
		}
		if compiled.table == "" {
			if _, exists := globalRules[compiled.column]; exists {
				return nil, fmt.Errorf("mask rule %q: %w", compiled.column, ErrDuplicateMaskColumn)
			}
			globalRules[compiled.column] = compiled
		} else if compiled.schema == "" {
			key := wildcardScopedRuleKey(compiled.table, compiled.column)
			if _, exists := wildcardScopedRules[key]; exists {
				return nil, fmt.Errorf("mask rule %q: %w", compiled.column, ErrDuplicateMaskColumn)
			}
			wildcardScopedRules[key] = compiled
			scopedRulesByColumn[compiled.column] = append(scopedRulesByColumn[compiled.column], compiled)
		} else {
			key := exactScopedRuleKey(compiled.schema, compiled.table, compiled.column)
			if _, exists := exactScopedRules[key]; exists {
				return nil, fmt.Errorf("mask rule %q: %w", compiled.column, ErrDuplicateMaskColumn)
			}
			exactScopedRules[key] = compiled
			scopedRulesByColumn[compiled.column] = append(scopedRulesByColumn[compiled.column], compiled)
		}
		hasHashRule = hasHashRule || rule.Algorithm == AlgoHash
	}

	options := redactorOptions{}
	for _, option := range opts {
		if option != nil {
			option(&options)
		}
	}
	var hash *hasher
	if hasHashRule {
		var err error
		hash, err = newHasher(options.hashKey)
		if err != nil {
			return nil, err
		}
	}
	return &resultRedactor{
		globalRules:         globalRules,
		exactScopedRules:    exactScopedRules,
		wildcardScopedRules: wildcardScopedRules,
		scopedRulesByColumn: scopedRulesByColumn,
		hasher:              hash,
	}, nil
}

func exactScopedRuleKey(schema, table, column string) string {
	return schema + "\x00" + table + "\x00" + column
}

func wildcardScopedRuleKey(table, column string) string {
	return table + "\x00" + column
}

// SensitiveTypeOrder returns the shared deterministic sort key: phone, email,
// idcard, bankcard, ip, birthdate, generic, number, then date. Unknown types
// sort after them.
func SensitiveTypeOrder(sensitiveType SensitiveType) int {
	switch sensitiveType {
	case TypePhone:
		return 0
	case TypeEmail:
		return 1
	case TypeIDCard:
		return 2
	case TypeBankCard:
		return 3
	case TypeIP:
		return 4
	case TypeBirthDate:
		return 5
	case TypeGeneric:
		return 6
	case TypeNumber:
		return 7
	case TypeDate:
		return 8
	default:
		return 9
	}
}

func isMaskSensitiveType(sensitiveType SensitiveType) bool {
	switch sensitiveType {
	case TypePhone, TypeEmail, TypeIDCard, TypeBankCard, TypeIP, TypeBirthDate:
		return true
	default:
		return false
	}
}

func isKnownSensitiveType(sensitiveType SensitiveType) bool {
	return isMaskSensitiveType(sensitiveType) || sensitiveType == TypeGeneric || isRangeSensitiveType(sensitiveType)
}

func isRangeSensitiveType(sensitiveType SensitiveType) bool {
	return sensitiveType == TypeNumber || sensitiveType == TypeDate
}

func (redactor *resultRedactor) Apply(result model.QueryResult) (model.QueryResult, RedactReport) {
	return redactor.ApplyWithColumnSources(result, nil, nil)
}

func (redactor *resultRedactor) ApplyWithSourceColumns(
	result model.QueryResult,
	sources []string,
) (model.QueryResult, RedactReport) {
	var columnSources []ColumnSource
	if sources != nil && len(sources) == len(result.Columns) {
		columnSources = make([]ColumnSource, len(sources))
		for index, source := range sources {
			columnSources[index].Column = source
		}
	}
	return redactor.ApplyWithColumnSources(result, columnSources, nil)
}

func (redactor *resultRedactor) ApplyWithColumnSources(
	result model.QueryResult,
	sources []ColumnSource,
	possibleRelations []model.ObjectRef,
) (model.QueryResult, RedactReport) {
	copyResult := cloneQueryResult(result)
	report := RedactReport{}
	if redactor == nil || !redactor.hasRules() || len(copyResult.Columns) == 0 {
		return copyResult, report
	}
	for columnIndex, columnName := range copyResult.Columns {
		var source ColumnSource
		if columnIndex < len(sources) {
			source = sources[columnIndex]
		}
		rule, matched := redactor.matchRule(columnName, source)
		if matched {
			report.touchColumn(columnIndex, rule.sensitiveType)
			for rowIndex := range copyResult.Rows {
				if columnIndex >= len(copyResult.Rows[rowIndex]) {
					continue
				}
				value := copyResult.Rows[rowIndex][columnIndex]
				masked, changed := applyRule(rule, value, redactor.hasher)
				if changed {
					copyResult.Rows[rowIndex][columnIndex] = masked
					report.MaskedCells++
				}
			}
			continue
		}

		if source.Source.Table != "" {
			continue
		}
		fallbackType, fallback := redactor.unresolvedScopedType(columnName, source.Column, possibleRelations)
		if !fallback {
			continue
		}
		report.touchColumn(columnIndex, fallbackType)
		maskedCells := 0
		for rowIndex := range copyResult.Rows {
			if columnIndex >= len(copyResult.Rows[rowIndex]) {
				continue
			}
			value := copyResult.Rows[rowIndex][columnIndex]
			masked, changed := applyUnresolvedScopedBlock(value)
			if changed {
				copyResult.Rows[rowIndex][columnIndex] = masked
				report.MaskedCells++
				maskedCells++
			}
		}
		if maskedCells > 0 {
			if report.UnresolvedScopedColumns == nil {
				report.UnresolvedScopedColumns = make(map[int]SensitiveType)
			}
			report.UnresolvedScopedColumns[columnIndex] = fallbackType
		}
	}
	return copyResult, report
}

func (redactor *resultRedactor) hasRules() bool {
	return len(redactor.globalRules) != 0 || len(redactor.exactScopedRules) != 0 || len(redactor.wildcardScopedRules) != 0
}

func (redactor *resultRedactor) matchRule(resultName string, source ColumnSource) (redactorRule, bool) {
	resultColumn := normalizeColumnName(resultName)
	sourceColumn := normalizeColumnName(source.Column)

	// Tier 1: an explicitly renamed result keeps a global result-name rule's
	// protection ahead of any weaker scoped source rule.
	if source.Column != "" && resultName != source.Column {
		if rule, matched := redactor.globalRules[resultColumn]; matched {
			return rule, true
		}
	}

	// Tier 2: a resolved relation first tries its exact schema, then the
	// schema-wildcard rule. A source without a schema never guesses one.
	if source.Source.Table != "" {
		if source.Source.Schema != "" {
			if rule, matched := redactor.exactScopedRules[exactScopedRuleKey(source.Source.Schema, source.Source.Table, sourceColumn)]; matched {
				return rule, true
			}
		}
		if rule, matched := redactor.wildcardScopedRules[wildcardScopedRuleKey(source.Source.Table, sourceColumn)]; matched {
			return rule, true
		}
	}

	// Tiers 3 and 4 preserve the legacy result-name then source-name order.
	if rule, matched := redactor.globalRules[resultColumn]; matched {
		return rule, true
	}
	if source.Column != "" {
		if rule, matched := redactor.globalRules[sourceColumn]; matched {
			return rule, true
		}
	}
	return redactorRule{}, false
}

func (redactor *resultRedactor) unresolvedScopedType(
	resultName string,
	sourceColumnName string,
	possibleRelations []model.ObjectRef,
) (SensitiveType, bool) {
	if len(possibleRelations) == 0 {
		return "", false
	}
	resultColumn := normalizeColumnName(resultName)
	sourceColumn := normalizeColumnName(sourceColumnName)
	matched := false
	var selected SensitiveType
	consider := func(rule redactorRule) {
		if !scopedRuleRelationPossible(rule, possibleRelations) {
			return
		}
		if !matched || SensitiveTypeOrder(rule.sensitiveType) < SensitiveTypeOrder(selected) {
			selected = rule.sensitiveType
			matched = true
		}
	}
	for _, rule := range redactor.scopedRulesByColumn[resultColumn] {
		consider(rule)
	}
	if sourceColumnName != "" && sourceColumn != resultColumn {
		for _, rule := range redactor.scopedRulesByColumn[sourceColumn] {
			consider(rule)
		}
	}
	return selected, matched
}

func scopedRuleRelationPossible(rule redactorRule, possibleRelations []model.ObjectRef) bool {
	for _, relation := range possibleRelations {
		if relation.Table != rule.table {
			continue
		}
		if rule.schema == "" || relation.Schema == rule.schema {
			return true
		}
	}
	return false
}

func (report *RedactReport) touchColumn(columnIndex int, sensitiveType SensitiveType) {
	if report.TouchedColumns == nil {
		report.TouchedColumns = make(map[int]SensitiveType)
	}
	report.TouchedColumns[columnIndex] = sensitiveType
}

func applyUnresolvedScopedBlock(value string) (string, bool) {
	if isEmptySensitiveValue(value) {
		return value, false
	}
	return BlockPlaceholder, true
}

func applyRule(rule redactorRule, value string, hash *hasher) (string, bool) {
	if rule.algorithm == AlgoRange {
		switch rule.sensitiveType {
		case TypeNumber:
			return bucketNumeric(value, rule.rangeWidth, rule.rangeOffset)
		case TypeDate:
			return truncateDate(value, rule.rangeGranularity)
		default:
			return RedactedFallback, true
		}
	}
	if rule.algorithm == AlgoHash {
		if isEmptySensitiveValue(value) || hash == nil {
			return value, false
		}
		return hash.fingerprint(value), true
	}
	if rule.algorithm == AlgoBlock {
		if isEmptySensitiveValue(value) {
			return value, false
		}
		return BlockPlaceholder, true
	}
	if rule.algorithm != AlgoMask {
		return value, false
	}
	switch rule.sensitiveType {
	case TypePhone:
		return maskPhone(value)
	case TypeEmail:
		return maskEmail(value)
	case TypeIDCard:
		return maskIDCard(value)
	case TypeBankCard:
		return maskBankCard(value)
	case TypeIP:
		return maskIP(value)
	case TypeBirthDate:
		return maskBirthDate(value)
	default:
		return value, false
	}
}

func cloneQueryResult(result model.QueryResult) model.QueryResult {
	copyResult := result
	if result.Columns != nil {
		copyResult.Columns = make([]string, len(result.Columns))
		copy(copyResult.Columns, result.Columns)
	}
	if result.Rows != nil {
		copyResult.Rows = make([][]string, len(result.Rows))
		for rowIndex, row := range result.Rows {
			if row == nil {
				continue
			}
			copyResult.Rows[rowIndex] = make([]string, len(row))
			copy(copyResult.Rows[rowIndex], row)
		}
	}
	return copyResult
}

var _ Redactor = (*resultRedactor)(nil)
var _ ProjectionLineageAwareRedactor = (*resultRedactor)(nil)
var _ SourceAwareRedactor = (*resultRedactor)(nil)
var _ RelationSourceAwareRedactor = (*resultRedactor)(nil)
