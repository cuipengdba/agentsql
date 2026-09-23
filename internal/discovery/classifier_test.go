package discovery

import (
	"reflect"
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
		{name: "id old 15 digit", category: CategoryIDCard, value: "130503670401001", valid: true},
		{name: "id old invalid date", category: CategoryIDCard, value: "130503671301001"},
		{name: "id checksum invalid", category: CategoryIDCard, value: "110105194912310021"},
		{name: "id birth invalid", category: CategoryIDCard, value: "110105202302300029"},
		{name: "luhn", category: CategoryBankCard, value: "4111111111111111", valid: true},
		{name: "luhn spaces", category: CategoryBankCard, value: "4111 1111 1111 1111", valid: true},
		{name: "luhn hyphens", category: CategoryBankCard, value: "4111-1111-1111-1111", valid: true},
		{name: "luhn other separator rejected", category: CategoryBankCard, value: "4111/1111/1111/1111"},
		{name: "luhn unicode dash rejected", category: CategoryBankCard, value: "4111‑1111‑1111‑1111"},
		{name: "luhn invalid", category: CategoryBankCard, value: "4111111111111112"},
		{name: "luhn too short", category: CategoryBankCard, value: "424242424242"},
		{name: "ipv4", category: CategoryIP, value: "192.0.2.10", valid: true},
		{name: "ipv4 inet prefix", category: CategoryIP, value: "192.0.2.10/32", valid: true},
		{name: "ipv4 cidr prefix", category: CategoryIP, value: "192.0.2.0/24", valid: true},
		{name: "ipv6", category: CategoryIP, value: "2001:db8::1", valid: true},
		{name: "ipv6 loopback", category: CategoryIP, value: "::1", valid: true},
		{name: "ipv6 prefix", category: CategoryIP, value: "2001:db8::1/128", valid: true},
		{name: "ipv4 mapped ipv6", category: CategoryIP, value: "::ffff:192.0.2.10", valid: true},
		{name: "ip zone rejected", category: CategoryIP, value: "fe80::1%eth0"},
		{name: "ip partial", category: CategoryIP, value: "192.0.2.10:80"},
		{name: "ipv6 host port rejected", category: CategoryIP, value: "[2001:db8::1]:443"},
		{name: "birth dash", category: CategoryBirthdate, value: "2000-02-29", valid: true},
		{name: "birth slash", category: CategoryBirthdate, value: "2000/02/29", valid: true},
		{name: "birth compact", category: CategoryBirthdate, value: "20000229", valid: true},
		{name: "birth rfc3339 timestamp", category: CategoryBirthdate, value: "2000-02-29T00:00:00Z", valid: true},
		{name: "birth rfc3339 nano offset", category: CategoryBirthdate, value: "2000-02-29T08:09:10.123456789+08:00", valid: true},
		{name: "birth sql timestamp", category: CategoryBirthdate, value: "2000-02-29 08:09:10", valid: true},
		{name: "birth sql timestamp fraction", category: CategoryBirthdate, value: "2000-02-29 08:09:10.123456", valid: true},
		{name: "birth invalid date", category: CategoryBirthdate, value: "2001-02-29"},
		{name: "birth invalid compact date", category: CategoryBirthdate, value: "20010229"},
		{name: "birth invalid sql timestamp date", category: CategoryBirthdate, value: "2001-02-29 08:09:10"},
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

func TestEnhancedCounterexamplesAndFinalVocabulary(t *testing.T) {
	t.Parallel()
	tests := []struct {
		column   string
		category Category
		strength signalStrength
		matched  bool
	}{
		{column: "date_of_birth", category: CategoryBirthdate, strength: strengthStrong, matched: true},
		{column: "birthdate", category: CategoryBirthdate, strength: strengthStrong, matched: true},
		{column: "email_address", category: CategoryEmail, strength: strengthStrong, matched: true},
		{column: "ip_address", category: CategoryIP, strength: strengthStrong, matched: true},
		{column: "created_at", category: CategoryDate, strength: strengthStrong, matched: true},
		{column: "processing_time_ms"},
		{column: "order_number"},
		{column: "employee_id"},
		{column: "risk_score", category: CategoryNumber, strength: strengthStrong, matched: true},
		{column: "username", category: CategoryGeneric, strength: strengthStrong, matched: true},
		{column: "product_name", category: CategoryGeneric, strength: strengthMedium, matched: true},
		{column: "comment_count", category: CategoryGeneric, strength: strengthMedium, matched: true},
		{column: "total_pages"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.column, func(t *testing.T) {
			t.Parallel()
			candidate, matched := classifyColumnNameGlobal(test.column, true)
			if matched != test.matched {
				t.Fatalf("matched = %v, want %v; candidate = %#v", matched, test.matched, candidate)
			}
			if matched && (candidate.category != test.category || candidate.strength != test.strength) {
				t.Fatalf("candidate = %#v, want category=%q strength=%d", candidate, test.category, test.strength)
			}
		})
	}
}

func TestEnhancedAllowlistFiltersGlobalWinnerWithoutFallback(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		column     string
		categories []Category
	}{
		{name: "birthdate does not fall back to date", column: "date_of_birth", categories: []Category{CategoryDate}},
		{name: "email does not fall back to generic address", column: "email_address", categories: []Category{CategoryGeneric}},
		{name: "ip does not fall back to generic address", column: "ip_address", categories: []Category{CategoryGeneric}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			allowed, _, err := normalizeCategoriesFor(test.categories, true)
			if err != nil {
				t.Fatal(err)
			}
			winner, matched := classifyColumnNameGlobal(test.column, true)
			if !matched {
				t.Fatal("global classifier did not find the expected higher-priority winner")
			}
			if _, filtered := filterColumnCandidate(winner, matched, allowed); filtered {
				t.Fatalf("global winner %q unexpectedly fell back into allowlist %#v", winner.category, test.categories)
			}
		})
	}
}

func TestNamePatternComparatorIsExplicitStrictTotalOrder(t *testing.T) {
	t.Parallel()
	legacyRanks := make(map[int]struct{})
	for left := range columnNamePatterns {
		if !columnNamePatterns[left].enhancedOnly {
			if columnNamePatterns[left].legacyRank < 1 {
				t.Fatalf("legacy pattern %d has no explicit compatibility rank", left)
			}
			if _, duplicate := legacyRanks[columnNamePatterns[left].legacyRank]; duplicate {
				t.Fatalf("duplicate legacy rank %d", columnNamePatterns[left].legacyRank)
			}
			legacyRanks[columnNamePatterns[left].legacyRank] = struct{}{}
		}
		if betterNamePattern(columnNamePatterns[left], columnNamePatterns[left]) {
			t.Fatalf("pattern %d sorts before itself", left)
		}
		for right := range columnNamePatterns {
			if left == right {
				continue
			}
			leftFirst := betterNamePattern(columnNamePatterns[left], columnNamePatterns[right])
			rightFirst := betterNamePattern(columnNamePatterns[right], columnNamePatterns[left])
			if leftFirst == rightFirst {
				t.Fatalf("patterns %d and %d lack a strict order", left, right)
			}
		}
	}

	for first := range columnNamePatterns {
		for second := range columnNamePatterns {
			if !betterNamePattern(columnNamePatterns[first], columnNamePatterns[second]) {
				continue
			}
			for third := range columnNamePatterns {
				if betterNamePattern(columnNamePatterns[second], columnNamePatterns[third]) &&
					!betterNamePattern(columnNamePatterns[first], columnNamePatterns[third]) {
					t.Fatalf("pattern order is not transitive: %d < %d < %d", first, second, third)
				}
			}
		}
	}
}

func TestNamePatternComparatorHonorsDesignedPrecedence(t *testing.T) {
	t.Parallel()
	pattern := func(category Category, strength signalStrength, tokens ...string) namePattern {
		return namePattern{tokens: tokens, category: category, strength: strength, enhancedOnly: true}
	}
	tests := []struct {
		name        string
		preferred   namePattern
		lowerRanked namePattern
	}{
		{
			name:        "dedicated pii beats longer date",
			preferred:   pattern(CategoryBirthdate, strengthMedium, "birth"),
			lowerRanked: pattern(CategoryDate, strengthStrong, "created", "at"),
		},
		{
			name:        "date number tier beats longer generic",
			preferred:   pattern(CategoryNumber, strengthStrong, "score"),
			lowerRanked: pattern(CategoryGeneric, strengthStrong, "full", "name"),
		},
		{
			name:        "longer phrase wins within category",
			preferred:   pattern(CategoryGeneric, strengthStrong, "full", "name"),
			lowerRanked: pattern(CategoryGeneric, strengthMedium, "name"),
		},
		{
			name:        "fixed category preference breaks equal length tie",
			preferred:   pattern(CategoryDate, strengthStrong, "order", "date"),
			lowerRanked: pattern(CategoryNumber, strengthStrong, "total", "amount"),
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if !betterNamePattern(test.preferred, test.lowerRanked) || betterNamePattern(test.lowerRanked, test.preferred) {
				t.Fatalf("precedence not honored: preferred=%#v lower=%#v", test.preferred, test.lowerRanked)
			}
		})
	}
}

func TestEnhancedMixedColumnNamesGolden(t *testing.T) {
	t.Parallel()
	want := map[string]Category{
		"phone_email_address":         CategoryEmail,
		"email_phone_number":          CategoryPhone,
		"birth_date_timestamp":        CategoryBirthdate,
		"created_at_amount":           CategoryDate,
		"total_amount_order_date":     CategoryDate,
		"username_price":              CategoryNumber,
		"ip_address_full_name":        CategoryIP,
		"full_name_description_notes": CategoryGeneric,
	}
	got := make(map[string]Category, len(want))
	for column := range want {
		candidate, matched := classifyColumnNameGlobal(column, true)
		if !matched {
			t.Fatalf("%q did not match", column)
		}
		got[column] = candidate.category
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mixed-name golden = %#v, want %#v", got, want)
	}
}

func TestValidateEnhancedSamples(t *testing.T) {
	t.Parallel()
	reference := time.Date(2026, time.September, 19, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		category Category
		value    string
		valid    bool
	}{
		{name: "integer", category: CategoryNumber, value: "-42", valid: true},
		{name: "decimal", category: CategoryNumber, value: "+42.50", valid: true},
		{name: "currency rejected", category: CategoryNumber, value: "$42.50"},
		{name: "thousands rejected", category: CategoryNumber, value: "1,000"},
		{name: "number text rejected", category: CategoryNumber, value: "42ms"},
		{name: "date dash", category: CategoryDate, value: "2026-09-19", valid: true},
		{name: "date slash", category: CategoryDate, value: "2026/09/19", valid: true},
		{name: "date time", category: CategoryDate, value: "2026-09-19 12:13:14.123", valid: true},
		{name: "rfc3339", category: CategoryDate, value: "2026-09-19T12:13:14+08:00", valid: true},
		{name: "unix seconds rejected", category: CategoryDate, value: "1789776000"},
		{name: "unix millis rejected", category: CategoryDate, value: "1789776000000"},
		{name: "compact numeric date rejected", category: CategoryDate, value: "20260919"},
		{name: "invalid calendar date", category: CategoryDate, value: "2026-02-30"},
		{name: "generic has no format validator", category: CategoryGeneric, value: "private free text"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := ValidateSample(test.category, test.value, reference); got != test.valid {
				t.Fatalf("ValidateSample(%q) = %v, want %v", test.category, got, test.valid)
			}
		})
	}
}

func TestGenericSamplePolicyProducesZeroEvidence(t *testing.T) {
	t.Parallel()
	values := stringPointers("secret-one", "secret-two", "secret-three")
	evidence := evaluateSamples(CategoryGeneric, values, fixedNow())
	if evidence != (sampleEvidence{}) || evidence.supports() || evidence.contradicts() {
		t.Fatalf("generic evidence = %#v, supports=%v contradicts=%v", evidence, evidence.supports(), evidence.contradicts())
	}
	if got := confidenceWithoutSamples(CategoryGeneric, strengthStrong); got != ConfidenceMedium {
		t.Fatalf("strong generic confidence = %q", got)
	}
	if got := confidenceWithoutSamples(CategoryGeneric, strengthMedium); got != ConfidenceLow {
		t.Fatalf("broad generic confidence = %q", got)
	}
}

func TestEveryCategoryDeclaresSamplePolicy(t *testing.T) {
	t.Parallel()
	for _, category := range allCategories {
		want := samplePolicyFormat
		if category == CategoryGeneric {
			want = samplePolicyNone
		}
		if got := samplePolicyFor(category); got != want {
			t.Fatalf("samplePolicyFor(%q) = %d, want %d", category, got, want)
		}
	}
}

func TestEnhancedConfidenceCaps(t *testing.T) {
	t.Parallel()
	supporting := sampleEvidence{eligible: 5, matched: 5}
	for _, category := range []Category{CategoryNumber, CategoryDate} {
		confidence, include := confidenceFor(category, strengthStrong, supporting)
		if !include || confidence != ConfidenceMedium {
			t.Fatalf("%q confidence = %q/%v, want medium/true", category, confidence, include)
		}
	}
	for _, test := range []struct {
		strength signalStrength
		want     Confidence
	}{
		{strength: strengthStrong, want: ConfidenceMedium},
		{strength: strengthMedium, want: ConfidenceLow},
	} {
		confidence, include := confidenceFor(CategoryGeneric, test.strength, sampleEvidence{eligible: 10, matched: 10})
		if !include || confidence != test.want {
			t.Fatalf("generic strength %d confidence = %q/%v, want %q/true", test.strength, confidence, include, test.want)
		}
	}
}

func TestLegacyGateLeavesPublicClassifierAtHeadBehavior(t *testing.T) {
	t.Parallel()
	if discoveryEnhancedEnabled {
		t.Fatal("S1 must leave discoveryEnhancedEnabled false")
	}
	legacyGolden := map[string]Category{
		"customer_phone_number":   CategoryPhone,
		"primaryEmailAddress":     CategoryEmail,
		"legal-id-card":           CategoryIDCard,
		"settlement bank account": CategoryBankCard,
		"clientIPAddress":         CategoryIP,
		"verified_date_of_birth":  CategoryBirthdate,
	}
	for column, want := range legacyGolden {
		got, _, matched, err := ClassifyColumnName(column, nil)
		if err != nil || !matched || got != want {
			t.Fatalf("ClassifyColumnName(%q) = %q, %v, %v; want %q, true, nil", column, got, matched, err, want)
		}
	}
	for _, column := range []string{"birthdate", "username", "created_at", "risk_score", "product_name"} {
		category, signals, matched, err := ClassifyColumnName(column, nil)
		if err != nil || matched || category != "" || len(signals) != 0 {
			t.Fatalf("enhanced column %q leaked through gate: %q %#v %v %v", column, category, signals, matched, err)
		}
	}
	for _, category := range []Category{CategoryNumber, CategoryDate, CategoryGeneric} {
		if _, _, _, err := ClassifyColumnName("phone", []Category{category}); !isError(err, ErrUnknownCategory) {
			t.Fatalf("public category %q error = %v, want ErrUnknownCategory", category, err)
		}
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
