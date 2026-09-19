package mask

import (
	"fmt"
	"sort"

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

// SourceAwareRedactor optionally matches positional top-level direct source
// columns after the final result-column name has failed to match.
type SourceAwareRedactor interface {
	Redactor
	ApplyWithSourceColumns(result model.QueryResult, sources []string) (model.QueryResult, RedactReport)
}

type redactorRule struct {
	column        string
	sensitiveType SensitiveType
	algorithm     Algorithm
}

type resultRedactor struct {
	rules  []redactorRule
	hasher *hasher
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
	validated := make([]redactorRule, 0, len(rules))
	hasHashRule := false
	for index, rule := range rules {
		if err := ValidateRule(rule); err != nil {
			return nil, fmt.Errorf("mask rule %d: %w", index, err)
		}
		validated = append(validated, redactorRule{
			column:        normalizeColumnName(rule.Column),
			sensitiveType: rule.SensitiveType,
			algorithm:     rule.Algorithm,
		})
		hasHashRule = hasHashRule || rule.Algorithm == AlgoHash
	}
	sort.SliceStable(validated, func(left, right int) bool {
		if validated[left].column != validated[right].column {
			return validated[left].column < validated[right].column
		}
		leftPriority := SensitiveTypeOrder(validated[left].sensitiveType)
		rightPriority := SensitiveTypeOrder(validated[right].sensitiveType)
		if leftPriority != rightPriority {
			return leftPriority < rightPriority
		}
		return false
	})
	for index := 1; index < len(validated); index++ {
		if validated[index-1].column == validated[index].column {
			return nil, fmt.Errorf("mask rule %q: %w", validated[index].column, ErrDuplicateMaskColumn)
		}
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
	return &resultRedactor{rules: validated, hasher: hash}, nil
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
	return redactor.ApplyWithSourceColumns(result, nil)
}

func (redactor *resultRedactor) ApplyWithSourceColumns(
	result model.QueryResult,
	sources []string,
) (model.QueryResult, RedactReport) {
	copyResult := cloneQueryResult(result)
	report := RedactReport{}
	if redactor == nil || len(redactor.rules) == 0 || len(copyResult.Columns) == 0 {
		return copyResult, report
	}
	rulesByColumn := make(map[string]redactorRule, len(redactor.rules))
	for _, rule := range redactor.rules {
		rulesByColumn[rule.column] = rule
	}
	useSources := sources != nil && len(sources) == len(copyResult.Columns)
	for columnIndex, columnName := range copyResult.Columns {
		rule, matched := rulesByColumn[normalizeColumnName(columnName)]
		if !matched && useSources && sources[columnIndex] != "" {
			rule, matched = rulesByColumn[normalizeColumnName(sources[columnIndex])]
		}
		if !matched {
			continue
		}
		if report.TouchedColumns == nil {
			report.TouchedColumns = make(map[int]SensitiveType)
		}
		report.TouchedColumns[columnIndex] = rule.sensitiveType
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
	}
	return copyResult, report
}

func applyRule(rule redactorRule, value string, hash *hasher) (string, bool) {
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
var _ SourceAwareRedactor = (*resultRedactor)(nil)
