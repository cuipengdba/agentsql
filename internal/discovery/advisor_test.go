package discovery

import (
	"testing"

	"github.com/cuipengdba/agentsql/internal/mask"
)

func TestAdviseOnlyRunnableRulesAreApplicable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		category   Category
		applicable bool
		typeValue  mask.SensitiveType
		algo       mask.Algorithm
	}{
		{category: CategoryPhone, applicable: true, typeValue: mask.TypePhone, algo: mask.AlgoMask},
		{category: CategoryEmail, applicable: true, typeValue: mask.TypeEmail, algo: mask.AlgoMask},
		{category: CategoryIDCard, applicable: true, typeValue: mask.TypeIDCard, algo: mask.AlgoMask},
		{category: CategoryBankCard, applicable: true, typeValue: mask.TypeBankCard, algo: mask.AlgoMask},
		{category: CategoryIP, applicable: true, typeValue: mask.TypeIP, algo: mask.AlgoMask},
		{category: CategoryBirthdate, applicable: true, typeValue: mask.TypeBirthDate, algo: mask.AlgoMask},
	}
	for _, test := range tests {
		test := test
		t.Run(string(test.category), func(t *testing.T) {
			t.Parallel()
			rule, applicable, reason, err := Advise(test.category)
			if err != nil {
				t.Fatalf("Advise() error = %v", err)
			}
			if applicable != test.applicable {
				t.Fatalf("applicable = %v, want %v", applicable, test.applicable)
			}
			if !applicable {
				if rule != nil || reason != ManualDispositionReason {
					t.Fatalf("unsupported advice = %#v, %q", rule, reason)
				}
				return
			}
			if rule == nil || rule.SensitiveType != test.typeValue || rule.Algo != test.algo || reason != "" {
				t.Fatalf("advice = %#v, %q", rule, reason)
			}
			if _, err := mask.NewRedactor([]mask.Rule{{
				Column:        "value",
				SensitiveType: rule.SensitiveType,
				Algorithm:     rule.Algo,
			}}); err != nil {
				t.Fatalf("recommended rule is not runnable: %v", err)
			}
		})
	}
}

func TestValidateApplicableAcceptsSixAndRejectsMismatchAndUnknown(t *testing.T) {
	t.Parallel()
	for _, category := range allCategories {
		rule, _, _, err := Advise(category)
		if err != nil || rule == nil {
			t.Fatalf("Advise(%q) = %#v, %v", category, rule, err)
		}
		if err := ValidateApplicable(category, *rule); err != nil {
			t.Fatalf("ValidateApplicable(%q) error = %v", category, err)
		}
	}
	for _, rule := range []RecommendedRule{
		{SensitiveType: mask.TypeEmail, Algo: mask.AlgoMask},
		{SensitiveType: mask.TypePhone, Algo: mask.AlgoHash},
	} {
		if err := ValidateApplicable(CategoryPhone, rule); !isError(err, ErrNotApplicable) {
			t.Fatalf("ValidateApplicable mismatch error = %v, want ErrNotApplicable", err)
		}
	}
	if _, _, _, err := Advise("future"); !isError(err, ErrUnknownCategory) {
		t.Fatalf("error = %v, want ErrUnknownCategory", err)
	}
}
