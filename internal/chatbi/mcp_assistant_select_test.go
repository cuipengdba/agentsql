package chatbi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAssistantCatalogModelSelectionUsesOnlyBoundedNamesAndIDs(t *testing.T) {
	calls := 0
	selected := `{"id":"82"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer synthetic-model-key" {
			t.Error("catalog selection used a different provider request")
		}
		var payload struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&payload) != nil || len(payload.Messages) != 2 ||
			!strings.Contains(payload.Messages[1].Content, `"id":"81"`) ||
			!strings.Contains(payload.Messages[1].Content, `"id":"82"`) ||
			strings.Contains(payload.Messages[1].Content, "private-binding") {
			t.Error("selection prompt leaked a binding or omitted catalog IDs")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
			"finish_reason": "stop", "message": map[string]string{"content": selected},
		}}})
	}))
	defer server.Close()
	model := OpenAIModel{}
	config := ModelConfig{Model: "fixture", Endpoint: server.URL + "/v1", APIKey: "synthetic-model-key"}
	entries := []assistantCatalogEntry{{CatalogID: "81", Datasource: "10", Name: "Sales"},
		{CatalogID: "82", Datasource: "11", Name: "Inventory"}}
	got, err := model.SelectAssistantCatalogSource(context.Background(), config, "Inventory total?", entries)
	if err != nil || got != "82" || calls != 1 {
		t.Fatalf("registered model choice failed: id=%s error=%v calls=%d", got, err, calls)
	}
	selected = `{"id":81}`
	got, err = model.SelectAssistantCatalogSource(context.Background(), config, "Sales total?", entries)
	if err != nil || got != "81" || calls != 2 {
		t.Fatalf("numeric catalog ID failed: id=%s error=%v calls=%d", got, err, calls)
	}
	selected = `{"id":"999"}`
	if _, err := model.SelectAssistantCatalogSource(context.Background(), config, "Inventory total?", entries); err == nil || calls != 3 {
		t.Fatal("unregistered model ID accepted")
	}
	selected = `{"id":""}`
	if _, err := model.SelectAssistantCatalogSource(context.Background(), config, "ambiguous", entries); err == nil || calls != 4 {
		t.Fatal("ambiguous model choice accepted")
	}
	selected = `{"id":"81","extra":"unexpected"}`
	if _, err := model.SelectAssistantCatalogSource(context.Background(), config, "Sales total?", entries); err == nil || calls != 5 {
		t.Fatal("unexpected model selection fields accepted")
	}
}
