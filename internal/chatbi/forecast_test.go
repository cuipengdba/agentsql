package chatbi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestForecastOrderedSeriesAndRejectsUnorderedOrMasked(t *testing.T) {
	series := QueryData{Fields: []string{"day", "amount"}, AuditID: 8, Data: []map[string]any{
		{"day": "2026-01-01", "amount": 10},
		{"day": "2026-01-02", "amount": 20},
		{"day": "2026-01-03", "amount": 30},
	}}
	forecast, err := forecastData("12", series)
	if err != nil || len(forecast.Points) != 3 || forecast.Points[0].Label != "2026-01-04" || forecast.Points[0].Value != 40 || forecast.Points[2].Value != 60 || forecast.AuditID != 8 {
		t.Fatalf("linear forecast: %#v, %v", forecast, err)
	}
	series.Data[1]["amount"] = "***"
	if _, err := forecastData("12", series); !errors.Is(err, ErrForecastUnavailable) {
		t.Fatalf("masked value accepted: %v", err)
	}
	series.Data[1]["amount"] = 20
	series.Data[1]["day"] = "west"
	if _, err := forecastData("12", series); !errors.Is(err, ErrForecastUnavailable) {
		t.Fatalf("unordered categories accepted: %v", err)
	}
	series.Data[1]["day"] = "2026-01-02"
	series.Truncated = true
	if _, err := forecastData("12", series); !errors.Is(err, ErrForecastUnavailable) {
		t.Fatalf("truncated source accepted: %v", err)
	}
}

func TestIllustrativeForecastNarrativeUsesTheSamePointsInChinese(t *testing.T) {
	series := QueryData{Fields: []string{"month", "amount"}, AuditID: 8, Data: []map[string]any{
		{"month": "2026-01", "amount": 10}, {"month": "2026-02", "amount": 20},
		{"month": "2026-03", "amount": 30}, {"month": "2026-04", "amount": 40},
	}}
	forecast, err := forecastData("12", series)
	if err != nil {
		t.Fatal(err)
	}
	content := illustrativeForecastNarrative("预测金额", forecast)
	for _, expected := range []string{"4 行数据", "2026-05 ≈ 50", "2026-06 ≈ 60", "2026-07 ≈ 70", "示意", "未经过预测准确性验证"} {
		if !strings.Contains(content, expected) {
			t.Fatalf("prediction prose omits %q: %s", expected, content)
		}
	}
}

func TestLegacyPredictStreamAndFreshData(t *testing.T) {
	service, p, _, _, token := serviceFixture(t)
	service.Gateway = &forecastGateway{data: QueryData{Fields: []string{"month", "amount"}, Data: []map[string]any{
		{"month": "2026-01", "amount": 4}, {"month": "2026-02", "amount": 6}, {"month": "2026-03", "amount": 8},
	}}}
	model := &narrativeFixtureModel{}
	service.Model = model
	record := finishedFixtureRecord(t, service, p, token)
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/chat/record/"+string(record.ID)+"/predict", strings.NewReader("{}"))
	request.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("predict stream status=%d body=%s", response.Code, response.Body.String())
	}
	events := make([]map[string]any, 0)
	for _, line := range strings.Split(response.Body.String(), "\n") {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var event map[string]any
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data:")), &event) != nil {
			t.Fatal("invalid event JSON")
		}
		events = append(events, event)
	}
	if len(events) != 4 || events[0]["type"] != "id" || events[1]["type"] != "predict-result" || events[2]["type"] != "predict-success" || events[3]["type"] != "predict_finish" {
		t.Fatalf("predict event contract: %#v", events)
	}
	if content, _ := events[1]["content"].(string); model.calls != 0 || !strings.Contains(content, "2026-04 ≈ 10") || !strings.Contains(content, "2026-06 ≈ 14") || !strings.Contains(content, "has not been validated") {
		t.Fatalf("prediction prose diverged from calculated points: calls=%d content=%q", model.calls, content)
	}
	childID, ok := events[0]["id"].(string)
	if !ok || childID == string(record.ID) {
		t.Fatalf("invalid child ID: %#v", events[0])
	}
	get := httptest.NewRequest(http.MethodGet, "/api/v1/chat/record/"+childID+"/predict_data", nil)
	get.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
	dataResponse := httptest.NewRecorder()
	handler.ServeHTTP(dataResponse, get)
	if dataResponse.Code != 200 {
		t.Fatalf("predict data status=%d body=%s", dataResponse.Code, dataResponse.Body.String())
	}
	var envelope struct {
		Data []map[string]any `json:"data"`
	}
	if json.Unmarshal(dataResponse.Body.Bytes(), &envelope) != nil || len(envelope.Data) != 3 || envelope.Data[0]["amount"] != float64(10) {
		t.Fatalf("predict data: %#v", envelope.Data)
	}
}

func TestLegacyPredictWithoutSeriesReturnsEmptyPoints(t *testing.T) {
	service, p, _, _, token := serviceFixture(t)
	series := &forecastGateway{data: QueryData{Fields: []string{"region", "amount"}, Data: []map[string]any{
		{"region": "east", "amount": 10}, {"region": "west", "amount": 20},
	}}}
	service.Gateway = series
	model := &narrativeFixtureModel{}
	service.Model = model
	record := finishedFixtureRecord(t, service, p, token)
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	predict := httptest.NewRequest(http.MethodPost, "/api/v1/chat/record/"+string(record.ID)+"/predict", strings.NewReader("{}"))
	predict.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
	stream := httptest.NewRecorder()
	handler.ServeHTTP(stream, predict)
	if stream.Code != http.StatusOK || !strings.Contains(stream.Body.String(), `"type":"predict-failed"`) {
		t.Fatalf("predict prose stream: %d %s", stream.Code, stream.Body.String())
	}
	if model.calls != 1 {
		t.Fatalf("non-series prose did not use local model: calls=%d", model.calls)
	}
	var childID string
	for _, line := range strings.Split(stream.Body.String(), "\n") {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var event struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data:")), &event); err != nil {
			t.Fatal(err)
		}
		if event.Type == "id" {
			childID = event.ID
		}
	}
	if childID == "" {
		t.Fatal("prediction child ID missing")
	}
	path := "/api/v1/chat/record/" + childID + "/predict_data"
	get := func(bearer string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		if bearer != "" {
			request.Header.Set("X-SQLBOT-TOKEN", "Bearer "+bearer)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	if got := get(""); got.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous prediction data returned %d", got.Code)
	}
	before := series.queries
	response := get(token)
	var envelope struct {
		Data []map[string]any `json:"data"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &envelope) != nil || envelope.Data == nil || len(envelope.Data) != 0 || series.queries <= before {
		t.Fatalf("empty audited prediction data: %d %s queries=%d", response.Code, response.Body.String(), series.queries-before)
	}
	revision, err := service.Store.get(context.Background(), bindingKey(p, "10"), new(Binding))
	if err != nil || service.Store.delete(context.Background(), bindingKey(p, "10"), revision) != nil {
		t.Fatal("could not revoke source")
	}
	if got := get(token); got.Code != http.StatusNotFound {
		t.Fatalf("revoked source returned %d %s", got.Code, got.Body.String())
	}
}

type forecastGateway struct {
	fakeGateway
	data QueryData
}

func (g *forecastGateway) Query(_ context.Context, _ Principal, _ Binding, _ string) (QueryData, error) {
	g.queries++
	if g.queryErr != nil {
		return QueryData{}, g.queryErr
	}
	result := g.data
	result.AuditID = int64(g.queries)
	return result, nil
}

func TestForecastRecordReauthorizesAndAudits(t *testing.T) {
	ctx := context.Background()
	service, p, _, _, token := serviceFixture(t)
	gateway := &forecastGateway{data: QueryData{Fields: []string{"month", "amount"}, Data: []map[string]any{
		{"month": "2026-01", "amount": 4}, {"month": "2026-02", "amount": 6}, {"month": "2026-03", "amount": 8},
	}}}
	service.Gateway = gateway
	record := finishedFixtureRecord(t, service, p, token)
	before := gateway.queries
	result, err := service.ForecastRecord(ctx, p, record.ID)
	if err != nil || gateway.queries != before+1 || result.AuditID <= 0 || result.Points[0].Label != "2026-04" || result.Points[0].Value != 10 {
		t.Fatalf("audited forecast: %#v, queries=%d, err=%v", result, gateway.queries-before, err)
	}
	revision, err := service.Store.get(ctx, bindingKey(p, "10"), new(Binding))
	if err != nil || service.Store.delete(ctx, bindingKey(p, "10"), revision) != nil {
		t.Fatal("could not revoke source")
	}
	before = gateway.queries
	if _, err := service.ForecastRecord(ctx, p, record.ID); err == nil || gateway.queries != before {
		t.Fatalf("revoked forecast reached gateway: %v", err)
	}
}
