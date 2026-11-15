package chatbi

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMultipleMeasureChartMetadataKeepsOrderAndNoResultValues(t *testing.T) {
	for _, question := range []string{"按地区画柱状图", "draw a line chart", "draw a bar chart"} {
		data := QueryData{Fields: []string{"id", "region", "sales", "cost"},
			FieldsInfo: []FieldInfo{{Name: "id"}, {Name: "region"}, {Name: "sales"}, {Name: "cost"}},
			Data:       []map[string]any{{"id": 1, "region": "synthetic-east", "sales": "10", "cost": nil}, {"id": 2, "region": "synthetic-west", "sales": 0, "cost": 5.5}}}
		var chart struct {
			Type string `json:"type"`
			Axis struct {
				X map[string]string   `json:"x"`
				Y []map[string]string `json:"y"`
			} `json:"axis"`
		}
		raw := chartConfigForQuestion(question, &data)
		require.NoError(t, json.Unmarshal([]byte(raw), &chart))
		require.NotEqual(t, "table", chart.Type)
		require.Equal(t, "region", chart.Axis.X["value"])
		require.Equal(t, []map[string]string{{"name": "sales", "value": "sales"}, {"name": "cost", "value": "cost"}}, chart.Axis.Y)
		require.NotContains(t, raw, "synthetic-east")
		require.NotContains(t, raw, "synthetic-west")
		require.NotContains(t, raw, "5.5")
	}
}

func TestChartNumericProofAndPieDoNotCoerceOrDiscardValues(t *testing.T) {
	for _, invalid := range []any{true, false, "", " ", "0x10", "NaN", "Infinity", math.Inf(1), "9007199254740993", "9007199254740991.1", "1e-400"} {
		_, valid := chartNumber(invalid)
		require.False(t, valid, "value=%v", invalid)
	}
	for _, tc := range []struct {
		question string
		values   []any
		kind     string
	}{
		{"画柱状图", []any{0, nil}, "column"},
		{"画折线图", []any{-2, nil, 3}, "line"},
		{"画饼图", []any{-2, 3}, "table"},
		{"画饼图", []any{0, 0}, "table"},
		{"画饼图", []any{nil, 3}, "pie"},
	} {
		data := QueryData{Fields: []string{"region", "amount"}, FieldsInfo: []FieldInfo{{Name: "region"}, {Name: "amount"}}}
		for _, value := range tc.values {
			data.Data = append(data.Data, map[string]any{"region": "synthetic", "amount": value})
		}
		var chart struct {
			Type string `json:"type"`
		}
		require.NoError(t, json.Unmarshal([]byte(chartConfigForQuestion(tc.question, &data)), &chart))
		require.Equal(t, tc.kind, chart.Type)
	}
}

func TestPieMultipleMeasuresAndOverwideOrUntrustedChartFallBackToTable(t *testing.T) {
	for _, fields := range [][]string{{"region", "sales", "cost"}, {"region", "a", "b", "c", "d", "e"}, {"region", "sales", "sales"}} {
		data := QueryData{Fields: fields, Data: []map[string]any{{"region": "synthetic", "sales": 10, "cost": 5, "a": 1, "b": 2, "c": 3, "d": 4, "e": 5}}}
		for _, field := range fields {
			data.FieldsInfo = append(data.FieldsInfo, FieldInfo{Name: field})
		}
		raw := chartConfigForQuestion("画饼图", &data)
		require.True(t, strings.Contains(raw, `"type":"table"`), raw)
	}
}

func TestMultipleMeasureChartGroupsOriginalClientQuotasAndRejectsReservedDimensions(t *testing.T) {
	data := QueryData{Fields: []string{"region", "sales", "cost"}, FieldsInfo: []FieldInfo{{Name: "region"}, {Name: "sales"}, {Name: "cost"}}, Data: []map[string]any{{"region": "east", "sales": 10, "cost": 5}}}
	var chart struct {
		Type string `json:"type"`
		Axis struct {
			Y          []map[string]string `json:"y"`
			MultiQuota struct {
				Name  string   `json:"name"`
				Value []string `json:"value"`
			} `json:"multi-quota"`
		} `json:"axis"`
	}
	require.NoError(t, json.Unmarshal([]byte(chartConfigForQuestion("画柱状图", &data)), &chart))
	require.Len(t, chart.Axis.Y, 2)
	require.Equal(t, "指标 / Measures", chart.Axis.MultiQuota.Name)
	require.Equal(t, []string{"sales", "cost"}, chart.Axis.MultiQuota.Value, "SQLBot otherwise renders only y[0]")
	for _, reserved := range []string{"sqlbot_auto_quota", "sqlbot_auto_series", "sqlbot_axis_format", "__proto__", "constructor", "prototype"} {
		data.Fields[0], data.FieldsInfo[0].Name = reserved, reserved
		data.Data[0] = map[string]any{reserved: "east", "sales": 10, "cost": 5}
		var fallback struct {
			Type string `json:"type"`
		}
		require.NoError(t, json.Unmarshal([]byte(chartConfigForQuestion("画折线图", &data)), &fallback))
		require.Equal(t, "table", fallback.Type, "reserved renderer dimension=%s", reserved)
	}
}
