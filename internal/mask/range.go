package mask

import (
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	numericRangePattern   = regexp.MustCompile(`^[+-]?(\d+(\.\d*)?|\.\d+)([eE][+-]?\d+)?$`)
	localTimestampPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}(\.\d{1,9})?$`)
	rfc3339Pattern        = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,9})?(Z|[+-]\d{2}:\d{2})$`)
)

func bucketNumeric(raw string, width, offset int64) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if isEmptySensitiveValue(trimmed) {
		return raw, false
	}
	if len(trimmed) > 128 || width <= 0 || !numericRangePattern.MatchString(trimmed) {
		return RedactedFallback, true
	}

	mantissa := trimmed
	exponent := int64(0)
	if separator := strings.IndexAny(trimmed, "eE"); separator >= 0 {
		mantissa = trimmed[:separator]
		parsedExponent, err := strconv.ParseInt(trimmed[separator+1:], 10, 64)
		if err != nil || parsedExponent < -1000 || parsedExponent > 1000 {
			return RedactedFallback, true
		}
		exponent = parsedExponent
	}

	value, ok := new(big.Rat).SetString(mantissa)
	if !ok {
		return RedactedFallback, true
	}
	if exponent != 0 {
		absoluteExponent := exponent
		if absoluteExponent < 0 {
			absoluteExponent = -absoluteExponent
		}
		power := new(big.Int).Exp(big.NewInt(10), big.NewInt(absoluteExponent), nil)
		factor := new(big.Rat).SetInt(power)
		if exponent > 0 {
			value.Mul(value, factor)
		} else {
			value.Quo(value, factor)
		}
	}

	value.Sub(value, new(big.Rat).SetInt64(offset))
	quotient := value.Quo(value, new(big.Rat).SetInt64(width))
	k, remainder := new(big.Int), new(big.Int)
	k.QuoRem(quotient.Num(), quotient.Denom(), remainder)
	if quotient.Sign() < 0 && remainder.Sign() != 0 {
		k.Sub(k, big.NewInt(1))
	}

	lower := new(big.Int).Mul(k, big.NewInt(width))
	lower.Add(lower, big.NewInt(offset))
	upper := new(big.Int).Add(new(big.Int).Set(lower), big.NewInt(width))
	minimum := big.NewInt(-1 << 63)
	maximum := big.NewInt(1<<63 - 1)
	if lower.Cmp(minimum) < 0 || lower.Cmp(maximum) > 0 || upper.Cmp(minimum) < 0 || upper.Cmp(maximum) > 0 {
		return RedactedFallback, true
	}
	return "[" + lower.String() + "," + upper.String() + ")", true
}

func truncateDate(raw string, granularity RangeGranularity) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if isEmptySensitiveValue(trimmed) {
		return raw, false
	}

	var year int
	var month time.Month
	precision := RangeMonth
	switch {
	case len(trimmed) == 4 && isASCIIDigits(trimmed):
		year, _ = strconv.Atoi(trimmed)
		precision = RangeYear
	case len(trimmed) == 7 && trimmed[4] == '-' && isASCIIDigits(trimmed[:4]) && isASCIIDigits(trimmed[5:]):
		year, _ = strconv.Atoi(trimmed[:4])
		monthNumber, _ := strconv.Atoi(trimmed[5:])
		if monthNumber < 1 || monthNumber > 12 {
			return RedactedFallback, true
		}
		month = time.Month(monthNumber)
	default:
		parsed, ok := parseRangeDate(trimmed)
		if !ok {
			return RedactedFallback, true
		}
		year, month, _ = parsed.Date()
	}
	if year < 1 || year > 9999 {
		return RedactedFallback, true
	}

	switch granularity {
	case RangeYear:
		return fmt.Sprintf("%04d", year), true
	case RangeQuarter:
		if precision == RangeYear {
			return RedactedFallback, true
		}
		quarter := (int(month)-1)/3 + 1
		return fmt.Sprintf("%04dQ%d", year, quarter), true
	case RangeMonth:
		if precision == RangeYear {
			return RedactedFallback, true
		}
		return fmt.Sprintf("%04d-%02d", year, month), true
	default:
		return RedactedFallback, true
	}
}

func parseRangeDate(value string) (time.Time, bool) {
	layouts := [...]string{
		"2006-01-02",
		"2006/01/02",
		"2006.01.02",
		"20060102",
	}
	for _, layout := range layouts {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed, true
		}
	}
	if localTimestampPattern.MatchString(value) {
		layout := "2006-01-02 15:04:05"
		if len(value) > len(layout) {
			layout += ".999999999"
		}
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed, true
		}
	}
	if rfc3339Pattern.MatchString(value) {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}
