package discovery

import "github.com/cuipengdba/agentsql/internal/mask"

const ManualDispositionReason = "当前版本无可用脱敏算法，请人工处置"

// Advise maps only combinations that are actually executable by the current
// mask package. It never uses reserved sensitive types or algorithms.
func Advise(category Category) (*RecommendedRule, bool, string, error) {
	var sensitiveType mask.SensitiveType
	switch category {
	case CategoryPhone:
		sensitiveType = mask.TypePhone
	case CategoryEmail:
		sensitiveType = mask.TypeEmail
	case CategoryIDCard:
		sensitiveType = mask.TypeIDCard
	case CategoryBankCard:
		sensitiveType = mask.TypeBankCard
	case CategoryIP:
		sensitiveType = mask.TypeIP
	case CategoryBirthdate:
		sensitiveType = mask.TypeBirthDate
	default:
		return nil, false, "", classified(CodeUnknownCategory, ErrUnknownCategory)
	}
	return &RecommendedRule{SensitiveType: sensitiveType, Algo: mask.AlgoMask}, true, "", nil
}

// ValidateApplicable rejects any category/rule pair that cannot run today.
func ValidateApplicable(category Category, rule RecommendedRule) error {
	recommended, applicable, _, err := Advise(category)
	if err != nil {
		return err
	}
	if !applicable || recommended == nil || rule != *recommended {
		return classified(CodeNotApplicable, ErrNotApplicable)
	}
	return nil
}
