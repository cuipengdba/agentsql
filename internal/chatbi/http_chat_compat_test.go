package chatbi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSQLBotRecommendationStreamAndUsageStayOwned(t *testing.T) {
	ctx := context.Background()
	service, principal, _, _, token := serviceFixture(t)
	config := RecommendationConfig{Workspace: "1", Datasource: "10", Mode: 2,
		Questions: []RecommendedProblem{
			{ID: "31", Datasource: "10", Question: "Show amount", Sort: 1},
			{ID: "32", Datasource: "10", Question: "Show sales", Sort: 2},
		}}
	if err := service.Store.put(ctx, recommendationKey("1", "10"), config, 0); err != nil {
		t.Fatal(err)
	}
	chat, err := service.Start(ctx, principal, "10", "fixture", 0)
	if err != nil {
		t.Fatal(err)
	}
	record, err := service.Ask(ctx, principal, chat.ID, "amount", func(context.Context) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service, "v4.2.0")
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, bearer string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, nil)
		if bearer != "" {
			r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	path := "/api/v1/chat/recommend_questions/" + string(chat.ID) + "?articles_number=1"
	if got := request(http.MethodPost, path, ""); got.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous recommendation returned %d", got.Code)
	}
	first := request(http.MethodPost, path, token)
	if first.Code != http.StatusOK || first.Header().Get("Content-Type") != "text/event-stream" || !strings.Contains(first.Body.String(), `Show amount`) || strings.Contains(first.Body.String(), `Show sales`) {
		t.Fatalf("bounded first-chat stream: %d %s", first.Code, first.Body.String())
	}
	followup := request(http.MethodPost, "/api/v1/chat/recommend_questions/"+string(record.ID), token)
	if followup.Code != http.StatusOK || !strings.Contains(followup.Body.String(), `Show sales`) {
		t.Fatalf("record recommendation stream: %d %s", followup.Code, followup.Body.String())
	}
	usage := request(http.MethodGet, "/api/v1/chat/record/"+string(record.ID)+"/usage", token)
	assertSummary := func(label string, result *httptest.ResponseRecorder) {
		t.Helper()
		var body struct {
			Data map[string]json.RawMessage `json:"data"`
		}
		if result.Code != http.StatusOK || json.Unmarshal(result.Body.Bytes(), &body) != nil {
			t.Fatalf("%s summary response: %d %s", label, result.Code, result.Body.String())
		}
		allowed := map[string]bool{"start_time": true, "finish_time": true, "duration": true,
			"total_tokens": true, "steps": true}
		if len(body.Data) != len(allowed) {
			t.Fatalf("%s exposed extra data or omitted summary fields: %s", label, result.Body.String())
		}
		for field := range body.Data {
			if !allowed[field] {
				t.Fatalf("%s exposed unexpected field %q", label, field)
			}
		}
		var tokens int
		var steps []any
		if json.Unmarshal(body.Data["total_tokens"], &tokens) != nil || tokens != 12 ||
			json.Unmarshal(body.Data["steps"], &steps) != nil || len(steps) != 0 {
			t.Fatalf("%s summary mismatch: %s", label, result.Body.String())
		}
	}
	assertSummary("usage", usage)
	log := request(http.MethodGet, "/api/v1/chat/record/"+string(record.ID)+"/log", token)
	assertSummary("execution drawer", log)
	if got := request(http.MethodGet, "/api/v1/chat/record/"+string(record.ID)+"/usage", ""); got.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous usage returned %d", got.Code)
	}
	revision, err := service.Store.get(ctx, bindingKey(principal, "10"), new(Binding))
	if err != nil || service.Store.delete(ctx, bindingKey(principal, "10"), revision) != nil {
		t.Fatal("could not revoke datasource binding")
	}
	if got := request(http.MethodPost, path, token); got.Code != http.StatusNotFound {
		t.Fatalf("revoked recommendation returned %d", got.Code)
	}
	for _, suffix := range []string{"usage", "log"} {
		if got := request(http.MethodGet, "/api/v1/chat/record/"+string(record.ID)+"/"+suffix, token); got.Code != http.StatusNotFound {
			t.Fatalf("revoked %s metadata returned %d", suffix, got.Code)
		}
	}
}
