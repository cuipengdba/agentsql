package discovery

import (
	"testing"
	"time"
)

func TestClassifyColumnName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		column     string
		category   Category
		candidate  bool
		signalName string
	}{
		{name: "snake phone phrase", column: "customer_phone_number", category: CategoryPhone, candidate: true, signalName: "column_name_strong_phone"},
		{name: "camel email phrase", column: "primaryEmailAddress", category: CategoryEmail, candidate: true, signalName: "column_name_strong_email"},
		{name: "kebab id card", column: "legal-id-card", category: CategoryIDCard, candidate: true, signalName: "column_name_strong_idcard"},
		{name: "space bank account", column: "settlement bank account", category: CategoryBankCard, candidate: true, signalName: "column_name_strong_bankcard"},
		{name: "camel acronym ip", column: "clientIPAddress", category: CategoryIP, candidate: true, signalName: "column_name_strong_ip"},
		{name: "long birth phrase", column: "verified_date_of_birth", category: CategoryBirthdate, candidate: true, signalName: "column_name_strong_birthdate"},
		{name: "medium tel", column: "contact_tel", category: CategoryPhone, candidate: true, signalName: "column_name_medium_phone"},
		{name: "medium card no", column: "payment_card_no", category: CategoryBankCard, candidate: true, signalName: "column_name_medium_bankcard"},
		{name: "username excluded", column: "username"},
		{name: "bare name excluded", column: "name"},
		{name: "bare address excluded", column: "address"},
		{name: "substring not matched", column: "smartphone"},
		{name: "description excluded", column: "description"},
		{name: "remark excluded", column: "remark"},
		{name: "created at excluded", column: "created_at"},
		{name: "uuid excluded", column: "uuid"},
		{name: "price excluded", column: "price"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			category, signals, candidate, err := ClassifyColumnName(test.column, nil)
			if err != nil {
				t.Fatalf("ClassifyColumnName() error = %v", err)
			}
			if candidate != test.candidate {
				t.Fatalf("candidate = %v, want %v", candidate, test.candidate)
			}
			if !candidate {
				if category != "" || len(signals) != 0 {
					t.Fatalf("non-candidate returned category/signals: %q %#v", category, signals)
				}
				return
			}
			if category != test.category {
				t.Fatalf("category = %q, want %q", category, test.category)
			}
			if len(signals) != 1 || signals[0].Name != test.signalName || signals[0].Count != 1 {
				t.Fatalf("signals = %#v, want %q count 1", signals, test.signalName)
			}
		})
	}
}

func TestClassifyColumnNameCategoryFilterAndUnknown(t *testing.T) {
	t.Parallel()
	_, _, candidate, err := ClassifyColumnName("email_address", []Category{CategoryPhone})
	if err != nil || candidate {
		t.Fatalf("filtered email candidate = %v, error = %v", candidate, err)
	}
	_, _, _, err = ClassifyColumnName("phone", []Category{"future"})
	if !isError(err, ErrUnknownCategory) {
		t.Fatalf("error = %v, want ErrUnknownCategory", err)
	}
}

func TestValidateSample(t *testing.T) {
	t.Parallel()
	reference := time.Date(2026, time.September, 19, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		category Category
		value    string
		valid    bool
	}{
		{name: "phone", category: CategoryPhone, value: "13800138000", valid: true},
		{name: "phone country code", category: CategoryPhone, value: "+8613800138000", valid: true},
		{name: "phone invalid segment", category: CategoryPhone, value: "12800138000"},
		{name: "phone separators rejected", category: CategoryPhone, value: "138-0013-8000"},
		{name: "email", category: CategoryEmail, value: "user.name+tag@example.com", valid: true},
		{name: "email full match", category: CategoryEmail, value: "User <user@example.com>"},
		{name: "email consecutive dots", category: CategoryEmail, value: "user..name@example.com"},
		{name: "email leading dot", category: CategoryEmail, value: ".user@example.com"},
		{name: "id checksum", category: CategoryIDCard, value: "11010519491231002X", valid: true},
		{name: "id checksum invalid", category: CategoryIDCard, value: "110105194912310021"},
		{name: "id birth invalid", category: CategoryIDCard, value: "110105202302300029"},
		{name: "luhn", category: CategoryBankCard, value: "4111111111111111", valid: true},
		{name: "luhn invalid", category: CategoryBankCard, value: "4111111111111112"},
		{name: "luhn too short", category: CategoryBankCard, value: "424242424242"},
		{name: "ipv4", category: CategoryIP, value: "192.0.2.10", valid: true},
		{name: "ipv6", category: CategoryIP, value: "2001:db8::1", valid: true},
		{name: "ip partial", category: CategoryIP, value: "192.0.2.10:80"},
		{name: "birth dash", category: CategoryBirthdate, value: "2000-02-29", valid: true},
		{name: "birth slash", category: CategoryBirthdate, value: "2000/02/29", valid: true},
		{name: "birth compact", category: CategoryBirthdate, value: "20000229", valid: true},
		{name: "birth timestamp rejected", category: CategoryBirthdate, value: "2000-02-29T00:00:00Z"},
		{name: "birth invalid date", category: CategoryBirthdate, value: "2001-02-29"},
		{name: "birth over 120", category: CategoryBirthdate, value: "1900-01-01"},
		{name: "birth future", category: CategoryBirthdate, value: "2027-01-01"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := ValidateSample(test.category, test.value, reference); got != test.valid {
				t.Fatalf("ValidateSample(%q, %q) = %v, want %v", test.category, test.value, got, test.valid)
			}
		})
	}
}

func TestEvaluateSamplesExcludesNullAndBlank(t *testing.T) {
	t.Parallel()
	blank := "   "
	valid := "13800138000"
	invalid := "product-code"
	evidence := evaluateSamples(CategoryPhone, []*string{nil, &blank, &valid, &invalid}, fixedNow())
	if evidence.eligible != 2 || evidence.matched != 1 {
		t.Fatalf("evidence = %#v, want eligible=2 matched=1", evidence)
	}
}

func FuzzClassifyColumnNameNoSubstringPanics(f *testing.F) {
	for _, seed := range []string{"phone", "emailAddress", "ip_address", "username", "", "电话📞"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, name string) {
		category, signals, candidate, err := ClassifyColumnName(name, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !candidate && (category != "" || len(signals) != 0) {
			t.Fatalf("non-candidate leaked classification: %q %#v", category, signals)
		}
	})
}
