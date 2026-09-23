package discovery

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/mask"
)

func TestAdviseEnhancedNineCategoryMatrix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		category   Category
		strength   signalStrength
		applicable bool
		typeValue  mask.SensitiveType
		algo       mask.Algorithm
		reason     string
	}{
		{category: CategoryPhone, strength: strengthStrong, applicable: true, typeValue: mask.TypePhone, algo: mask.AlgoMask},
		{category: CategoryEmail, strength: strengthStrong, applicable: true, typeValue: mask.TypeEmail, algo: mask.AlgoMask},
		{category: CategoryIDCard, strength: strengthStrong, applicable: true, typeValue: mask.TypeIDCard, algo: mask.AlgoMask},
		{category: CategoryBankCard, strength: strengthStrong, applicable: true, typeValue: mask.TypeBankCard, algo: mask.AlgoMask},
		{category: CategoryIP, strength: strengthStrong, applicable: true, typeValue: mask.TypeIP, algo: mask.AlgoMask},
		{category: CategoryBirthdate, strength: strengthStrong, applicable: true, typeValue: mask.TypeBirthDate, algo: mask.AlgoMask},
		{category: CategoryNumber, strength: strengthStrong, applicable: true, typeValue: mask.TypeNumber, algo: mask.AlgoBlock},
		{category: CategoryDate, strength: strengthStrong, applicable: true, typeValue: mask.TypeDate, algo: mask.AlgoRange},
		{category: CategoryGeneric, strength: strengthStrong, applicable: true, typeValue: mask.TypeGeneric, algo: mask.AlgoBlock},
		{category: CategoryGeneric, strength: strengthMedium, reason: GenericReviewReason},
	}
	for _, test := range tests {
		test := test
		t.Run(string(test.category)+"/"+test.reason, func(t *testing.T) {
			t.Parallel()
			rule, applicable, reason, err := advise(test.category, test.strength, nil, true)
			if err != nil {
				t.Fatalf("advise() error = %v", err)
			}
			if applicable != test.applicable || reason != test.reason {
				t.Fatalf("applicable/reason = %v/%q, want %v/%q", applicable, reason, test.applicable, test.reason)
			}
			if !applicable {
				if rule != nil {
					t.Fatalf("non-applicable advice = %#v", rule)
				}
				return
			}
			if rule == nil || rule.SensitiveType != test.typeValue || rule.Algo != test.algo {
				t.Fatalf("advice = %#v, want type=%q algo=%q", rule, test.typeValue, test.algo)
			}
			if rule.Algo == mask.AlgoHash {
				t.Fatal("automatic advice must never return hash")
			}
			if err := mask.ValidateRule(toMaskRule(*rule)); err != nil {
				t.Fatalf("recommended rule is not runnable: %v", err)
			}
		})
	}
}

func TestCanonicalizerNineCategoryCapabilityMatrix(t *testing.T) {
	t.Parallel()
	width := int64(10)
	tests := []struct {
		category Category
		mask     bool
		hash     bool
		block    bool
		rangeOK  bool
	}{
		{category: CategoryPhone, mask: true, hash: true, block: true},
		{category: CategoryEmail, mask: true, hash: true, block: true},
		{category: CategoryIDCard, mask: true, hash: true, block: true},
		{category: CategoryBankCard, mask: true, hash: true, block: true},
		{category: CategoryIP, mask: true, hash: true, block: true},
		{category: CategoryBirthdate, mask: true, hash: true, block: true},
		{category: CategoryNumber, hash: true, block: true, rangeOK: true},
		{category: CategoryDate, hash: true, block: true, rangeOK: true},
		{category: CategoryGeneric, hash: true, block: true},
	}
	for _, test := range tests {
		test := test
		t.Run(string(test.category), func(t *testing.T) {
			t.Parallel()
			sensitiveType, _ := sensitiveTypeForCategory(test.category)
			for _, algorithm := range []struct {
				value mask.Algorithm
				want  bool
			}{
				{value: mask.AlgoMask, want: test.mask},
				{value: mask.AlgoHash, want: test.hash},
				{value: mask.AlgoBlock, want: test.block},
				{value: mask.AlgoRange, want: test.rangeOK},
			} {
				rule := RecommendedRule{SensitiveType: sensitiveType, Algo: algorithm.value}
				if algorithm.value == mask.AlgoRange {
					switch test.category {
					case CategoryNumber:
						rule.Range = &RangeHint{BucketWidth: &width}
					case CategoryDate:
						rule.Range = &RangeHint{Granularity: "month"}
					}
				}
				canonical, err := CanonicalizeRule(test.category, rule)
				if (err == nil) != algorithm.want {
					t.Errorf("%q capability error = %v, want supported=%v", algorithm.value, err, algorithm.want)
				}
				if err == nil {
					if validateErr := mask.ValidateRule(toMaskRule(canonical)); validateErr != nil {
						t.Errorf("%q canonical rule failed mask validation: %v", algorithm.value, validateErr)
					}
				}
			}
		})
	}
}

func TestNumberDefaultsToBlockAndRangeRequiresExplicitValidWidth(t *testing.T) {
	t.Parallel()
	rule, applicable, _, err := advise(CategoryNumber, strengthStrong, nil, true)
	if err != nil || !applicable || rule == nil || rule.Algo != mask.AlgoBlock || rule.Range != nil {
		t.Fatalf("default number advice = %#v, %v, %v", rule, applicable, err)
	}

	width := int64(25)
	rule, applicable, _, err = advise(CategoryNumber, strengthStrong, &RangeHint{BucketWidth: &width}, true)
	if err != nil || !applicable || rule == nil || rule.Algo != mask.AlgoRange ||
		rule.Range == nil || rule.Range.BucketWidth == nil || *rule.Range.BucketWidth != width ||
		rule.Range.BucketOffset != nil || rule.Range.Granularity != "" {
		t.Fatalf("explicit number range advice = %#v, %v, %v", rule, applicable, err)
	}
	if err := validateApplicable(CategoryNumber, *rule, true); err != nil {
		t.Fatalf("explicit number range ValidateApplicable() error = %v", err)
	}

	for _, hint := range []*RangeHint{
		{},
		{BucketWidth: int64PointerCopy(0)},
		{BucketWidth: int64PointerCopy(-1)},
		{BucketWidth: int64PointerCopy(maxRangeMagnitude + 1)},
		{BucketWidth: int64PointerCopy(1), Granularity: "month"},
	} {
		if _, _, _, err := advise(CategoryNumber, strengthStrong, hint, true); !isError(err, ErrNotApplicable) {
			t.Fatalf("invalid number hint %#v error = %v", hint, err)
		}
	}
}

func TestDateAdvicePersistsExplicitMonth(t *testing.T) {
	t.Parallel()
	rule, applicable, _, err := advise(CategoryDate, strengthStrong, nil, true)
	if err != nil || !applicable || rule == nil || rule.Algo != mask.AlgoRange || rule.Range == nil ||
		rule.Range.Granularity != "month" || rule.Range.BucketWidth != nil || rule.Range.BucketOffset != nil {
		t.Fatalf("date advice = %#v, %v, %v", rule, applicable, err)
	}
	encoded, err := json.Marshal(rule)
	if err != nil {
		t.Fatal(err)
	}
	jsonValue := string(encoded)
	if !strings.Contains(jsonValue, `"granularity":"month"`) ||
		strings.Contains(jsonValue, "bucket_width") || strings.Contains(jsonValue, "bucket_offset") {
		t.Fatalf("date advice JSON does not preserve exact month semantics: %s", jsonValue)
	}
}

func TestCanonicalizeRuleEquivalentFormsAndCopiesPointers(t *testing.T) {
	t.Parallel()
	width, zero := int64(10), int64(0)
	withoutOffset, err := CanonicalizeRule(CategoryNumber, RecommendedRule{
		SensitiveType: mask.TypeNumber,
		Algo:          mask.AlgoRange,
		Range:         &RangeHint{BucketWidth: &width},
	})
	if err != nil {
		t.Fatal(err)
	}
	withZeroOffset, err := CanonicalizeRule(CategoryNumber, RecommendedRule{
		SensitiveType: mask.TypeNumber,
		Algo:          mask.AlgoRange,
		Range:         &RangeHint{BucketWidth: &width, BucketOffset: &zero},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sameRecommendedRule(withoutOffset, withZeroOffset) || withoutOffset.Range.BucketOffset != nil {
		t.Fatalf("zero/missing offset did not canonicalize equally: %#v %#v", withoutOffset, withZeroOffset)
	}
	width = 99
	if *withoutOffset.Range.BucketWidth != 10 {
		t.Fatal("canonicalizer retained caller width pointer")
	}

	dateMissing, err := CanonicalizeRule(CategoryDate, RecommendedRule{SensitiveType: mask.TypeDate, Algo: mask.AlgoRange})
	if err != nil {
		t.Fatal(err)
	}
	dateMonth, err := CanonicalizeRule(CategoryDate, RecommendedRule{
		SensitiveType: mask.TypeDate, Algo: mask.AlgoRange, Range: &RangeHint{Granularity: "month"},
	})
	if err != nil || !sameRecommendedRule(dateMissing, dateMonth) || dateMissing.Range == nil || dateMissing.Range.Granularity != "month" {
		t.Fatalf("date forms did not canonicalize to month: %#v %#v %v", dateMissing, dateMonth, err)
	}

	emptyRangeBlock, err := CanonicalizeRule(CategoryGeneric, RecommendedRule{
		SensitiveType: mask.TypeGeneric, Algo: mask.AlgoBlock, Range: &RangeHint{},
	})
	if err != nil || emptyRangeBlock.Range != nil {
		t.Fatalf("empty non-range fields did not canonicalize away: %#v %v", emptyRangeBlock, err)
	}
}

func TestCanonicalizeRuleRejectsInvalidShapes(t *testing.T) {
	t.Parallel()
	width, offset := int64(10), int64(2)
	tests := []struct {
		name     string
		category Category
		rule     RecommendedRule
	}{
		{name: "type mismatch", category: CategoryPhone, rule: RecommendedRule{SensitiveType: mask.TypeEmail, Algo: mask.AlgoMask}},
		{name: "number range missing width", category: CategoryNumber, rule: RecommendedRule{SensitiveType: mask.TypeNumber, Algo: mask.AlgoRange}},
		{name: "number range granularity", category: CategoryNumber, rule: RecommendedRule{SensitiveType: mask.TypeNumber, Algo: mask.AlgoRange, Range: &RangeHint{BucketWidth: &width, Granularity: "month"}}},
		{name: "date width", category: CategoryDate, rule: RecommendedRule{SensitiveType: mask.TypeDate, Algo: mask.AlgoRange, Range: &RangeHint{BucketWidth: &width}}},
		{name: "date offset", category: CategoryDate, rule: RecommendedRule{SensitiveType: mask.TypeDate, Algo: mask.AlgoRange, Range: &RangeHint{BucketOffset: &offset}}},
		{name: "date wrong granularity", category: CategoryDate, rule: RecommendedRule{SensitiveType: mask.TypeDate, Algo: mask.AlgoRange, Range: &RangeHint{Granularity: "year"}}},
		{name: "mask with range", category: CategoryPhone, rule: RecommendedRule{SensitiveType: mask.TypePhone, Algo: mask.AlgoMask, Range: &RangeHint{BucketWidth: &width}}},
		{name: "hash with range", category: CategoryGeneric, rule: RecommendedRule{SensitiveType: mask.TypeGeneric, Algo: mask.AlgoHash, Range: &RangeHint{Granularity: "month"}}},
		{name: "block with range", category: CategoryNumber, rule: RecommendedRule{SensitiveType: mask.TypeNumber, Algo: mask.AlgoBlock, Range: &RangeHint{BucketOffset: &offset}}},
		{name: "unsupported combination", category: CategoryGeneric, rule: RecommendedRule{SensitiveType: mask.TypeGeneric, Algo: mask.AlgoMask}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := CanonicalizeRule(test.category, test.rule); !isError(err, ErrNotApplicable) {
				t.Fatalf("error = %v, want ErrNotApplicable", err)
			}
		})
	}
}

func TestCanonicalizerAllowsHashCapabilityButAutomaticAdviceNeverUsesIt(t *testing.T) {
	t.Parallel()
	for _, category := range allCategories {
		sensitiveType, _ := sensitiveTypeForCategory(category)
		canonical, err := CanonicalizeRule(category, RecommendedRule{SensitiveType: sensitiveType, Algo: mask.AlgoHash})
		if err != nil || canonical.Algo != mask.AlgoHash || canonical.Range != nil {
			t.Fatalf("hash capability for %q = %#v, %v", category, canonical, err)
		}
		auto, applicable, _, err := advise(category, strengthStrong, nil, true)
		if err != nil || !applicable || auto == nil || auto.Algo == mask.AlgoHash {
			t.Fatalf("automatic advice for %q = %#v, %v, %v", category, auto, applicable, err)
		}
	}
}

func TestEnabledGateAdvisePreservesSixMaskDefaultsAndAddsEnhancedDefaults(t *testing.T) {
	t.Parallel()
	for _, category := range legacyCategories {
		rule, applicable, reason, err := Advise(category)
		if err != nil || !applicable || reason != "" || rule == nil || rule.Algo != mask.AlgoMask || rule.Range != nil {
			t.Fatalf("Advise(%q) = %#v, %v, %q, %v", category, rule, applicable, reason, err)
		}
		if err := ValidateApplicable(category, *rule); err != nil {
			t.Fatalf("ValidateApplicable(%q) error = %v", category, err)
		}
	}
	wants := map[Category]mask.Algorithm{
		CategoryNumber:  mask.AlgoBlock,
		CategoryDate:    mask.AlgoRange,
		CategoryGeneric: mask.AlgoBlock,
	}
	for category, want := range wants {
		rule, applicable, reason, err := Advise(category)
		if err != nil || !applicable || reason != "" || rule == nil || rule.Algo != want {
			t.Fatalf("Advise(%q) = %#v %v %q %v; want algo %q", category, rule, applicable, reason, err, want)
		}
	}
	for _, rule := range []RecommendedRule{
		{SensitiveType: mask.TypeEmail, Algo: mask.AlgoMask},
		{SensitiveType: mask.TypePhone, Algo: mask.AlgoHash},
		{SensitiveType: mask.TypePhone, Algo: mask.AlgoBlock},
	} {
		if err := ValidateApplicable(CategoryPhone, rule); !isError(err, ErrNotApplicable) {
			t.Fatalf("ValidateApplicable mismatch error = %v, want ErrNotApplicable", err)
		}
	}
	if _, _, _, err := Advise("future"); !isError(err, ErrUnknownCategory) {
		t.Fatalf("error = %v, want ErrUnknownCategory", err)
	}
}
