package chatbi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestModelRequestAndFailureIsolation(t *testing.T) {
	for _, mode := range []string{"success", "error", "truncated", "bad-json", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer private-key" {
					t.Error("invalid provider request")
				}
				raw, _ := io.ReadAll(r.Body)
				if !strings.Contains(string(raw), "authorized_sales") || strings.Contains(string(raw), "unauthorized_payroll") {
					t.Error("schema boundary missing")
				}
				switch mode {
				case "error":
					w.WriteHeader(500)
					_, _ = w.Write([]byte("provider-secret-error"))
				case "redirect":
					w.Header().Set("Location", "/stolen")
					w.WriteHeader(307)
				case "bad-json":
					_, _ = w.Write([]byte("not-json"))
				default:
					finish := "stop"
					if mode == "truncated" {
						finish = "length"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": finish, "message": map[string]string{"content": `{"sql":"SELECT amount FROM public.authorized_sales"}`}}}, "usage": map[string]int{"prompt_tokens": 20, "completion_tokens": 10, "total_tokens": 30}})
				}
			}))
			defer server.Close()
			result, err := (OpenAIModel{}).Generate(context.Background(), ModelConfig{Model: "fixture", Endpoint: server.URL + "/v1", APIKey: "private-key"}, "pg", "amount", []Column{{"public", "authorized_sales", "amount", "numeric"}})
			if mode == "success" {
				if err != nil || result.TotalTokens != 30 || result.SQL == "" {
					t.Fatal("model result failed")
				}
			} else if err == nil || strings.Contains(err.Error(), "provider-secret") {
				t.Fatal("provider failure leaked or succeeded")
			}
			if calls != 1 {
				t.Fatal("provider request retried or redirected")
			}
		})
	}
}

func TestModelCancellationAndEndpointPolicy(t *testing.T) {
	for _, endpoint := range []string{"http://external.example/v1", "https://user:secret@example.com/v1", "file:///tmp/model", "https://example.com/v1?key=secret"} {
		if _, err := validateEndpoint(endpoint, false); err == nil {
			t.Fatalf("unsafe endpoint accepted: %s", endpoint)
		}
	}
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (OpenAIModel{}).Generate(ctx, ModelConfig{Model: "fixture", Endpoint: server.URL + "/v1"}, "pg", "amount", []Column{{"public", "sales", "amount", "numeric"}})
	if err == nil || called {
		t.Fatal("cancelled model request ran")
	}
}

func TestModelHistoryIsBoundedContextWithoutResultRows(t *testing.T) {
	called := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		var payload struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || len(payload.Messages) != 2 {
			t.Fatal("invalid model payload")
		}
		prompt := payload.Messages[1].Content
		if !strings.Contains(prompt, "previous question") || !strings.Contains(prompt, "SELECT amount FROM public.sales") || !strings.Contains(prompt, "current question") || strings.Contains(prompt, "SECRET_ROW") {
			t.Fatal("context was missing or result data reached the model")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": `{"sql":"SELECT amount FROM public.sales"}`}}}})
	}))
	defer server.Close()
	model := OpenAIModel{}
	config := ModelConfig{Model: "fixture", Endpoint: server.URL + "/v1"}
	_, err := model.GenerateWithHistory(context.Background(), config, "pg", "current question", []Column{{"public", "sales", "amount", "numeric"}}, []TurnContext{{Question: "previous question", SQL: "SELECT amount FROM public.sales"}})
	if err != nil || called != 1 {
		t.Fatalf("contextual generation failed: %v", err)
	}
	tooMany := []TurnContext{{Question: "a", SQL: "SELECT 1"}, {Question: "b", SQL: "SELECT 2"}, {Question: "c", SQL: "SELECT 3"}, {Question: "d", SQL: "SELECT 4"}}
	if _, err := model.GenerateWithHistory(context.Background(), config, "pg", "current question", []Column{{"public", "sales", "amount", "numeric"}}, tooMany); err == nil || called != 1 {
		t.Fatal("oversized history reached provider")
	}
}
