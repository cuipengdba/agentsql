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

func TestRedactorMatchesDirectSourceColumns(t *testing.T) {
	redactor, err := NewRedactor([]Rule{{
		Column:        "phone",
		SensitiveType: TypePhone,
		Algorithm:     AlgoMask,
	}})
	require.NoError(t, err)
	sourceAware, ok := redactor.(SourceAwareRedactor)
	require.True(t, ok)

	input := model.QueryResult{
		Columns: []string{"mobile", "ordinary"},
		Rows:    [][]string{{"13812345678", "byte-for-byte"}},
	}
	output, report := sourceAware.ApplyWithSourceColumns(input, []string{"phone", ""})

	require.Equal(t, "138****5678", output.Rows[0][0])
	require.Equal(t, "byte-for-byte", output.Rows[0][1])
	require.Equal(t, map[int]SensitiveType{0: TypePhone}, report.TouchedColumns)
	require.Equal(t, 1, report.MaskedCells)
	require.Equal(t, "13812345678", input.Rows[0][0])
	require.Equal(t, "byte-for-byte", input.Rows[0][1])
	output.Columns[0] = "changed"
	output.Rows[0][0] = "changed"
	require.Equal(t, "mobile", input.Columns[0])
	require.Equal(t, "13812345678", input.Rows[0][0])
}

func TestRedactorFinalColumnTakesPriorityOverSourceColumn(t *testing.T) {
	redactor, err := NewRedactor([]Rule{
		{Column: "email", SensitiveType: TypeEmail, Algorithm: AlgoMask},
		{Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask},
	})
	require.NoError(t, err)
	sourceAware := redactor.(SourceAwareRedactor)

	output, report := sourceAware.ApplyWithSourceColumns(model.QueryResult{
		Columns: []string{"email", "phone"},
		Rows:    [][]string{{"13812345678", "13912345678"}},
	}, []string{"phone", "email"})

	require.Equal(t, []string{"1***", "139****5678"}, output.Rows[0])
	require.Equal(t, map[int]SensitiveType{0: TypeEmail, 1: TypePhone}, report.TouchedColumns)
	require.Equal(t, 2, report.MaskedCells)
}

func TestRedactorInvalidOrEmptySourcesFallBackToFinalNames(t *testing.T) {
	redactor, err := NewRedactor([]Rule{
		{Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask},
		{Column: "email", SensitiveType: TypeEmail, Algorithm: AlgoMask},
	})
	require.NoError(t, err)
	sourceAware := redactor.(SourceAwareRedactor)
	input := model.QueryResult{
		Columns: []string{"ordinary", "email"},
		Rows:    [][]string{{"13812345678", "user@example.com"}},
	}

	tests := []struct {
		name    string
		sources []string
	}{
		{name: "nil", sources: nil},
		{name: "too short", sources: []string{"phone"}},
		{name: "too long", sources: []string{"phone", "", "email"}},
		{name: "empty source slot", sources: []string{"", ""}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output, report := sourceAware.ApplyWithSourceColumns(input, test.sources)
			require.Equal(t, "13812345678", output.Rows[0][0])
			require.Equal(t, "u***@example.com", output.Rows[0][1])
			require.Equal(t, map[int]SensitiveType{1: TypeEmail}, report.TouchedColumns)
			require.Equal(t, 1, report.MaskedCells)
		})
	}
}

func TestRedactorLegacyApplyBehaviorAndReportRemainUnchanged(t *testing.T) {
	redactor, err := NewRedactor([]Rule{{
		Column:        "phone",
		SensitiveType: TypePhone,
		Algorithm:     AlgoMask,
	}})
	require.NoError(t, err)
	input := model.QueryResult{
		Columns: []string{"phone", "mobile"},
		Rows: [][]string{
			{"13812345678", "13912345678"},
			{"NULL", "unchanged"},
		},
	}

	output, report := redactor.Apply(input)

	require.Equal(t, "138****5678", output.Rows[0][0])
	require.Equal(t, "13912345678", output.Rows[0][1])
	require.Equal(t, "NULL", output.Rows[1][0])
	require.Equal(t, "unchanged", output.Rows[1][1])
	require.Equal(t, map[int]SensitiveType{0: TypePhone}, report.TouchedColumns)
	require.Equal(t, 1, report.MaskedCells)
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
			name:  "unknown type is unsupported",
			rules: []Rule{{Column: "value", SensitiveType: SensitiveType("unknown"), Algorithm: AlgoMask}},
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

func TestNewRedactorAcceptsAllSupportedTypes(t *testing.T) {
	types := []SensitiveType{TypePhone, TypeEmail, TypeIDCard, TypeBankCard, TypeIP, TypeBirthDate}
	for _, sensitiveType := range types {
		t.Run(string(sensitiveType), func(t *testing.T) {
			_, err := NewRedactor([]Rule{{
				Column:        "value",
				SensitiveType: sensitiveType,
				Algorithm:     AlgoMask,
			}})
			require.NoError(t, err)
		})
	}
}

func TestNewRedactorDeterministicallyMergesSameColumn(t *testing.T) {
	for _, rules := range [][]Rule{
		{
			{Column: "contact", SensitiveType: TypeEmail, Algorithm: AlgoMask},
			{Column: `"CONTACT"`, SensitiveType: TypePhone, Algorithm: AlgoMask},
			{Column: " contact ", SensitiveType: TypePhone, Algorithm: AlgoMask},
		},
		{
			{Column: "contact", SensitiveType: TypePhone, Algorithm: AlgoMask},
			{Column: "CONTACT", SensitiveType: TypeEmail, Algorithm: AlgoMask},
		},
	} {
		redactor, err := NewRedactor(rules)
		require.NoError(t, err)
		result, report := redactor.Apply(model.QueryResult{
			Columns: []string{"CONTACT"}, Rows: [][]string{{"13812345678"}},
		})
		require.Equal(t, "138****5678", result.Rows[0][0])
		require.Equal(t, TypePhone, report.TouchedColumns[0])
	}
	require.Equal(t, "phone", NormalizeColumnName(` "PHONE" `))
}

func TestSensitiveTypeOrderAndSameColumnDeduplication(t *testing.T) {
	tests := []struct {
		name     string
		rules    []SensitiveType
		input    string
		expected string
		selected SensitiveType
	}{
		{name: "phone before every type", rules: []SensitiveType{TypeBirthDate, TypeIP, TypeBankCard, TypeIDCard, TypeEmail, TypePhone}, input: "13812345678", expected: "138****5678", selected: TypePhone},
		{name: "email before later types", rules: []SensitiveType{TypeBirthDate, TypeIP, TypeBankCard, TypeIDCard, TypeEmail}, input: "user@example.com", expected: "u***@example.com", selected: TypeEmail},
		{name: "idcard before later types", rules: []SensitiveType{TypeBirthDate, TypeIP, TypeBankCard, TypeIDCard}, input: "11010519491231002X", expected: "110105********002X", selected: TypeIDCard},
		{name: "bankcard before later types", rules: []SensitiveType{TypeBirthDate, TypeIP, TypeBankCard}, input: "4111111111111111", expected: "411111******1111", selected: TypeBankCard},
		{name: "ip before birthdate", rules: []SensitiveType{TypeBirthDate, TypeIP}, input: "192.168.1.20", expected: "192.168.*.*", selected: TypeIP},
		{name: "birthdate last", rules: []SensitiveType{TypeBirthDate}, input: "2000-02-29", expected: "2000-**-**", selected: TypeBirthDate},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rules := make([]Rule, 0, len(test.rules))
			for _, sensitiveType := range test.rules {
				rules = append(rules, Rule{Column: "shared", SensitiveType: sensitiveType, Algorithm: AlgoMask})
			}
			redactor, err := NewRedactor(rules)
			require.NoError(t, err)
			result, report := redactor.Apply(model.QueryResult{Columns: []string{"shared"}, Rows: [][]string{{test.input}}})
			require.Equal(t, test.expected, result.Rows[0][0])
			require.Equal(t, map[int]SensitiveType{0: test.selected}, report.TouchedColumns)
			require.Equal(t, 1, report.MaskedCells)
		})
	}

	require.Equal(t, []int{0, 1, 2, 3, 4, 5}, []int{
		SensitiveTypeOrder(TypePhone),
		SensitiveTypeOrder(TypeEmail),
		SensitiveTypeOrder(TypeIDCard),
		SensitiveTypeOrder(TypeBankCard),
		SensitiveTypeOrder(TypeIP),
		SensitiveTypeOrder(TypeBirthDate),
	})
	require.Equal(t, 6, SensitiveTypeOrder(SensitiveType("unknown")))
}

func TestAdditionalTypesReportAndFailClosedBehavior(t *testing.T) {
	redactor, err := NewRedactor([]Rule{
		{Column: "id", SensitiveType: TypeIDCard, Algorithm: AlgoMask},
		{Column: "card", SensitiveType: TypeBankCard, Algorithm: AlgoMask},
		{Column: "address", SensitiveType: TypeIP, Algorithm: AlgoMask},
		{Column: "birthday", SensitiveType: TypeBirthDate, Algorithm: AlgoMask},
	})
	require.NoError(t, err)
	input := model.QueryResult{
		Columns: []string{"id", "card", "address", "birthday"},
		Rows: [][]string{
			{"11010519491231002x", "4111 1111-1111 1111", "2001:db8::1", "2000-02-29T08:09:10.123+08:00"},
			{"invalid-id", "invalid-card", "host:443", "2001-02-29"},
			{"NULL", "", "<nil>", " null "},
		},
	}

	result, report := redactor.Apply(input)
	require.Equal(t, []string{"110105********002X", "411111******1111", "2001:0db8:****", "2000-**-**"}, result.Rows[0])
	require.Equal(t, []string{RedactedFallback, RedactedFallback, RedactedFallback, RedactedFallback}, result.Rows[1])
	require.Equal(t, input.Rows[2], result.Rows[2])
	require.Equal(t, map[int]SensitiveType{
		0: TypeIDCard,
		1: TypeBankCard,
		2: TypeIP,
		3: TypeBirthDate,
	}, report.TouchedColumns)
	require.Equal(t, 8, report.MaskedCells)
	require.Equal(t, "11010519491231002x", input.Rows[0][0])
	result.Rows[0][0] = "changed"
	require.Equal(t, "11010519491231002x", input.Rows[0][0])
}

func TestAdditionalTypesPreserveSourceAwareMatching(t *testing.T) {
	redactor, err := NewRedactor([]Rule{{Column: "identity", SensitiveType: TypeIDCard, Algorithm: AlgoMask}})
	require.NoError(t, err)
	sourceAware := redactor.(SourceAwareRedactor)
	input := model.QueryResult{Columns: []string{"alias"}, Rows: [][]string{{"11010519491231002X"}}}

	output, report := sourceAware.ApplyWithSourceColumns(input, []string{"identity"})

	require.Equal(t, "110105********002X", output.Rows[0][0])
	require.Equal(t, map[int]SensitiveType{0: TypeIDCard}, report.TouchedColumns)
	require.Equal(t, 1, report.MaskedCells)
	require.Equal(t, "11010519491231002X", input.Rows[0][0])
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
	_, err = NewRedactor([]Rule{{Column: "value", SensitiveType: SensitiveType("unknown"), Algorithm: AlgoMask}})
	require.True(t, errors.Is(err, ErrUnsupportedType))
}
