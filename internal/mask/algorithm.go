package mask

import (
	"fmt"
	"net/netip"
	"strings"
	"time"
)

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

func maskIDCard(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	if isEmptySensitiveValue(trimmed) {
		return value, false
	}
	if len(trimmed) == 18 && isASCIIDigits(trimmed[:17]) &&
		(isASCIIDigit(trimmed[17]) || trimmed[17] == 'X' || trimmed[17] == 'x') {
		lastFour := trimmed[14:18]
		if trimmed[17] == 'x' {
			lastFour = trimmed[14:17] + "X"
		}
		return trimmed[:6] + "********" + lastFour, true
	}
	if len(trimmed) == 15 && isASCIIDigits(trimmed) {
		return trimmed[:6] + "******" + trimmed[12:], true
	}
	return RedactedFallback, true
}

func maskBankCard(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	if isEmptySensitiveValue(trimmed) {
		return value, false
	}
	digits := make([]byte, 0, len(trimmed))
	for index := 0; index < len(trimmed); index++ {
		character := trimmed[index]
		switch {
		case isASCIIDigit(character):
			digits = append(digits, character)
		case character == ' ' || character == '-':
		default:
			return RedactedFallback, true
		}
	}
	if len(digits) < 13 || len(digits) > 19 {
		return RedactedFallback, true
	}
	return string(digits[:6]) + strings.Repeat("*", len(digits)-10) + string(digits[len(digits)-4:]), true
}

func maskIP(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	if isEmptySensitiveValue(trimmed) {
		return value, false
	}
	addr, err := netip.ParseAddr(trimmed)
	if err != nil {
		prefix, prefixErr := netip.ParsePrefix(trimmed)
		if prefixErr != nil {
			return RedactedFallback, true
		}
		addr = prefix.Addr()
	}
	if addr.Zone() != "" {
		return RedactedFallback, true
	}
	addr = addr.Unmap()
	if addr.Is4() {
		bytes := addr.As4()
		return fmt.Sprintf("%d.%d.*.*", bytes[0], bytes[1]), true
	}
	if addr.Is6() {
		bytes := addr.As16()
		return fmt.Sprintf("%02x%02x:%02x%02x:****", bytes[0], bytes[1], bytes[2], bytes[3]), true
	}
	return RedactedFallback, true
}

func maskBirthDate(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	if isEmptySensitiveValue(trimmed) {
		return value, false
	}
	layouts := [...]string{
		"2006-01-02",
		"2006/01/02",
		"2006.01.02",
		"20060102",
		time.RFC3339Nano,
		"2006-01-02 15:04:05",
		"2006-01-02 15:04:05.999999999",
	}
	for _, layout := range layouts {
		parsed, err := time.Parse(layout, trimmed)
		if err == nil {
			return fmt.Sprintf("%04d-**-**", parsed.Year()), true
		}
	}
	return RedactedFallback, true
}

func isASCIIDigits(value string) bool {
	if value == "" {
		return false
	}
	for index := 0; index < len(value); index++ {
		if !isASCIIDigit(value[index]) {
			return false
		}
	}
	return true
}

func isASCIIDigit(character byte) bool {
	return character >= '0' && character <= '9'
}

func isEmptySensitiveValue(value string) bool {
	return value == "" || strings.EqualFold(value, "NULL") || strings.EqualFold(value, "<nil>")
}
