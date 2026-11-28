package chatbi

import (
	"strings"
	"testing"
)

func sampleAnalysis(rows ...map[string]any) QueryData {
	return QueryData{Fields: []string{"label", "amount"}, FieldsInfo: []FieldInfo{{Name: "label"}, {Name: "amount", Numeric: true}},
		AuditID: 10, Data: rows}
}

func TestExactAnalysisShares(t *testing.T) {
	data := sampleAnalysis(map[string]any{"label": "华东", "amount": "60.00"}, map[string]any{"label": "华南", "amount": "30.00"},
		map[string]any{"label": "华北", "amount": "10.00"})
	answer, ok := resultAnalysisFacts(data, "说明地区占比")
	if !ok || !strings.Contains(answer, "100.00") || !strings.Contains(answer, "60.00%") || !strings.Contains(answer, "30.00%") {
		t.Fatalf("unexpected shares: %t %q", ok, answer)
	}
	data.Truncated = true
	if _, ok := resultAnalysisFacts(data, "说明地区占比"); ok {
		t.Fatal("no full-result share for truncated samples")
	}
}

func TestExactAnalysisOrderedDates(t *testing.T) {
	data := sampleAnalysis(map[string]any{"label": "2026-03", "amount": "100.00"}, map[string]any{"label": "2026-01", "amount": "0.00"},
		map[string]any{"label": "2026-02", "amount": "80.00"})
	answer, ok := resultAnalysisFacts(data, "说明每月差值")
	if !ok || !strings.Contains(answer, "2026-01 到 2026-03") || !strings.Contains(answer, "20.00 (25.00%)") || strings.Count(answer, "%") != 1 {
		t.Fatalf("unexpected dated changes: %t %q", ok, answer)
	}
}

func TestExactAnalysisDoesNotCoerceMaskedOrNullMetrics(t *testing.T) {
	for _, value := range []any{nil, "**.00", "30/10", "1e300"} {
		data := sampleAnalysis(map[string]any{"label": "East", "amount": "5.00"}, map[string]any{"label": "West", "amount": value})
		if _, ok := resultAnalysisFacts(data, "Compare regions"); ok {
			t.Errorf("unexpected analysis of %v", value)
		}
	}
}

func TestExactAnalysisRejectsPreviouslyRoundedFloat(t *testing.T) {
	// A float64 can no longer distinguish consecutive integer values here.
	const previouslyRounded = float64(9007199254740993)
	data := sampleAnalysis(
		map[string]any{"label": "East", "amount": "1"},
		map[string]any{"label": "West", "amount": previouslyRounded},
	)
	if _, ok := resultAnalysisFacts(data, "Compare regions"); ok {
		t.Fatal("a previously rounded number must not be labeled exact")
	}

	data.Data[1]["amount"] = "9007199254740993"
	result, ok := resultAnalysisFacts(data, "Compare regions")
	if !ok || !strings.Contains(result, "9007199254740994") {
		t.Fatalf("exact decimal text must retain the full integer: %t %q", ok, result)
	}
}
