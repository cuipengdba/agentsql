package mask

import "strings"

func normalizeColumnName(name string) string {
	normalized := strings.TrimSpace(name)
	if len(normalized) >= 2 {
		first, last := normalized[0], normalized[len(normalized)-1]
		if (first == '"' && last == '"') ||
			(first == '`' && last == '`') ||
			(first == '[' && last == ']') {
			normalized = strings.TrimSpace(normalized[1 : len(normalized)-1])
		}
	}
	return strings.ToLower(normalized)
}

func maskPhone(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	if isEmptySensitiveValue(trimmed) {
		return value, false
	}
	phone := trimmed
	if strings.HasPrefix(phone, "+") {
		phone = phone[1:]
	}
	if strings.HasPrefix(phone, "86") && len(phone)-2 == 11 {
		phone = phone[2:]
	}
	phoneRunes := []rune(phone)
	switch len(phoneRunes) {
	case 11:
		return string(phoneRunes[:3]) + "****" + string(phoneRunes[len(phoneRunes)-4:]), true
	case 7, 8, 9, 10:
		return string(phoneRunes[:3]) + "*" + string(phoneRunes[len(phoneRunes)-4:]), true
	case 3, 4, 5, 6:
		return string(phoneRunes[:1]) + strings.Repeat("*", len(phoneRunes)-1), true
	default:
		return value, false
	}
}

func maskEmail(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	if isEmptySensitiveValue(trimmed) {
		return value, false
	}
	lastAt := strings.LastIndex(trimmed, "@")
	if lastAt < 0 {
		return maskEmailLocal(trimmed), true
	}
	local := trimmed[:lastAt]
	if local == "" {
		return value, false
	}
	localRunes := []rune(local)
	return string(localRunes[:1]) + "***" + trimmed[lastAt:], true
}

func maskEmailLocal(local string) string {
	runes := []rune(local)
	return string(runes[:1]) + "***"
}

func isEmptySensitiveValue(value string) bool {
	return value == "" || strings.EqualFold(value, "NULL") || strings.EqualFold(value, "<nil>")
}
