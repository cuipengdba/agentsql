package chatbi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExplicitChartIntentUsesOnlyControlledResultMetadata(t *testing.T) {
	for _, tc := range []struct{ question, kind string }{
		{"Show amount by region as a bar chart", "bar"},
		{"按地区画柱状图", "column"},
		{"Show trend as a line chart", "line"},
		{"Show share as a pie chart", "pie"},
	} {
		data := QueryData{
			Fields:     []string{"region", "amount"},
			FieldsInfo: []FieldInfo{{Name: "region"}, {Name: "amount"}},
			Data:       []map[string]any{{"region": "east", "amount": "10"}, {"region": "west", "amount": "20"}},
		}
		raw := chartConfigForQuestion(tc.question, &data)
		var chart struct {
			Type string         `json:"type"`
			Axis map[string]any `json:"axis"`
		}
		if err := json.Unmarshal([]byte(raw), &chart); err != nil || chart.Type != tc.kind || !data.FieldsInfo[1].Numeric || data.FieldsInfo[0].Numeric || strings.Contains(raw, "east") || strings.Contains(raw, "west") {
			t.Fatalf("%q: chart=%s fields=%+v err=%v", tc.question, raw, data.FieldsInfo, err)
		}
		if tc.kind == "pie" {
			if chart.Axis["series"] == nil || chart.Axis["y"] == nil {
				t.Fatalf("pie axis: %s", raw)
			}
		} else if chart.Axis["x"] == nil || chart.Axis["y"] == nil {
			t.Fatalf("cartesian axis: %s", raw)
		}
	}
}

func TestAmbiguousOrUnverifiedChartFallsBackToTable(t *testing.T) {
	for _, tc := range []struct {
		question string
		fields   []string
		rows     []map[string]any
	}{
		{"Show amount by region", []string{"region", "amount"}, []map[string]any{{"region": "east", "amount": "10"}}},
		{"Show a bar chart and a pie chart", []string{"region", "amount"}, []map[string]any{{"region": "east", "amount": "10"}}},
		{"Show a bar chart", []string{"region", "amount"}, []map[string]any{{"region": "east", "amount": "not-a-number"}}},
		{"Show a bar chart", []string{"region", "amount"}, nil},
		{"Show a bar chart", []string{"region", "amount", "other"}, []map[string]any{{"region": "east", "amount": "10", "other": "20"}}},
	} {
		data := QueryData{Fields: tc.fields, Data: tc.rows}
		for _, field := range tc.fields {
			data.FieldsInfo = append(data.FieldsInfo, FieldInfo{Name: field})
		}
		var chart struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(chartConfigForQuestion(tc.question, &data)), &chart); err != nil || chart.Type != "table" {
			t.Fatalf("%q: unexpected chart %q: %v", tc.question, chart.Type, err)
		}
	}
}
