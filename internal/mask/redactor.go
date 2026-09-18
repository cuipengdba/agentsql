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
	rules []redactorRule
}

// NewRedactor validates and freezes the supplied rules for deterministic use.
func NewRedactor(rules []Rule) (Redactor, error) {
	validated := make([]redactorRule, 0, len(rules))
	for index, rule := range rules {
		column := normalizeColumnName(rule.Column)
		if column == "" {
			return nil, fmt.Errorf("mask rule %d: column is required", index)
		}
		if !isSupportedSensitiveType(rule.SensitiveType) {
			return nil, fmt.Errorf("mask rule %q: %w", rule.Column, ErrUnsupportedType)
		}
		if rule.Algorithm != AlgoMask {
			return nil, fmt.Errorf("mask rule %q: %w", rule.Column, ErrUnsupportedAlgorithm)
		}
		validated = append(validated, redactorRule{
			column:        column,
			sensitiveType: rule.SensitiveType,
			algorithm:     rule.Algorithm,
		})
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
		if validated[left].sensitiveType != validated[right].sensitiveType {
			return validated[left].sensitiveType < validated[right].sensitiveType
		}
		return validated[left].algorithm < validated[right].algorithm
	})
	compiled := make([]redactorRule, 0, len(validated))
	for _, rule := range validated {
		if len(compiled) > 0 && compiled[len(compiled)-1].column == rule.column {
			continue
		}
		compiled = append(compiled, rule)
	}
	return &resultRedactor{rules: compiled}, nil
}

// SensitiveTypeOrder returns the shared deterministic sort key: phone, email,
// idcard, bankcard, ip, then birthdate. Unknown types sort after them.
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
	default:
		return 6
	}
}

func isSupportedSensitiveType(sensitiveType SensitiveType) bool {
	switch sensitiveType {
	case TypePhone, TypeEmail, TypeIDCard, TypeBankCard, TypeIP, TypeBirthDate:
		return true
	default:
		return false
	}
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
			masked, changed := applyRule(rule, value)
			if changed {
				copyResult.Rows[rowIndex][columnIndex] = masked
				report.MaskedCells++
			}
		}
	}
	return copyResult, report
}

func applyRule(rule redactorRule, value string) (string, bool) {
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
