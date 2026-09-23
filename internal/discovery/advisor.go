package discovery

import "github.com/cuipengdba/agentsql/internal/mask"

const (
	ManualDispositionReason = "当前版本无可用脱敏算法，请人工处置"
	GenericReviewReason     = "可能含敏感自由文本，请人工判断后选择 block/hash"

	maxRangeMagnitude int64 = 1_000_000_000
)

// Advise returns the automatic recommendation for a category. A number range
// is returned only when the caller explicitly supplies one valid width hint;
// otherwise number defaults to block. Automatic advice never returns hash.
func Advise(category Category, rangeHints ...*RangeHint) (*RecommendedRule, bool, string, error) {
	if len(rangeHints) > 1 {
		return nil, false, "", classified(CodeNotApplicable, ErrNotApplicable)
	}
	var hint *RangeHint
	if len(rangeHints) == 1 {
		hint = rangeHints[0]
	}
	return advise(category, strengthStrong, hint, discoveryEnhancedEnabled)
}

func advise(
	category Category,
	strength signalStrength,
	hint *RangeHint,
	enhanced bool,
) (*RecommendedRule, bool, string, error) {
	if !isKnownCategoryFor(category, enhanced) {
		return nil, false, "", classified(CodeUnknownCategory, ErrUnknownCategory)
	}
	if category == CategoryGeneric && strength != strengthStrong {
		return nil, false, GenericReviewReason, nil
	}

	sensitiveType, ok := sensitiveTypeForCategory(category)
	if !ok {
		return nil, false, "", classified(CodeUnknownCategory, ErrUnknownCategory)
	}
	rule := RecommendedRule{SensitiveType: sensitiveType, Algo: mask.AlgoMask}
	if enhanced {
		switch category {
		case CategoryNumber:
			rule.Algo = mask.AlgoBlock
			if hint != nil {
				rule.Algo = mask.AlgoRange
				rule.Range = hint
			}
		case CategoryDate:
			rule.Algo = mask.AlgoRange
			if hint == nil {
				rule.Range = &RangeHint{Granularity: string(mask.RangeMonth)}
			} else {
				rule.Range = hint
			}
		case CategoryGeneric:
			rule.Algo = mask.AlgoBlock
		}
	} else if hint != nil {
		return nil, false, "", classified(CodeNotApplicable, ErrNotApplicable)
	}

	canonical, err := CanonicalizeRule(category, rule)
	if err != nil {
		return nil, false, "", err
	}
	return &canonical, true, "", nil
}

// CanonicalizeRule is the sole discovery range canonicalizer. It validates a
// category-aligned rule, normalizes semantically equivalent empty/zero forms,
// and returns fresh pointer fields that callers may safely retain.
func CanonicalizeRule(category Category, rule RecommendedRule) (RecommendedRule, error) {
	expectedType, ok := sensitiveTypeForCategory(category)
	if !ok || rule.SensitiveType != expectedType {
		return RecommendedRule{}, classified(CodeNotApplicable, ErrNotApplicable)
	}

	canonical := RecommendedRule{SensitiveType: rule.SensitiveType, Algo: rule.Algo}
	switch rule.Algo {
	case mask.AlgoMask, mask.AlgoHash, mask.AlgoBlock:
		if hasRangeFields(rule.Range) {
			return RecommendedRule{}, classified(CodeNotApplicable, ErrNotApplicable)
		}
	case mask.AlgoRange:
		switch category {
		case CategoryNumber:
			if rule.Range == nil || rule.Range.BucketWidth == nil {
				return RecommendedRule{}, classified(CodeNotApplicable, ErrNotApplicable)
			}
			width := *rule.Range.BucketWidth
			if width < 1 || width > maxRangeMagnitude || rule.Range.Granularity != "" {
				return RecommendedRule{}, classified(CodeNotApplicable, ErrNotApplicable)
			}
			canonical.Range = &RangeHint{BucketWidth: int64PointerCopy(width)}
			if rule.Range.BucketOffset != nil {
				offset := *rule.Range.BucketOffset
				if offset < -maxRangeMagnitude || offset > maxRangeMagnitude {
					return RecommendedRule{}, classified(CodeNotApplicable, ErrNotApplicable)
				}
				if offset != 0 {
					canonical.Range.BucketOffset = int64PointerCopy(offset)
				}
			}
		case CategoryDate:
			if rule.Range != nil && (rule.Range.BucketWidth != nil || rule.Range.BucketOffset != nil) {
				return RecommendedRule{}, classified(CodeNotApplicable, ErrNotApplicable)
			}
			granularity := ""
			if rule.Range != nil {
				granularity = rule.Range.Granularity
			}
			if granularity != "" && granularity != string(mask.RangeMonth) {
				return RecommendedRule{}, classified(CodeNotApplicable, ErrNotApplicable)
			}
			canonical.Range = &RangeHint{Granularity: string(mask.RangeMonth)}
		default:
			return RecommendedRule{}, classified(CodeNotApplicable, ErrNotApplicable)
		}
	default:
		return RecommendedRule{}, classified(CodeNotApplicable, ErrNotApplicable)
	}

	if err := mask.ValidateRule(toMaskRule(canonical)); err != nil {
		return RecommendedRule{}, classified(CodeNotApplicable, ErrNotApplicable)
	}
	return canonical, nil
}

// ValidateApplicable checks the automatic-advice contract. For number, a
// range rule is applicable only when its explicit width survives canonicalization.
func ValidateApplicable(category Category, rule RecommendedRule) error {
	return validateApplicable(category, rule, discoveryEnhancedEnabled)
}

func validateApplicable(category Category, rule RecommendedRule, enhanced bool) error {
	canonical, err := CanonicalizeRule(category, rule)
	if err != nil || !isKnownCategoryFor(category, enhanced) {
		return classified(CodeNotApplicable, ErrNotApplicable)
	}
	var hint *RangeHint
	if enhanced && category == CategoryNumber && canonical.Algo == mask.AlgoRange {
		hint = canonical.Range
	}
	recommended, applicable, _, err := advise(category, strengthStrong, hint, enhanced)
	if err != nil || !applicable || recommended == nil || !sameRecommendedRule(canonical, *recommended) {
		return classified(CodeNotApplicable, ErrNotApplicable)
	}
	return nil
}

func sensitiveTypeForCategory(category Category) (mask.SensitiveType, bool) {
	switch category {
	case CategoryPhone:
		return mask.TypePhone, true
	case CategoryEmail:
		return mask.TypeEmail, true
	case CategoryIDCard:
		return mask.TypeIDCard, true
	case CategoryBankCard:
		return mask.TypeBankCard, true
	case CategoryIP:
		return mask.TypeIP, true
	case CategoryBirthdate:
		return mask.TypeBirthDate, true
	case CategoryNumber:
		return mask.TypeNumber, true
	case CategoryDate:
		return mask.TypeDate, true
	case CategoryGeneric:
		return mask.TypeGeneric, true
	default:
		return "", false
	}
}

func hasRangeFields(hint *RangeHint) bool {
	return hint != nil && (hint.BucketWidth != nil || hint.BucketOffset != nil || hint.Granularity != "")
}

func sameRecommendedRule(left, right RecommendedRule) bool {
	if left.SensitiveType != right.SensitiveType || left.Algo != right.Algo {
		return false
	}
	if left.Range == nil || right.Range == nil {
		return left.Range == nil && right.Range == nil
	}
	return sameInt64Pointer(left.Range.BucketWidth, right.Range.BucketWidth) &&
		sameInt64Pointer(left.Range.BucketOffset, right.Range.BucketOffset) &&
		left.Range.Granularity == right.Range.Granularity
}

func sameInt64Pointer(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func int64PointerCopy(value int64) *int64 {
	copy := value
	return &copy
}

func toMaskRule(rule RecommendedRule) mask.Rule {
	converted := mask.Rule{
		Column:        "value",
		SensitiveType: rule.SensitiveType,
		Algorithm:     rule.Algo,
	}
	if rule.Range == nil {
		return converted
	}
	converted.Range = &mask.RangeParams{
		BucketWidth:  rule.Range.BucketWidth,
		BucketOffset: rule.Range.BucketOffset,
	}
	if rule.Range.Granularity != "" {
		granularity := mask.RangeGranularity(rule.Range.Granularity)
		converted.Range.Granularity = &granularity
	}
	return converted
}
