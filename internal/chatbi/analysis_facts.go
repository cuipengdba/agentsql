package chatbi

import (
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const analysisFactsMethod = "authorized_result_exact_decimal"

var analysisDecimal = regexp.MustCompile(`^[+-]?[0-9]+(?:\.[0-9]+)?$`)

type analysisDatum struct {
	label string
	value *big.Rat
	date  time.Time
}

// Summarize a complete single-dimension series using only the audited,
// currently visible rows. Other shapes retain the existing narrative route.
func resultAnalysisFacts(data QueryData, question string) (string, bool) {
	if data.Truncated || data.AuditID <= 0 || len(data.Data) < 2 || len(data.Data) > 20 || len(data.Fields) != 2 {
		return "", false
	}
	label, measure := data.Fields[0], data.Fields[1]
	numeric := map[string]bool{}
	for _, field := range data.FieldsInfo {
		numeric[field.Name] = field.Numeric
	}
	if label == measure || numeric[label] || !numeric[measure] || !analysisFieldLabel(measure) {
		return "", false
	}

	rows := make([]analysisDatum, 0, len(data.Data))
	seen := make(map[string]bool, len(data.Data))
	total := new(big.Rat)
	nonnegative := true
	precision := 0
	for _, row := range data.Data {
		if row[label] == nil || row[measure] == nil || len(row) != 2 {
			return "", false
		}
		name, ok := row[label].(string)
		if !ok || !analysisFieldLabel(name) || seen[name] {
			return "", false
		}
		seen[name] = true
		value, decimals, ok := exactAnalysisDecimal(row[measure])
		if !ok {
			return "", false
		}
		precision = max(precision, decimals)
		nonnegative = nonnegative && value.Sign() >= 0
		total.Add(total, value)
		rows = append(rows, analysisDatum{label: name, value: value})
	}

	chinese := strings.ContainsFunc(question, func(r rune) bool { return unicode.Is(unicode.Han, r) })
	if analysisDateSeries(rows) {
		return dateAnalysisFacts(measure, rows, precision, chinese), true
	}
	if !nonnegative || total.Sign() <= 0 {
		return "", false
	}
	return categoryAnalysisFacts(measure, rows, total, precision, chinese), true
}

func analysisFieldLabel(label string) bool {
	if label == "" || strings.TrimSpace(label) != label || len([]rune(label)) > 80 {
		return false
	}
	return !strings.ContainsFunc(label, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != ' ' && r != '_' && r != '-' && r != '.'
	})
}

func exactAnalysisDecimal(value any) (*big.Rat, int, bool) {
	var text string
	switch typed := value.(type) {
	case json.Number:
		text = string(typed)
	case string:
		text = typed
	case int:
		text = strconv.Itoa(typed)
	case int64:
		text = strconv.FormatInt(typed, 10)
	default:
		return nil, 0, false
	}
	if len(text) > 48 || !analysisDecimal.MatchString(text) {
		return nil, 0, false
	}
	precision := 0
	if _, fraction, found := strings.Cut(text, "."); found {
		precision = len(fraction)
	}
	if precision > 12 {
		return nil, 0, false
	}
	number, ok := new(big.Rat).SetString(text)
	return number, precision, ok
}

func analysisDateSeries(rows []analysisDatum) bool {
	layout := "2006-01-02"
	if len(rows[0].label) == len("2006-01") {
		layout = "2006-01"
	}
	for index := range rows {
		date, err := time.Parse(layout, rows[index].label)
		if err != nil || date.Format(layout) != rows[index].label {
			return false
		}
		rows[index].date = date
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].date.Before(rows[j].date) })
	return true
}

func categoryAnalysisFacts(measure string, rows []analysisDatum, total *big.Rat, precision int, chinese bool) string {
	shares := make([]string, 0, len(rows))
	for _, row := range rows {
		percent := new(big.Rat).Mul(new(big.Rat).Quo(row.value, total), big.NewRat(100, 1)).FloatString(2)
		if chinese {
			shares = append(shares, fmt.Sprintf("%s 为 %s，占结果合计的 %s%%", row.label, row.value.FloatString(precision), percent))
		} else {
			shares = append(shares, fmt.Sprintf("%s is %s (%s%% of the returned total)", row.label, row.value.FloatString(precision), percent))
		}
	}
	if chinese {
		return fmt.Sprintf("当前受权结果共 %d 行，%s 的结果值之和为 %s。%s。", len(rows), measure, total.FloatString(precision), strings.Join(shares, "。"))
	}
	return fmt.Sprintf("The %d currently authorized rows total %s for %s. %s.", len(rows), total.FloatString(precision), measure, strings.Join(shares, ". "))
}

func dateAnalysisFacts(measure string, rows []analysisDatum, precision int, chinese bool) string {
	changes := make([]string, 0, len(rows)-1)
	for index := 1; index < len(rows); index++ {
		previous, current := rows[index-1], rows[index]
		delta := new(big.Rat).Sub(current.value, previous.value)
		percent := ""
		if previous.value.Sign() > 0 {
			change := new(big.Rat).Mul(new(big.Rat).Quo(delta, previous.value), big.NewRat(100, 1))
			percent = fmt.Sprintf(" (%s%%)", change.FloatString(2))
		}
		if chinese {
			changes = append(changes, fmt.Sprintf("%s 的 %s 为 %s，较上一条结果 %s 变化 %s%s", current.label, measure,
				current.value.FloatString(precision), previous.label, delta.FloatString(precision), percent))
		} else {
			changes = append(changes, fmt.Sprintf("%s %s is %s, a change of %s%s from the prior observed value on %s", current.label,
				measure, current.value.FloatString(precision), delta.FloatString(precision), percent, previous.label))
		}
	}
	if chinese {
		return fmt.Sprintf("当前受权结果从 %s 到 %s。%s。", rows[0].label, rows[len(rows)-1].label, strings.Join(changes, "。"))
	}
	return fmt.Sprintf("The currently authorized results cover %s to %s. %s.", rows[0].label, rows[len(rows)-1].label, strings.Join(changes, ". "))
}
