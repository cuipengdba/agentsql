package mask

import (
	"errors"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestPhoneMasking(t *testing.T) {
	redactor, err := NewRedactor([]Rule{{
		Column:        "phone",
		SensitiveType: TypePhone,
		Algorithm:     AlgoMask,
	}})
	require.NoError(t, err)
	tests := []struct {
		name     string
		input    string
		expected string
		changed  bool
	}{
		{name: "eleven digits", input: "13812345678", expected: "138****5678", changed: true},
		{name: "plus country code", input: "+8613812345678", expected: "138****5678", changed: true},
		{name: "country code without plus", input: "8613812345678", expected: "138****5678", changed: true},
		{name: "ten digits", input: "1234567890", expected: "123*7890", changed: true},
		{name: "six digits", input: "123456", expected: "1*****", changed: true},
		{name: "two digits unchanged", input: "12", expected: "12", changed: false},
		{name: "empty unchanged", input: "", expected: "", changed: false},
		{name: "NULL unchanged", input: "NULL", expected: "NULL", changed: false},
		{name: "nil sentinel unchanged", input: "<nil>", expected: "<nil>", changed: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, report := redactor.Apply(model.QueryResult{
				Columns: []string{"phone"},
				Rows:    [][]string{{test.input}},
			})
			require.Equal(t, test.expected, result.Rows[0][0])
			if test.changed {
				require.Equal(t, 1, report.MaskedCells)
			} else {
				require.Zero(t, report.MaskedCells)
			}
			require.Equal(t, SensitiveType(TypePhone), report.TouchedColumns[0])
		})
	}
}

func TestEmailMasking(t *testing.T) {
	redactor, err := NewRedactor([]Rule{{
		Column:        "email",
		SensitiveType: TypeEmail,
		Algorithm:     AlgoMask,
	}})
	require.NoError(t, err)
	tests := []struct {
		name     string
		input    string
		expected string
		changed  bool
	}{
		{name: "standard", input: "ZhangSan@x.com", expected: "Z***@x.com", changed: true},
		{name: "multiple domain points", input: "a.b@example.co.uk", expected: "a***@example.co.uk", changed: true},
		{name: "last at separates", input: "name@sub@x.com", expected: "n***@x.com", changed: true},
		{name: "without at", input: "ZhangSan", expected: "Z***", changed: true},
		{name: "empty unchanged", input: "", expected: "", changed: false},
		{name: "NULL unchanged", input: "null", expected: "null", changed: false},
		{name: "nil sentinel unchanged", input: "<nil>", expected: "<nil>", changed: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, report := redactor.Apply(model.QueryResult{
				Columns: []string{"email"},
				Rows:    [][]string{{test.input}},
			})
			require.Equal(t, test.expected, result.Rows[0][0])
			if test.changed {
				require.Equal(t, 1, report.MaskedCells)
			} else {
				require.Zero(t, report.MaskedCells)
			}
			require.Equal(t, SensitiveType(TypeEmail), report.TouchedColumns[0])
		})
	}
}

func TestRedactorMatchesFinalColumnNames(t *testing.T) {
	redactor, err := NewRedactor([]Rule{
		{Column: "p", SensitiveType: TypePhone, Algorithm: AlgoMask},
		{Column: "mobile", SensitiveType: TypePhone, Algorithm: AlgoMask},
		{Column: "email", SensitiveType: TypeEmail, Algorithm: AlgoMask},
	})
	require.NoError(t, err)
	result, report := redactor.Apply(model.QueryResult{
		Columns: []string{` "P" `, "`mobile`", "[email]", "ordinary"},
		Rows: [][]string{{
			"13812345678",
			"13912345678",
			"user@example.com",
			"unchanged",
		}},
	})
	require.Equal(t, []string{"138****5678", "139****5678", "u***@example.com", "unchanged"}, result.Rows[0])
	require.Equal(t, map[int]SensitiveType{
		0: TypePhone,
		1: TypePhone,
		2: TypeEmail,
	}, report.TouchedColumns)
	require.Equal(t, 3, report.MaskedCells)
}

func TestRedactorDeepCopiesResultAndPreservesMetadata(t *testing.T) {
	input := model.QueryResult{
		Columns:   []string{"phone", "ordinary"},
		Rows:      [][]string{{"13812345678", "exact"}, nil, {"NULL"}},
		RowCount:  3,
		Truncated: true,
		LatencyMS: 17,
	}
	originalColumns := append([]string(nil), input.Columns...)
	originalRows := make([][]string, len(input.Rows))
	for index, row := range input.Rows {
		if row != nil {
			originalRows[index] = append([]string(nil), row...)
		}
	}
	redactor, err := NewRedactor([]Rule{{Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask}})
	require.NoError(t, err)
	output, report := redactor.Apply(input)
	require.Equal(t, originalColumns, input.Columns)
	require.Equal(t, originalRows, input.Rows)
	require.Len(t, output.Columns, len(input.Columns))
	require.Len(t, output.Rows, len(input.Rows))
	require.Equal(t, 3, output.RowCount)
	require.True(t, output.Truncated)
	require.Equal(t, int64(17), output.LatencyMS)
	require.Equal(t, 1, report.MaskedCells)
	require.Equal(t, "13812345678", input.Rows[0][0])
	require.Equal(t, "138****5678", output.Rows[0][0])
	output.Columns[0] = "changed"
	output.Rows[0][0] = "changed"
	require.Equal(t, "phone", input.Columns[0])
	require.Equal(t, "13812345678", input.Rows[0][0])
}

func TestRedactorReportCountsOnlyChangedCells(t *testing.T) {
	redactor, err := NewRedactor([]Rule{
		{Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask},
		{Column: "email", SensitiveType: TypeEmail, Algorithm: AlgoMask},
	})
	require.NoError(t, err)
	result, report := redactor.Apply(model.QueryResult{
		Columns: []string{"phone", "email", "plain"},
		Rows: [][]string{
			{"NULL", "", "NULL"},
			{"13812345678", "<nil>", "plain"},
		},
	})
	require.Equal(t, "NULL", result.Rows[0][0])
	require.Equal(t, "", result.Rows[0][1])
	require.Equal(t, "NULL", result.Rows[0][2])
	require.Equal(t, "plain", result.Rows[1][2])
	require.Equal(t, 1, report.MaskedCells)
	require.Equal(t, map[int]SensitiveType{0: TypePhone, 1: TypeEmail}, report.TouchedColumns)
}

func TestNewRedactorValidation(t *testing.T) {
	tests := []struct {
		name  string
		rules []Rule
		err   error
	}{
		{
			name: "duplicate normalized column",
			rules: []Rule{
				{Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask},
				{Column: `"PHONE"`, SensitiveType: TypePhone, Algorithm: AlgoMask},
			},
			err: ErrDuplicateMaskColumn,
		},
		{
			name:  "idcard is unsupported",
			rules: []Rule{{Column: "id", SensitiveType: TypeIDCard, Algorithm: AlgoMask}},
			err:   ErrUnsupportedType,
		},
		{
			name:  "bankcard is unsupported",
			rules: []Rule{{Column: "card", SensitiveType: TypeBankCard, Algorithm: AlgoMask}},
			err:   ErrUnsupportedType,
		},
		{
			name:  "hash is unsupported",
			rules: []Rule{{Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoHash}},
			err:   ErrUnsupportedAlgorithm,
		},
		{
			name:  "range is unsupported",
			rules: []Rule{{Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoRange}},
			err:   ErrUnsupportedAlgorithm,
		},
		{
			name:  "block is unsupported",
			rules: []Rule{{Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoBlock}},
			err:   ErrUnsupportedAlgorithm,
		},
		{
			name:  "blank column is invalid",
			rules: []Rule{{Column: "   ", SensitiveType: TypePhone, Algorithm: AlgoMask}},
			err:   nil,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewRedactor(test.rules)
			require.Error(t, err)
			if test.err != nil {
				require.ErrorIs(t, err, test.err)
			} else {
				require.False(t, errors.Is(err, ErrUnsupportedType))
			}
		})
	}
}

func TestEmptyRulesReturnIndependentEquivalentCopy(t *testing.T) {
	input := model.QueryResult{
		Columns: []string{"phone"},
		Rows:    [][]string{{"13812345678"}},
	}
	redactor, err := NewRedactor(nil)
	require.NoError(t, err)
	output, report := redactor.Apply(input)
	require.Equal(t, input, output)
	require.Equal(t, RedactReport{}, report)
	output.Columns[0] = "changed"
	output.Rows[0][0] = "changed"
	require.Equal(t, "phone", input.Columns[0])
	require.Equal(t, "13812345678", input.Rows[0][0])
}

func TestRedactorHandlesShortRowsAndTypedNil(t *testing.T) {
	redactor, err := NewRedactor([]Rule{{Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask}})
	require.NoError(t, err)
	output, report := redactor.Apply(model.QueryResult{
		Columns: []string{"phone", "ordinary"},
		Rows:    [][]string{{"13812345678"}, nil},
	})
	require.Equal(t, "138****5678", output.Rows[0][0])
	require.Equal(t, 1, report.MaskedCells)
	var typedNil *resultRedactor
	output, report = typedNil.Apply(model.QueryResult{Columns: []string{"phone"}, Rows: [][]string{{"13812345678"}}})
	require.Equal(t, "13812345678", output.Rows[0][0])
	require.Equal(t, RedactReport{}, report)
}

func TestUnsupportedErrorsAreErrorsIsCompatible(t *testing.T) {
	_, err := NewRedactor([]Rule{{Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoHash}})
	require.True(t, errors.Is(err, ErrUnsupportedAlgorithm))
	_, err = NewRedactor([]Rule{{Column: "id", SensitiveType: TypeIDCard, Algorithm: AlgoMask}})
	require.True(t, errors.Is(err, ErrUnsupportedType))
}
