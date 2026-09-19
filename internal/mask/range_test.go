package mask

import (
	"errors"
	"strings"
	"testing"
)

func TestBucketNumericExactDecimalBoundaries(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{raw: "42", want: "[40,50)"},
		{raw: "10", want: "[10,20)"},
		{raw: "40", want: "[40,50)"},
		{raw: "1.0000000000000001", want: "[1,2)"},
		{raw: "0.9999999999999999", want: "[0,1)"},
		{raw: ".5", want: "[0,1)"},
		{raw: "1.", want: "[1,2)"},
	}
	for _, test := range tests {
		t.Run(test.raw, func(t *testing.T) {
			width := int64(1)
			if test.raw == "42" || test.raw == "10" || test.raw == "40" {
				width = 10
			}
			got, processed := bucketNumeric(test.raw, width, 0)
			if got != test.want || !processed {
				t.Fatalf("bucketNumeric(%q) = (%q, %v), want (%q, true)", test.raw, got, processed, test.want)
			}
		})
	}
}

func TestBucketNumericOffsetsAndNegatives(t *testing.T) {
	tests := []struct {
		raw    string
		width  int64
		offset int64
		want   string
	}{
		{raw: "-3", width: 10, want: "[-10,0)"},
		{raw: "-10", width: 10, want: "[-10,0)"},
		{raw: "17.999", width: 10, offset: 18, want: "[8,18)"},
		{raw: "18", width: 10, offset: 18, want: "[18,28)"},
	}
	for _, test := range tests {
		t.Run(test.raw, func(t *testing.T) {
			got, processed := bucketNumeric(test.raw, test.width, test.offset)
			if got != test.want || !processed {
				t.Fatalf("bucketNumeric(%q, %d, %d) = (%q, %v), want (%q, true)", test.raw, test.width, test.offset, got, processed, test.want)
			}
		})
	}
}

func TestBucketNumericScientificNotation(t *testing.T) {
	tests := []struct {
		raw   string
		width int64
		want  string
	}{
		{raw: "1e3", width: 1000, want: "[1000,2000)"},
		{raw: "-2.4E-2", width: 1, want: "[-1,0)"},
		{raw: "+5e+1", width: 10, want: "[50,60)"},
	}
	for _, test := range tests {
		t.Run(test.raw, func(t *testing.T) {
			got, processed := bucketNumeric(test.raw, test.width, 0)
			if got != test.want || !processed {
				t.Fatalf("bucketNumeric(%q) = (%q, %v), want (%q, true)", test.raw, got, processed, test.want)
			}
		})
	}
}

func TestBucketNumericRejectsInvalidAndResourceLimits(t *testing.T) {
	inputs := []string{
		"NaN", "Inf", "Infinity", "0x1p2", "1_000", "1/2", "1,000", "$12", "12abc",
		strings.Repeat("9", 129), "1e1001", "1e-1001",
	}
	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			got, processed := bucketNumeric(input, 10, 0)
			if got != RedactedFallback || !processed {
				t.Fatalf("bucketNumeric(%q) = (%q, %v), want (%q, true)", input, got, processed, RedactedFallback)
			}
		})
	}
}

func TestBucketNumericInt64OverflowFallback(t *testing.T) {
	for _, input := range []string{"9223372036854775807", "-9223372036854775808", "1e1000"} {
		t.Run(input, func(t *testing.T) {
			got, processed := bucketNumeric(input, 10, 0)
			if got != RedactedFallback || !processed {
				t.Fatalf("bucketNumeric(%q) = (%q, %v), want (%q, true)", input, got, processed, RedactedFallback)
			}
		})
	}
}

func TestTruncateDateLayoutsAndGranularity(t *testing.T) {
	inputs := []string{
		"1990-08-21",
		"1990/08/21",
		"1990.08.21",
		"19900821",
		"1990-08-21 13:45:00",
		"1990-08-21 13:45:00.1",
		"1990-08-21 13:45:00.123456789",
	}
	granularities := []struct {
		gran RangeGranularity
		want string
	}{
		{gran: RangeYear, want: "1990"},
		{gran: RangeQuarter, want: "1990Q3"},
		{gran: RangeMonth, want: "1990-08"},
	}
	for _, input := range inputs {
		for _, granularity := range granularities {
			t.Run(input+"/"+string(granularity.gran), func(t *testing.T) {
				got, processed := truncateDate(input, granularity.gran)
				if got != granularity.want || !processed {
					t.Fatalf("truncateDate(%q, %q) = (%q, %v), want (%q, true)", input, granularity.gran, got, processed, granularity.want)
				}
			})
		}
	}

	quarters := map[string]string{
		"1990-01-01": "1990Q1",
		"1990-04-01": "1990Q2",
		"1990-12-31": "1990Q4",
	}
	for input, want := range quarters {
		got, processed := truncateDate(input, RangeQuarter)
		if got != want || !processed {
			t.Errorf("truncateDate(%q, quarter) = (%q, %v), want (%q, true)", input, got, processed, want)
		}
	}
}

func TestTruncateDatePartialPrecision(t *testing.T) {
	tests := []struct {
		raw  string
		gran RangeGranularity
		want string
	}{
		{raw: "1990-08", gran: RangeYear, want: "1990"},
		{raw: "1990-08", gran: RangeQuarter, want: "1990Q3"},
		{raw: "1990-08", gran: RangeMonth, want: "1990-08"},
		{raw: "1990", gran: RangeYear, want: "1990"},
		{raw: "1990", gran: RangeQuarter, want: RedactedFallback},
		{raw: "1990", gran: RangeMonth, want: RedactedFallback},
	}
	for _, test := range tests {
		t.Run(test.raw+"/"+string(test.gran), func(t *testing.T) {
			got, processed := truncateDate(test.raw, test.gran)
			if got != test.want || !processed {
				t.Fatalf("truncateDate(%q, %q) = (%q, %v), want (%q, true)", test.raw, test.gran, got, processed, test.want)
			}
		})
	}
}

func TestTruncateDateLeapYearAndValidation(t *testing.T) {
	valid, processed := truncateDate("2024-02-29", RangeMonth)
	if valid != "2024-02" || !processed {
		t.Fatalf("valid leap date = (%q, %v), want (%q, true)", valid, processed, "2024-02")
	}
	for _, input := range []string{
		"2026-02-29", "1990-13-01", "1990-00", "0000-01-01", "10000-01-01", "notadate", "31/08/1990",
		"1990-08-21 13:45:00,1", "1990-08-21 13:45:00.1234567890",
	} {
		got, processed := truncateDate(input, RangeYear)
		if got != RedactedFallback || !processed {
			t.Errorf("truncateDate(%q) = (%q, %v), want (%q, true)", input, got, processed, RedactedFallback)
		}
	}
}

func TestTruncateDateRFC3339PreservesOffsetDate(t *testing.T) {
	inputs := []string{"1990-08-21T23:30:00+09:00", "1990-08-21T00:30:00+09:00"}
	wants := map[RangeGranularity]string{
		RangeYear: "1990", RangeQuarter: "1990Q3", RangeMonth: "1990-08",
	}
	for _, input := range inputs {
		for granularity, want := range wants {
			got, processed := truncateDate(input, granularity)
			if got != want || !processed {
				t.Errorf("truncateDate(%q, %q) = (%q, %v), want (%q, true)", input, granularity, got, processed, want)
			}
		}
	}
}

func TestRangeCapabilityMatrix(t *testing.T) {
	types := []struct {
		typeName SensitiveType
		mask     bool
		rangeOK  bool
	}{
		{TypePhone, true, false},
		{TypeEmail, true, false},
		{TypeIDCard, true, false},
		{TypeBankCard, true, false},
		{TypeIP, true, false},
		{TypeBirthDate, true, false},
		{TypeGeneric, false, false},
		{TypeNumber, false, true},
		{TypeDate, false, true},
	}
	width := int64(10)
	for _, test := range types {
		t.Run(string(test.typeName), func(t *testing.T) {
			if got := isMaskSensitiveType(test.typeName); got != test.mask {
				t.Errorf("isMaskSensitiveType(%q) = %v, want %v", test.typeName, got, test.mask)
			}
			if !isKnownSensitiveType(test.typeName) {
				t.Errorf("isKnownSensitiveType(%q) = false, want true", test.typeName)
			}
			if got := isRangeSensitiveType(test.typeName); got != test.rangeOK {
				t.Errorf("isRangeSensitiveType(%q) = %v, want %v", test.typeName, got, test.rangeOK)
			}

			maskErr := ValidateRule(Rule{Column: "value", SensitiveType: test.typeName, Algorithm: AlgoMask})
			if (maskErr == nil) != test.mask {
				t.Errorf("mask validation error = %v, supported = %v", maskErr, test.mask)
			}
			if err := ValidateRule(Rule{Column: "value", SensitiveType: test.typeName, Algorithm: AlgoHash}); err != nil {
				t.Errorf("hash validation error = %v", err)
			}
			if err := ValidateRule(Rule{Column: "value", SensitiveType: test.typeName, Algorithm: AlgoBlock}); err != nil {
				t.Errorf("block validation error = %v", err)
			}
			params := &RangeParams{BucketWidth: &width}
			if test.typeName == TypeDate {
				params = nil
			}
			rangeErr := ValidateRule(Rule{Column: "value", SensitiveType: test.typeName, Algorithm: AlgoRange, Range: params})
			if (rangeErr == nil) != test.rangeOK {
				t.Errorf("range validation error = %v, supported = %v", rangeErr, test.rangeOK)
			}
		})
	}
	unknown := SensitiveType("unknown")
	if isKnownSensitiveType(unknown) || isMaskSensitiveType(unknown) || isRangeSensitiveType(unknown) {
		t.Fatal("unknown type reported as supported")
	}
	if err := ValidateRule(Rule{Column: "value", SensitiveType: TypeNumber, Algorithm: Algorithm("unknown")}); !errors.Is(err, ErrUnsupportedAlgorithm) {
		t.Fatalf("unknown algorithm error = %v, want ErrUnsupportedAlgorithm", err)
	}
}

func TestValidateRangeParamsPresenceAndMutualExclusion(t *testing.T) {
	int64Pointer := func(value int64) *int64 { return &value }
	granularityPointer := func(value RangeGranularity) *RangeGranularity { return &value }
	valid := []Rule{
		{Column: "n", SensitiveType: TypeNumber, Algorithm: AlgoRange, Range: &RangeParams{BucketWidth: int64Pointer(1)}},
		{Column: "n", SensitiveType: TypeNumber, Algorithm: AlgoRange, Range: &RangeParams{BucketWidth: int64Pointer(1_000_000_000), BucketOffset: int64Pointer(-1_000_000_000)}},
		{Column: "n", SensitiveType: TypeNumber, Algorithm: AlgoRange, Range: &RangeParams{BucketWidth: int64Pointer(10), BucketOffset: int64Pointer(1_000_000_000)}},
		{Column: "d", SensitiveType: TypeDate, Algorithm: AlgoRange},
		{Column: "d", SensitiveType: TypeDate, Algorithm: AlgoRange, Range: &RangeParams{}},
		{Column: "d", SensitiveType: TypeDate, Algorithm: AlgoRange, Range: &RangeParams{Granularity: granularityPointer(RangeYear)}},
		{Column: "d", SensitiveType: TypeDate, Algorithm: AlgoRange, Range: &RangeParams{Granularity: granularityPointer(RangeQuarter)}},
		{Column: "d", SensitiveType: TypeDate, Algorithm: AlgoRange, Range: &RangeParams{Granularity: granularityPointer(RangeMonth)}},
	}
	for index, rule := range valid {
		if err := ValidateRule(rule); err != nil {
			t.Errorf("valid rule %d rejected: %v", index, err)
		}
	}

	invalid := []Rule{
		{Column: "n", SensitiveType: TypeNumber, Algorithm: AlgoRange},
		{Column: "n", SensitiveType: TypeNumber, Algorithm: AlgoRange, Range: &RangeParams{}},
		{Column: "n", SensitiveType: TypeNumber, Algorithm: AlgoRange, Range: &RangeParams{BucketWidth: int64Pointer(0)}},
		{Column: "n", SensitiveType: TypeNumber, Algorithm: AlgoRange, Range: &RangeParams{BucketWidth: int64Pointer(1_000_000_001)}},
		{Column: "n", SensitiveType: TypeNumber, Algorithm: AlgoRange, Range: &RangeParams{BucketWidth: int64Pointer(1), BucketOffset: int64Pointer(-1_000_000_001)}},
		{Column: "n", SensitiveType: TypeNumber, Algorithm: AlgoRange, Range: &RangeParams{BucketWidth: int64Pointer(1), BucketOffset: int64Pointer(1_000_000_001)}},
		{Column: "n", SensitiveType: TypeNumber, Algorithm: AlgoRange, Range: &RangeParams{BucketWidth: int64Pointer(1), Granularity: granularityPointer(RangeYear)}},
		{Column: "d", SensitiveType: TypeDate, Algorithm: AlgoRange, Range: &RangeParams{Granularity: granularityPointer("")}},
		{Column: "d", SensitiveType: TypeDate, Algorithm: AlgoRange, Range: &RangeParams{Granularity: granularityPointer("week")}},
		{Column: "d", SensitiveType: TypeDate, Algorithm: AlgoRange, Range: &RangeParams{BucketWidth: int64Pointer(1)}},
		{Column: "d", SensitiveType: TypeDate, Algorithm: AlgoRange, Range: &RangeParams{BucketOffset: int64Pointer(0)}},
		{Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask, Range: &RangeParams{}},
		{Column: "generic", SensitiveType: TypeGeneric, Algorithm: AlgoMask, Range: &RangeParams{}},
		{Column: "generic", SensitiveType: TypeGeneric, Algorithm: AlgoHash, Range: &RangeParams{}},
		{Column: "generic", SensitiveType: TypeGeneric, Algorithm: AlgoBlock, Range: &RangeParams{}},
	}
	for index, rule := range invalid {
		if err := ValidateRule(rule); !errors.Is(err, ErrInvalidRangeParams) {
			t.Errorf("invalid rule %d error = %v, want ErrInvalidRangeParams", index, err)
		}
	}

	if err := ValidateRule(Rule{Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoRange}); !errors.Is(err, ErrUnsupportedType) {
		t.Fatalf("unsupported range type error = %v, want ErrUnsupportedType", err)
	}
}

func TestRangeSentinelsReturnOriginalAndNotProcessed(t *testing.T) {
	inputs := []string{"", "   ", "NULL", " null ", "<nil>", " <NIL> "}
	for _, input := range inputs {
		if got, processed := bucketNumeric(input, 10, 0); got != input || processed {
			t.Errorf("bucketNumeric(%q) = (%q, %v), want original and false", input, got, processed)
		}
		if got, processed := truncateDate(input, RangeYear); got != input || processed {
			t.Errorf("truncateDate(%q) = (%q, %v), want original and false", input, got, processed)
		}
	}
}
