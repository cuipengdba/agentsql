package discovery

import (
	"net/netip"
	"regexp"
	"strings"
	"time"
	"unicode"
)

type signalStrength uint8

const (
	strengthMedium signalStrength = iota + 1
	strengthStrong
)

type columnCandidate struct {
	meta     ColumnMeta
	category Category
	strength signalStrength
	signal   Signal
}

type namePattern struct {
	tokens       []string
	category     Category
	strength     signalStrength
	enhancedOnly bool
	legacyRank   int
}

// Registration order is never precedence. betterNamePattern defines the
// enhanced full order; legacyRank explicitly freezes the pre-S1 order while
// the rollout gate is disabled.
var columnNamePatterns = []namePattern{
	{tokens: []string{"date", "of", "birth"}, category: CategoryBirthdate, strength: strengthStrong, legacyRank: 1},
	{tokens: []string{"identity", "number"}, category: CategoryIDCard, strength: strengthStrong, legacyRank: 2},
	{tokens: []string{"phone", "number"}, category: CategoryPhone, strength: strengthStrong, legacyRank: 3},
	{tokens: []string{"email", "address"}, category: CategoryEmail, strength: strengthStrong, legacyRank: 4},
	{tokens: []string{"id", "card"}, category: CategoryIDCard, strength: strengthStrong, legacyRank: 5},
	{tokens: []string{"bank", "card"}, category: CategoryBankCard, strength: strengthStrong, legacyRank: 6},
	{tokens: []string{"bank", "account"}, category: CategoryBankCard, strength: strengthStrong, legacyRank: 7},
	{tokens: []string{"ip", "address"}, category: CategoryIP, strength: strengthStrong, legacyRank: 8},
	{tokens: []string{"client", "ip"}, category: CategoryIP, strength: strengthStrong, legacyRank: 9},
	{tokens: []string{"birth", "date"}, category: CategoryBirthdate, strength: strengthStrong, legacyRank: 10},
	{tokens: []string{"card", "no"}, category: CategoryBankCard, strength: strengthMedium, legacyRank: 11},
	{tokens: []string{"mobile"}, category: CategoryPhone, strength: strengthStrong, legacyRank: 12},
	{tokens: []string{"phone"}, category: CategoryPhone, strength: strengthStrong, legacyRank: 13},
	{tokens: []string{"email"}, category: CategoryEmail, strength: strengthStrong, legacyRank: 14},
	{tokens: []string{"idcard"}, category: CategoryIDCard, strength: strengthStrong, legacyRank: 15},
	{tokens: []string{"ip"}, category: CategoryIP, strength: strengthStrong, legacyRank: 16},
	{tokens: []string{"dob"}, category: CategoryBirthdate, strength: strengthStrong, legacyRank: 17},
	{tokens: []string{"birthday"}, category: CategoryBirthdate, strength: strengthStrong, legacyRank: 18},
	{tokens: []string{"tel"}, category: CategoryPhone, strength: strengthMedium, legacyRank: 19},
	{tokens: []string{"mail"}, category: CategoryEmail, strength: strengthMedium, legacyRank: 20},
	{tokens: []string{"identity"}, category: CategoryIDCard, strength: strengthMedium, legacyRank: 21},
	{tokens: []string{"birth"}, category: CategoryBirthdate, strength: strengthMedium, legacyRank: 22},

	{tokens: []string{"birthdate"}, category: CategoryBirthdate, strength: strengthStrong, enhancedOnly: true},

	{tokens: []string{"created", "at"}, category: CategoryDate, strength: strengthStrong, enhancedOnly: true},
	{tokens: []string{"updated", "at"}, category: CategoryDate, strength: strengthStrong, enhancedOnly: true},
	{tokens: []string{"create", "time"}, category: CategoryDate, strength: strengthStrong, enhancedOnly: true},
	{tokens: []string{"update", "time"}, category: CategoryDate, strength: strengthStrong, enhancedOnly: true},
	{tokens: []string{"order", "date"}, category: CategoryDate, strength: strengthStrong, enhancedOnly: true},
	{tokens: []string{"ordered", "at"}, category: CategoryDate, strength: strengthStrong, enhancedOnly: true},
	{tokens: []string{"event", "time"}, category: CategoryDate, strength: strengthStrong, enhancedOnly: true},
	{tokens: []string{"date", "time"}, category: CategoryDate, strength: strengthStrong, enhancedOnly: true},
	{tokens: []string{"timestamp"}, category: CategoryDate, strength: strengthStrong, enhancedOnly: true},
	{tokens: []string{"date"}, category: CategoryDate, strength: strengthStrong, enhancedOnly: true},

	{tokens: []string{"unit", "price"}, category: CategoryNumber, strength: strengthStrong, enhancedOnly: true},
	{tokens: []string{"total", "amount"}, category: CategoryNumber, strength: strengthStrong, enhancedOnly: true},
	{tokens: []string{"amount"}, category: CategoryNumber, strength: strengthStrong, enhancedOnly: true},
	{tokens: []string{"salary"}, category: CategoryNumber, strength: strengthStrong, enhancedOnly: true},
	{tokens: []string{"balance"}, category: CategoryNumber, strength: strengthStrong, enhancedOnly: true},
	{tokens: []string{"price"}, category: CategoryNumber, strength: strengthStrong, enhancedOnly: true},
	{tokens: []string{"quantity"}, category: CategoryNumber, strength: strengthStrong, enhancedOnly: true},
	{tokens: []string{"qty"}, category: CategoryNumber, strength: strengthStrong, enhancedOnly: true},
	{tokens: []string{"stock"}, category: CategoryNumber, strength: strengthStrong, enhancedOnly: true},
	{tokens: []string{"income"}, category: CategoryNumber, strength: strengthStrong, enhancedOnly: true},
	{tokens: []string{"score"}, category: CategoryNumber, strength: strengthStrong, enhancedOnly: true},

	{tokens: []string{"full", "name"}, category: CategoryGeneric, strength: strengthStrong, enhancedOnly: true},
	{tokens: []string{"first", "name"}, category: CategoryGeneric, strength: strengthStrong, enhancedOnly: true},
	{tokens: []string{"last", "name"}, category: CategoryGeneric, strength: strengthStrong, enhancedOnly: true},
	{tokens: []string{"user", "name"}, category: CategoryGeneric, strength: strengthStrong, enhancedOnly: true},
	{tokens: []string{"username"}, category: CategoryGeneric, strength: strengthStrong, enhancedOnly: true},
	{tokens: []string{"address"}, category: CategoryGeneric, strength: strengthStrong, enhancedOnly: true},
	{tokens: []string{"name"}, category: CategoryGeneric, strength: strengthMedium, enhancedOnly: true},
	{tokens: []string{"addr"}, category: CategoryGeneric, strength: strengthMedium, enhancedOnly: true},
	{tokens: []string{"notes"}, category: CategoryGeneric, strength: strengthMedium, enhancedOnly: true},
	{tokens: []string{"note"}, category: CategoryGeneric, strength: strengthMedium, enhancedOnly: true},
	{tokens: []string{"remark"}, category: CategoryGeneric, strength: strengthMedium, enhancedOnly: true},
	{tokens: []string{"description"}, category: CategoryGeneric, strength: strengthMedium, enhancedOnly: true},
	{tokens: []string{"comment"}, category: CategoryGeneric, strength: strengthMedium, enhancedOnly: true},
}

var emailPattern = regexp.MustCompile(`^[A-Za-z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)+$`)
var numberPattern = regexp.MustCompile(`^[+-]?[0-9]+(?:\.[0-9]+)?$`)

var allCategories = []Category{
	CategoryPhone,
	CategoryEmail,
	CategoryIDCard,
	CategoryBankCard,
	CategoryIP,
	CategoryBirthdate,
	CategoryNumber,
	CategoryDate,
	CategoryGeneric,
}

var legacyCategories = allCategories[:6]

// ClassifyColumnName performs only token/phrase matching. It never uses
// substring matching. The returned signal name belongs to a fixed vocabulary.
func ClassifyColumnName(
	name string,
	categories []Category,
) (Category, []Signal, bool, error) {
	allowed, _, err := normalizeCategories(categories)
	if err != nil {
		return "", nil, false, err
	}
	classification, matched := classifyColumnNameGlobal(name, discoveryEnhancedEnabled)
	classification, matched = filterColumnCandidate(classification, matched, allowed)
	if !matched {
		return "", nil, false, nil
	}
	return classification.category, []Signal{classification.signal}, true, nil
}

func classifyColumn(meta ColumnMeta, allowed map[Category]bool) (columnCandidate, bool) {
	classification, matched := classifyColumnNameGlobal(meta.Column, discoveryEnhancedEnabled)
	classification, matched = filterColumnCandidate(classification, matched, allowed)
	if !matched {
		return columnCandidate{}, false
	}
	classification.meta = meta
	return classification, true
}

func filterColumnCandidate(
	candidate columnCandidate,
	matched bool,
	allowed map[Category]bool,
) (columnCandidate, bool) {
	if !matched || !allowed[candidate.category] {
		return columnCandidate{}, false
	}
	return candidate, true
}

func classifyColumnNameGlobal(name string, enhanced bool) (columnCandidate, bool) {
	tokens := splitColumnName(name)
	var winner namePattern
	matched := false
	for _, pattern := range columnNamePatterns {
		if pattern.enhancedOnly && !enhanced || !containsPhrase(tokens, pattern.tokens) {
			continue
		}
		if !matched || betterNamePatternFor(pattern, winner, enhanced) {
			winner = pattern
			matched = true
		}
	}
	if !matched {
		return columnCandidate{}, false
	}
	strength := "medium"
	if winner.strength == strengthStrong {
		strength = "strong"
	}
	return columnCandidate{
		category: winner.category,
		strength: winner.strength,
		signal: Signal{
			Name:  "column_name_" + strength + "_" + string(winner.category),
			Count: 1,
		},
	}, true
}

func betterNamePatternFor(left, right namePattern, enhanced bool) bool {
	if !enhanced {
		if left.legacyRank != right.legacyRank {
			return left.legacyRank < right.legacyRank
		}
	}
	return betterNamePattern(left, right)
}

func betterNamePattern(left, right namePattern) bool {
	if leftTier, rightTier := categoryTier(left.category), categoryTier(right.category); leftTier != rightTier {
		return leftTier < rightTier
	}
	if len(left.tokens) != len(right.tokens) {
		return len(left.tokens) > len(right.tokens)
	}
	if leftRank, rightRank := categoryPreference(left.category), categoryPreference(right.category); leftRank != rightRank {
		return leftRank < rightRank
	}
	if left.strength != right.strength {
		return left.strength > right.strength
	}
	return strings.Join(left.tokens, "\x00") < strings.Join(right.tokens, "\x00")
}

func categoryTier(category Category) int {
	switch category {
	case CategoryPhone, CategoryEmail, CategoryIDCard, CategoryBankCard, CategoryIP, CategoryBirthdate:
		return 0
	case CategoryDate, CategoryNumber:
		return 1
	case CategoryGeneric:
		return 2
	default:
		return 3
	}
}

func categoryPreference(category Category) int {
	switch category {
	case CategoryPhone:
		return 0
	case CategoryEmail:
		return 1
	case CategoryIDCard:
		return 2
	case CategoryBankCard:
		return 3
	case CategoryIP:
		return 4
	case CategoryBirthdate:
		return 5
	case CategoryDate:
		return 6
	case CategoryNumber:
		return 7
	case CategoryGeneric:
		return 8
	default:
		return 9
	}
}

func splitColumnName(name string) []string {
	runes := []rune(strings.TrimSpace(name))
	if len(runes) == 0 {
		return nil
	}
	var tokens []string
	start := -1
	flush := func(end int) {
		if start < 0 || start >= end {
			return
		}
		tokens = append(tokens, strings.ToLower(string(runes[start:end])))
		start = -1
	}
	for index, current := range runes {
		if !unicode.IsLetter(current) && !unicode.IsDigit(current) {
			flush(index)
			continue
		}
		if start < 0 {
			start = index
			continue
		}
		previous := runes[index-1]
		boundary := unicode.IsLower(previous) && unicode.IsUpper(current)
		boundary = boundary || unicode.IsDigit(previous) != unicode.IsDigit(current) &&
			(unicode.IsDigit(previous) || unicode.IsDigit(current))
		if !boundary && unicode.IsUpper(previous) && unicode.IsUpper(current) && index+1 < len(runes) {
			boundary = unicode.IsLower(runes[index+1])
		}
		if boundary {
			flush(index)
			start = index
		}
	}
	flush(len(runes))
	return tokens
}

func containsPhrase(tokens, phrase []string) bool {
	if len(phrase) == 0 || len(phrase) > len(tokens) {
		return false
	}
	for start := 0; start+len(phrase) <= len(tokens); start++ {
		matched := true
		for offset := range phrase {
			if tokens[start+offset] != phrase[offset] {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func normalizeCategories(categories []Category) (map[Category]bool, []Category, error) {
	return normalizeCategoriesFor(categories, discoveryEnhancedEnabled)
}

func normalizeCategoriesFor(categories []Category, enhanced bool) (map[Category]bool, []Category, error) {
	activeCategories := legacyCategories
	if enhanced {
		activeCategories = allCategories
	}
	allowed := make(map[Category]bool, len(activeCategories))
	if len(categories) == 0 {
		categories = activeCategories
	}
	normalized := make([]Category, 0, len(categories))
	for _, category := range categories {
		if !isKnownCategoryFor(category, enhanced) {
			return nil, nil, classified(CodeUnknownCategory, ErrUnknownCategory)
		}
		if allowed[category] {
			continue
		}
		allowed[category] = true
		normalized = append(normalized, category)
	}
	return allowed, normalized, nil
}

func isKnownCategory(category Category) bool {
	return isKnownCategoryFor(category, discoveryEnhancedEnabled)
}

// IsKnownCategory reports whether category is currently exposed by the
// discovery rollout gate. Callers must use this instead of maintaining a
// second category allowlist outside this package.
func IsKnownCategory(category Category) bool {
	return isKnownCategory(category)
}

func isKnownCategoryFor(category Category, enhanced bool) bool {
	switch category {
	case CategoryPhone, CategoryEmail, CategoryIDCard, CategoryBankCard, CategoryIP, CategoryBirthdate:
		return true
	case CategoryNumber, CategoryDate, CategoryGeneric:
		return enhanced
	default:
		return false
	}
}

// ValidateSample validates one complete non-NULL sample using the category's
// fixed first-release rule. referenceDate makes birth/age checks deterministic.
func ValidateSample(category Category, value string, referenceDate time.Time) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	switch category {
	case CategoryPhone:
		return validMainlandPhone(value)
	case CategoryEmail:
		return validEmail(value)
	case CategoryIDCard:
		return validMainlandIDCard(value, referenceDate)
	case CategoryBankCard:
		return validBankCard(value)
	case CategoryIP:
		return validIP(value)
	case CategoryBirthdate:
		_, ok := parseBirthdate(value, referenceDate)
		return ok
	case CategoryNumber:
		return numberPattern.MatchString(value)
	case CategoryDate:
		return validDate(value)
	case CategoryGeneric:
		return false
	default:
		return false
	}
}

func validDate(value string) bool {
	// Compact numeric values are deliberately excluded so Unix timestamps and
	// YYYYMMDD-shaped numbers are not assigned date semantics.
	if allASCIIDigits(value) {
		return false
	}
	for _, layout := range []string{
		"2006-01-02",
		"2006/01/02",
		"2006.01.02",
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999",
		"2006/01/02 15:04:05.999999999",
		"2006-01-02T15:04:05.999999999",
	} {
		if _, err := time.Parse(layout, value); err == nil {
			return true
		}
	}
	return false
}

func validEmail(value string) bool {
	if len(value) > 254 || !emailPattern.MatchString(value) {
		return false
	}
	at := strings.LastIndexByte(value, '@')
	if at < 1 || at > 64 {
		return false
	}
	local := value[:at]
	return local[0] != '.' && local[len(local)-1] != '.' && !strings.Contains(local, "..")
}

func validMainlandPhone(value string) bool {
	if strings.HasPrefix(value, "+86") {
		value = value[3:]
	}
	if len(value) != 11 || value[0] != '1' || value[1] < '3' || value[1] > '9' {
		return false
	}
	return allASCIIDigits(value)
}

func validMainlandIDCard(value string, referenceDate time.Time) bool {
	value = strings.ToUpper(value)
	if len(value) == 15 {
		if !allASCIIDigits(value) {
			return false
		}
		birthdate, err := time.ParseInLocation("20060102", "19"+value[6:12], time.UTC)
		if err != nil {
			return false
		}
		today := time.Date(referenceDate.Year(), referenceDate.Month(), referenceDate.Day(), 0, 0, 0, 0, time.UTC)
		return !birthdate.After(today)
	}
	if len(value) != 18 || !allASCIIDigits(value[:17]) {
		return false
	}
	if value[17] != 'X' && (value[17] < '0' || value[17] > '9') {
		return false
	}
	birthdate, err := time.ParseInLocation("20060102", value[6:14], time.UTC)
	if err != nil {
		return false
	}
	today := time.Date(referenceDate.Year(), referenceDate.Month(), referenceDate.Day(), 0, 0, 0, 0, time.UTC)
	if birthdate.After(today) {
		return false
	}
	weights := [...]int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}
	checks := "10X98765432"
	sum := 0
	for index := 0; index < 17; index++ {
		sum += int(value[index]-'0') * weights[index]
	}
	return value[17] == checks[sum%11]
}

func validBankCard(value string) bool {
	digits, ok := normalizeBankCard(value)
	if !ok || len(digits) < 13 || len(digits) > 19 {
		return false
	}
	sum := 0
	double := false
	for index := len(digits) - 1; index >= 0; index-- {
		digit := int(digits[index] - '0')
		if double {
			digit *= 2
			if digit > 9 {
				digit -= 9
			}
		}
		sum += digit
		double = !double
	}
	return sum%10 == 0
}

func normalizeBankCard(value string) (string, bool) {
	var digits strings.Builder
	digits.Grow(len(value))
	for index := 0; index < len(value); index++ {
		switch character := value[index]; {
		case character >= '0' && character <= '9':
			digits.WriteByte(character)
		case character == ' ' || character == '-':
		default:
			return "", false
		}
	}
	return digits.String(), true
}

func validIP(value string) bool {
	address, err := netip.ParseAddr(value)
	if err != nil {
		prefix, prefixErr := netip.ParsePrefix(value)
		if prefixErr != nil {
			return false
		}
		address = prefix.Addr()
	}
	if address.Zone() != "" {
		return false
	}
	address = address.Unmap()
	return address.Is4() || address.Is6()
}

func parseBirthdate(value string, referenceDate time.Time) (time.Time, bool) {
	for _, layout := range []string{"2006-01-02", "2006/01/02", "2006.01.02", "20060102"} {
		if date, ok := parseDateWithLayout(value, layout, referenceDate); ok {
			return date, true
		}
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05"} {
		date, err := time.Parse(layout, value)
		if err != nil {
			continue
		}
		calendarDate := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)
		if validBirthdateRange(calendarDate, referenceDate) {
			return calendarDate, true
		}
	}
	return time.Time{}, false
}

func parseDateWithLayout(value, layout string, referenceDate time.Time) (time.Time, bool) {
	date, err := time.ParseInLocation(layout, value, time.UTC)
	if err != nil {
		return time.Time{}, false
	}
	if !validBirthdateRange(date, referenceDate) {
		return time.Time{}, false
	}
	return date, true
}

func validBirthdateRange(date, referenceDate time.Time) bool {
	today := time.Date(referenceDate.Year(), referenceDate.Month(), referenceDate.Day(), 0, 0, 0, 0, time.UTC)
	if date.After(today) {
		return false
	}
	age := today.Year() - date.Year()
	anniversary := time.Date(today.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)
	if today.Before(anniversary) {
		age--
	}
	return age >= 0 && age <= 120
}

func allASCIIDigits(value string) bool {
	if value == "" {
		return false
	}
	for index := range value {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
	}
	return true
}

type sampleEvidence struct {
	eligible int
	matched  int
}

func evaluateSamples(category Category, values []*string, referenceDate time.Time) sampleEvidence {
	if samplePolicyFor(category) == samplePolicyNone {
		return sampleEvidence{}
	}
	var evidence sampleEvidence
	for _, pointer := range values {
		if pointer == nil || strings.TrimSpace(*pointer) == "" {
			continue
		}
		evidence.eligible++
		if ValidateSample(category, *pointer, referenceDate) {
			evidence.matched++
		}
	}
	return evidence
}

func samplePolicyFor(category Category) samplePolicy {
	switch category {
	case CategoryGeneric:
		return samplePolicyNone
	case CategoryPhone, CategoryEmail, CategoryIDCard, CategoryBankCard, CategoryIP,
		CategoryBirthdate, CategoryNumber, CategoryDate:
		return samplePolicyFormat
	default:
		return samplePolicyFormat
	}
}

func (evidence sampleEvidence) supports() bool {
	return evidence.eligible >= 3 && evidence.matched*100 >= evidence.eligible*80
}

func (evidence sampleEvidence) contradicts() bool {
	return evidence.eligible >= 3 && evidence.matched*100 < evidence.eligible*50
}
