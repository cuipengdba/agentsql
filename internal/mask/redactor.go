package mask

import (
	"fmt"

	"github.com/cuipengdba/agentsql/internal/model"
)

// Redactor applies configured masking rules to a query result.
type Redactor interface {
	Apply(result model.QueryResult) (model.QueryResult, RedactReport)
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
	compiled := make([]redactorRule, 0, len(rules))
	seenColumns := make(map[string]struct{}, len(rules))
	for index, rule := range rules {
		column := normalizeColumnName(rule.Column)
		if column == "" {
			return nil, fmt.Errorf("mask rule %d: column is required", index)
		}
		if _, exists := seenColumns[column]; exists {
			return nil, fmt.Errorf("mask rule %q: %w", rule.Column, ErrDuplicateMaskColumn)
		}
		seenColumns[column] = struct{}{}
		if rule.SensitiveType != TypePhone && rule.SensitiveType != TypeEmail {
			return nil, fmt.Errorf("mask rule %q: %w", rule.Column, ErrUnsupportedType)
		}
		if rule.Algorithm != AlgoMask {
			return nil, fmt.Errorf("mask rule %q: %w", rule.Column, ErrUnsupportedAlgorithm)
		}
		compiled = append(compiled, redactorRule{
			column:        column,
			sensitiveType: rule.SensitiveType,
			algorithm:     rule.Algorithm,
		})
	}
	return &resultRedactor{rules: compiled}, nil
}

func (redactor *resultRedactor) Apply(result model.QueryResult) (model.QueryResult, RedactReport) {
	copyResult := cloneQueryResult(result)
	report := RedactReport{}
	if redactor == nil || len(redactor.rules) == 0 || len(copyResult.Columns) == 0 {
		return copyResult, report
	}
	rulesByColumn := make(map[string]redactorRule, len(redactor.rules))
	for _, rule := range redactor.rules {
		rulesByColumn[rule.column] = rule
	}
	for columnIndex, columnName := range copyResult.Columns {
		rule, matched := rulesByColumn[normalizeColumnName(columnName)]
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
