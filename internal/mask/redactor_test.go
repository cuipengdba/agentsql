package mask

import (
	"errors"
	"strings"
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

func TestRedactionPlanInjectionAndSourceConflict(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	rules := []Rule{{Column: "secret", SensitiveType: TypeGeneric, Algorithm: AlgoHash}}
	plan, err := BuildRedactionPlan(1, map[int][]byte{1: key})
	require.NoError(t, err)

	fromPlan, err := NewRedactor(rules, WithRedactionPlan(plan))
	require.NoError(t, err)
	fromKey, err := NewRedactor(rules, WithHashKey(key))
	require.NoError(t, err)
	input := model.QueryResult{Columns: []string{"secret"}, Rows: [][]string{{"value"}}}
	planResult, _ := fromPlan.Apply(input)
	keyResult, _ := fromKey.Apply(input)
	require.Equal(t, keyResult, planResult)

	_, err = NewRedactor(rules, WithHashKey(key), WithRedactionPlan(RedactionPlan{}))
	require.NoError(t, err)
	_, err = NewRedactor(rules, WithHashKey(key), WithRedactionPlan(plan))
	require.Error(t, err)
}

func TestRedactionPlanCopiesSelectedMaterialAndEnablesVersionedActive(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	keys := map[int][]byte{1: key}
	plan, err := BuildRedactionPlan(1, keys)
	require.NoError(t, err)
	want := plan.active.Fingerprint("value")
	key[0] = 'X'
	keys[1] = []byte("abcdef0123456789abcdef0123456789")
	require.Equal(t, want, plan.active.Fingerprint("value"))

	versioned, err := BuildRedactionPlan(2, map[int][]byte{2: []byte("0123456789abcdef0123456789abcdef")})
	require.NoError(t, err)
	redactor, err := NewRedactor(
		[]Rule{{Column: "secret", SensitiveType: TypeGeneric, Algorithm: AlgoHash}},
		WithRedactionPlan(versioned),
	)
	require.NoError(t, err)
	result, report := redactor.Apply(model.QueryResult{Columns: []string{"secret"}, Rows: [][]string{{"value"}}})
	require.Regexp(t, `^h\.2\.[0-9a-f]{32}$`, result.Rows[0][0])
	require.NotNil(t, report.HashKeyVersion)
	require.Equal(t, 2, *report.HashKeyVersion)
	upperBoundary, err := BuildRedactionPlan(9999, map[int][]byte{9999: key})
	require.NoError(t, err)
	require.Regexp(t, `^h\.9999\.[0-9a-f]{32}$`, upperBoundary.active.Fingerprint("value"))

	for _, invalidVersion := range []int{-1, 0, 10000} {
		_, err = BuildRedactionPlan(invalidVersion, map[int][]byte{invalidVersion: key})
		require.Error(t, err)
	}
	_, err = BuildRedactionPlan(2, map[int][]byte{})
	require.ErrorIs(t, err, ErrHashKeyRequired)
	_, err = BuildRedactionPlan(2, map[int][]byte{2: []byte("short")})
	require.ErrorIs(t, err, ErrHashKeyTooShort)
}

func TestRedactReportHashKeyVersionOnlyForSuccessfulNonEmptyHash(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	plan, err := BuildRedactionPlan(1, map[int][]byte{1: key})
	require.NoError(t, err)
	redactor, err := NewRedactor([]Rule{{Column: "secret", SensitiveType: TypeGeneric, Algorithm: AlgoHash}}, WithRedactionPlan(plan))
	require.NoError(t, err)

	_, report := redactor.Apply(model.QueryResult{Columns: []string{"secret"}, Rows: [][]string{{"value"}, {"h." + strings.Repeat("a", 32)}}})
	require.NotNil(t, report.HashKeyVersion)
	require.Equal(t, 1, *report.HashKeyVersion)

	_, emptyReport := redactor.Apply(model.QueryResult{Columns: []string{"secret"}, Rows: [][]string{{""}, {"NULL"}}})
	require.Nil(t, emptyReport.HashKeyVersion)

	withoutHash, err := NewRedactor([]Rule{{Column: "secret", SensitiveType: TypeGeneric, Algorithm: AlgoBlock}})
	require.NoError(t, err)
	_, blockReport := withoutHash.Apply(model.QueryResult{Columns: []string{"secret"}, Rows: [][]string{{"value"}}})
	require.Nil(t, blockReport.HashKeyVersion)
}

func TestHashRuleWithoutActiveFailsClosedAndCounts(t *testing.T) {
	redactor := &resultRedactor{globalRules: map[string]redactorRule{
		"secret": {column: "secret", sensitiveType: TypeGeneric, algorithm: AlgoHash},
	}}
	result, report := redactor.Apply(model.QueryResult{
		Columns: []string{"secret"}, Rows: [][]string{{"value"}, {""}, {"NULL"}},
	})
	require.Equal(t, RedactedFallback, result.Rows[0][0])
	require.Equal(t, "", result.Rows[1][0])
	require.Equal(t, "NULL", result.Rows[2][0])
	require.Equal(t, 1, report.HashFallbackCount)
	require.Equal(t, 1, redactor.hashFallbackCount)
	require.Equal(t, 1, report.MaskedCells)
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
		opts  []Option
		err   error
	}{
		{
			name:  "unknown type is unsupported",
			rules: []Rule{{Column: "value", SensitiveType: SensitiveType("unknown"), Algorithm: AlgoMask}},
			err:   ErrUnsupportedType,
		},
		{
			name:  "generic mask type is unsupported",
			rules: []Rule{{Column: "value", SensitiveType: TypeGeneric, Algorithm: AlgoMask}},
			err:   ErrUnsupportedType,
		},
		{
			name:  "hash requires key",
			rules: []Rule{{Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoHash}},
			err:   ErrHashKeyRequired,
		},
		{
			name:  "hash rejects short key",
			rules: []Rule{{Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoHash}},
			opts:  []Option{WithHashKey([]byte("1234567890123456789012345678901"))},
			err:   ErrHashKeyTooShort,
		},
		{
			name:  "range rejects unsupported type",
			rules: []Rule{{Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoRange}},
			err:   ErrUnsupportedType,
		},
		{
			name:  "blank column is invalid",
			rules: []Rule{{Column: "   ", SensitiveType: TypePhone, Algorithm: AlgoMask}},
			err:   nil,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewRedactor(test.rules, test.opts...)
			require.Error(t, err)
			if test.err != nil {
				require.ErrorIs(t, err, test.err)
			} else {
				require.False(t, errors.Is(err, ErrUnsupportedType))
			}
		})
	}
}

func TestValidateRuleChecksStructureWithoutHashKey(t *testing.T) {
	valid := []Rule{
		{Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask},
		{Column: "phone_hash", SensitiveType: TypePhone, Algorithm: AlgoHash},
		{Column: "anything", SensitiveType: TypeGeneric, Algorithm: AlgoHash},
		{Column: "phone_block", SensitiveType: TypePhone, Algorithm: AlgoBlock},
		{Column: "anything_block", SensitiveType: TypeGeneric, Algorithm: AlgoBlock},
	}
	for _, rule := range valid {
		require.NoError(t, ValidateRule(rule))
	}

	require.ErrorIs(t, ValidateRule(Rule{Column: "value", SensitiveType: TypeGeneric, Algorithm: AlgoMask}), ErrUnsupportedType)
	require.ErrorIs(t, ValidateRule(Rule{Column: "value", SensitiveType: SensitiveType("unknown"), Algorithm: AlgoHash}), ErrUnsupportedType)
	require.ErrorIs(t, ValidateRule(Rule{Column: "value", SensitiveType: TypePhone, Algorithm: AlgoRange}), ErrUnsupportedType)
	require.ErrorIs(t, ValidateRule(Rule{Column: "value", SensitiveType: TypePhone, Algorithm: Algorithm("unknown")}), ErrUnsupportedAlgorithm)
	require.Error(t, ValidateRule(Rule{Column: " ` ` ", SensitiveType: TypePhone, Algorithm: AlgoMask}))
}

func TestNewRedactorAcceptsAllSupportedTypes(t *testing.T) {
	maskTypes := []SensitiveType{TypePhone, TypeEmail, TypeIDCard, TypeBankCard, TypeIP, TypeBirthDate}
	for _, sensitiveType := range maskTypes {
		t.Run("mask/"+string(sensitiveType), func(t *testing.T) {
			_, err := NewRedactor([]Rule{{
				Column:        "value",
				SensitiveType: sensitiveType,
				Algorithm:     AlgoMask,
			}})
			require.NoError(t, err)
		})
	}
	hashTypes := append(append([]SensitiveType(nil), maskTypes...), TypeGeneric, TypeNumber, TypeDate)
	for _, sensitiveType := range hashTypes {
		t.Run("hash/"+string(sensitiveType), func(t *testing.T) {
			_, err := NewRedactor([]Rule{{
				Column: "value", SensitiveType: sensitiveType, Algorithm: AlgoHash,
			}}, WithHashKey([]byte("0123456789abcdef0123456789abcdef")))
			require.NoError(t, err)
		})
	}
	for _, sensitiveType := range hashTypes {
		t.Run("block/"+string(sensitiveType), func(t *testing.T) {
			_, err := NewRedactor([]Rule{{
				Column: "value", SensitiveType: sensitiveType, Algorithm: AlgoBlock,
			}})
			require.NoError(t, err)
		})
	}
}

func TestNewRedactorRejectsNormalizedDuplicateColumns(t *testing.T) {
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
		{
			{Column: "contact", SensitiveType: TypePhone, Algorithm: AlgoMask},
			{Column: "CONTACT", SensitiveType: TypeGeneric, Algorithm: AlgoHash},
		},
		{
			{Column: "contact", SensitiveType: TypePhone, Algorithm: AlgoMask},
			{Column: "CONTACT", SensitiveType: TypeGeneric, Algorithm: AlgoBlock},
		},
		{
			{Column: "contact", SensitiveType: TypeGeneric, Algorithm: AlgoHash},
			{Column: "CONTACT", SensitiveType: TypeGeneric, Algorithm: AlgoBlock},
		},
	} {
		_, err := NewRedactor(rules)
		require.ErrorIs(t, err, ErrDuplicateMaskColumn)
	}
	require.Equal(t, "phone", NormalizeColumnName(` "PHONE" `))
}

func TestMaskOnlyRedactorDoesNotRequireOrValidateHashKey(t *testing.T) {
	_, err := NewRedactor(nil)
	require.NoError(t, err)
	_, err = NewRedactor([]Rule{{
		Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask,
	}}, WithHashKey([]byte("short")))
	require.NoError(t, err)
}

func TestValidateRuleBlockSupportsAllKnownTypesAndRejectsUnknown(t *testing.T) {
	knownTypes := []SensitiveType{
		TypePhone, TypeEmail, TypeIDCard, TypeBankCard, TypeIP, TypeBirthDate, TypeGeneric, TypeNumber, TypeDate,
	}
	for _, sensitiveType := range knownTypes {
		require.NoError(t, ValidateRule(Rule{
			Column: "value", SensitiveType: sensitiveType, Algorithm: AlgoBlock,
		}), sensitiveType)
	}
	require.ErrorIs(t, ValidateRule(Rule{
		Column: "value", SensitiveType: SensitiveType("future"), Algorithm: AlgoBlock,
	}), ErrUnsupportedType)
}

func TestBlockReplacesAllNonEmptyValuesWithFixedPlaceholder(t *testing.T) {
	types := []SensitiveType{
		TypePhone, TypeEmail, TypeIDCard, TypeBankCard, TypeIP, TypeBirthDate, TypeGeneric, TypeNumber, TypeDate,
	}
	rules := make([]Rule, 0, len(types))
	columns := make([]string, 0, len(types))
	values := make([]string, 0, len(types))
	wantTouched := make(map[int]SensitiveType, len(types))
	for index, sensitiveType := range types {
		column := string(sensitiveType)
		rules = append(rules, Rule{Column: column, SensitiveType: sensitiveType, Algorithm: AlgoBlock})
		columns = append(columns, column)
		values = append(values, "raw-"+column)
		wantTouched[index] = sensitiveType
	}
	redactor, err := NewRedactor(rules)
	require.NoError(t, err)

	result, report := redactor.Apply(model.QueryResult{Columns: columns, Rows: [][]string{values}})
	require.Equal(t, []string{
		BlockPlaceholder, BlockPlaceholder, BlockPlaceholder, BlockPlaceholder,
		BlockPlaceholder, BlockPlaceholder, BlockPlaceholder, BlockPlaceholder,
		BlockPlaceholder,
	}, result.Rows[0])
	require.Equal(t, wantTouched, report.TouchedColumns)
	require.Equal(t, len(types), report.MaskedCells)
}

func TestBlockPreservesEmptySentinelsAndCountsOnlyNonEmptyCells(t *testing.T) {
	redactor, err := NewRedactor([]Rule{{
		Column: "secret", SensitiveType: TypeGeneric, Algorithm: AlgoBlock,
	}})
	require.NoError(t, err)
	values := []string{"", " ", "\t\r\n", "NULL", " null ", "<nil>", " <NIL> ", " NULL value ", " <nil>x "}
	rows := make([][]string, len(values))
	for index, value := range values {
		rows[index] = []string{value}
	}

	result, report := redactor.Apply(model.QueryResult{Columns: []string{"secret"}, Rows: rows})
	for index := 0; index < 7; index++ {
		require.Equal(t, values[index], result.Rows[index][0])
	}
	require.Equal(t, BlockPlaceholder, result.Rows[7][0])
	require.Equal(t, BlockPlaceholder, result.Rows[8][0])
	require.Equal(t, map[int]SensitiveType{0: TypeGeneric}, report.TouchedColumns)
	require.Equal(t, 2, report.MaskedCells)
}

func TestBlockDoesNotDependOnValueFormat(t *testing.T) {
	redactor, err := NewRedactor([]Rule{{
		Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoBlock,
	}})
	require.NoError(t, err)
	values := []string{"not-a-phone", "12", "任意 Unicode 🚀", " 13812345678 ", " null value "}
	rows := make([][]string, len(values))
	for index, value := range values {
		rows[index] = []string{value}
	}

	result, report := redactor.Apply(model.QueryResult{Columns: []string{"phone"}, Rows: rows})
	for _, row := range result.Rows {
		require.Equal(t, BlockPlaceholder, row[0])
	}
	require.Equal(t, len(values), report.MaskedCells)
}

func TestBlockPlaceholderInputStillCountsAsChanged(t *testing.T) {
	redactor, err := NewRedactor([]Rule{{
		Column: "secret", SensitiveType: TypeGeneric, Algorithm: AlgoBlock,
	}})
	require.NoError(t, err)

	result, report := redactor.Apply(model.QueryResult{
		Columns: []string{"secret"}, Rows: [][]string{{BlockPlaceholder}},
	})
	require.Equal(t, BlockPlaceholder, result.Rows[0][0])
	require.Equal(t, 1, report.MaskedCells)
}

func TestNewRedactorMixedMaskHashBlock(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	redactor, err := NewRedactor([]Rule{
		{Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask},
		{Column: "name", SensitiveType: TypeGeneric, Algorithm: AlgoHash},
		{Column: "secret", SensitiveType: TypeGeneric, Algorithm: AlgoBlock},
	}, WithHashKey(key))
	require.NoError(t, err)

	result, report := redactor.Apply(model.QueryResult{
		Columns: []string{"phone", "name", "secret"},
		Rows:    [][]string{{"13812345678", "Alice Zhang", "top-secret"}},
	})
	require.Equal(t, "138****5678", result.Rows[0][0])
	require.Equal(t, "h.d119be750b2bb0be4dd7e2fc9fdae30f", result.Rows[0][1])
	require.Equal(t, BlockPlaceholder, result.Rows[0][2])
	require.Equal(t, map[int]SensitiveType{0: TypePhone, 1: TypeGeneric, 2: TypeGeneric}, report.TouchedColumns)
	require.Equal(t, 3, report.MaskedCells)
}

func TestNewRedactorRejectsDuplicateColumnsAcrossBlockMaskHash(t *testing.T) {
	for _, other := range []Rule{
		{Column: "SECRET", SensitiveType: TypePhone, Algorithm: AlgoMask},
		{Column: "SECRET", SensitiveType: TypeGeneric, Algorithm: AlgoHash},
		{Column: "SECRET", SensitiveType: TypeGeneric, Algorithm: AlgoBlock},
	} {
		_, err := NewRedactor([]Rule{
			{Column: "secret", SensitiveType: TypeGeneric, Algorithm: AlgoBlock},
			other,
		}, WithHashKey([]byte("0123456789abcdef0123456789abcdef")))
		require.ErrorIs(t, err, ErrDuplicateMaskColumn)
	}
}

func TestRangeRejectsUnsupportedTypeAndUnknownAlgorithm(t *testing.T) {
	require.ErrorIs(t, ValidateRule(Rule{
		Column: "value", SensitiveType: TypeGeneric, Algorithm: AlgoRange,
	}), ErrUnsupportedType)
	require.ErrorIs(t, ValidateRule(Rule{
		Column: "value", SensitiveType: TypeGeneric, Algorithm: Algorithm("future"),
	}), ErrUnsupportedAlgorithm)
}

func TestBlockOnlyRedactorDoesNotRequireOrValidateHashKey(t *testing.T) {
	for _, options := range [][]Option{nil, {WithHashKey([]byte("short"))}} {
		redactor, err := NewRedactor([]Rule{{
			Column: "secret", SensitiveType: TypeGeneric, Algorithm: AlgoBlock,
		}}, options...)
		require.NoError(t, err)
		result, report := redactor.Apply(model.QueryResult{
			Columns: []string{"secret"}, Rows: [][]string{{"raw"}},
		})
		require.Equal(t, BlockPlaceholder, result.Rows[0][0])
		require.Equal(t, 1, report.MaskedCells)
	}

	_, err := NewRedactor([]Rule{
		{Column: "secret", SensitiveType: TypeGeneric, Algorithm: AlgoBlock},
		{Column: "name", SensitiveType: TypeGeneric, Algorithm: AlgoHash},
	})
	require.ErrorIs(t, err, ErrHashKeyRequired)
}

func TestSensitiveTypeOrder(t *testing.T) {
	require.Equal(t, []int{0, 1, 2, 3, 4, 5, 6, 7, 8}, []int{
		SensitiveTypeOrder(TypePhone),
		SensitiveTypeOrder(TypeEmail),
		SensitiveTypeOrder(TypeIDCard),
		SensitiveTypeOrder(TypeBankCard),
		SensitiveTypeOrder(TypeIP),
		SensitiveTypeOrder(TypeBirthDate),
		SensitiveTypeOrder(TypeGeneric),
		SensitiveTypeOrder(TypeNumber),
		SensitiveTypeOrder(TypeDate),
	})
	require.Equal(t, 9, SensitiveTypeOrder(SensitiveType("unknown")))
}

func TestHashAllSensitiveTypesAndReport(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	redactor, err := NewRedactor([]Rule{
		{Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoHash},
		{Column: "email", SensitiveType: TypeEmail, Algorithm: AlgoHash},
		{Column: "id", SensitiveType: TypeIDCard, Algorithm: AlgoHash},
		{Column: "card", SensitiveType: TypeBankCard, Algorithm: AlgoHash},
		{Column: "address", SensitiveType: TypeIP, Algorithm: AlgoHash},
		{Column: "birthday", SensitiveType: TypeBirthDate, Algorithm: AlgoHash},
		{Column: "name", SensitiveType: TypeGeneric, Algorithm: AlgoHash},
	}, WithHashKey(key))
	require.NoError(t, err)

	result, report := redactor.Apply(model.QueryResult{
		Columns: []string{"phone", "email", "id", "card", "address", "birthday", "name"},
		Rows: [][]string{{
			"13812345678", "user@example.com", "11010519491231002X", "4111111111111111",
			"192.168.1.20", "2000-02-29", "Alice Zhang",
		}},
	})
	require.Equal(t, []string{
		"h.23c1be66d1c1675e58d6e7a0c19cf802",
		"h.4621a8322e0b5a9bc04262f9459841f1",
		"h.2a53712bfce2d73c5b5bb801fd42d5a1",
		"h.22e799aa7c2ec8bfb3ea133b0ff7f96a",
		"h.bd74724363eaa02d01d018cd93e12540",
		"h.ed7d6561988e50e73ff590b8504466a4",
		"h.d119be750b2bb0be4dd7e2fc9fdae30f",
	}, result.Rows[0])
	require.Equal(t, map[int]SensitiveType{
		0: TypePhone,
		1: TypeEmail,
		2: TypeIDCard,
		3: TypeBankCard,
		4: TypeIP,
		5: TypeBirthDate,
		6: TypeGeneric,
	}, report.TouchedColumns)
	require.Equal(t, 7, report.MaskedCells)
}

func TestHashPreservesSourceAwareMatching(t *testing.T) {
	redactor, err := NewRedactor([]Rule{{
		Column: "name", SensitiveType: TypeGeneric, Algorithm: AlgoHash,
	}}, WithHashKey([]byte("0123456789abcdef0123456789abcdef")))
	require.NoError(t, err)
	sourceAware := redactor.(SourceAwareRedactor)
	input := model.QueryResult{Columns: []string{"alias"}, Rows: [][]string{{"Alice Zhang"}}}

	output, report := sourceAware.ApplyWithSourceColumns(input, []string{"name"})

	require.Equal(t, "h.d119be750b2bb0be4dd7e2fc9fdae30f", output.Rows[0][0])
	require.Equal(t, map[int]SensitiveType{0: TypeGeneric}, report.TouchedColumns)
	require.Equal(t, 1, report.MaskedCells)
	require.Equal(t, "Alice Zhang", input.Rows[0][0])
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
	_, err := NewRedactor([]Rule{{Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoRange}})
	require.True(t, errors.Is(err, ErrUnsupportedType))
	_, err = NewRedactor([]Rule{{Column: "value", SensitiveType: SensitiveType("unknown"), Algorithm: AlgoMask}})
	require.True(t, errors.Is(err, ErrUnsupportedType))
	_, err = NewRedactor([]Rule{{Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoHash}})
	require.True(t, errors.Is(err, ErrHashKeyRequired))
}
