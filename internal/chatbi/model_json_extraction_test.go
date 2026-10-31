package chatbi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestModelExtractsFirstValidOuterJSON(t *testing.T) {
	const sql = `SELECT '{"key": "value"}' FROM public.authorized_sales`
	for _, test := range []struct {
		name    string
		content string
		wantSQL string
	}{
		{"braces in string", `{"sql":"SELECT '{' FROM public.authorized_sales"}`, `SELECT '{' FROM public.authorized_sales`},
		{"fenced explanation", "The query is:\n```json\n{\"sql\":\"SELECT '{\\\"key\\\": \\\"value\\\"}' FROM public.authorized_sales\"}\n```\nDone.", sql},
		{"invalid block before answer", `{not JSON} then {"sql":"SELECT 1 FROM public.authorized_sales"}`, `SELECT 1 FROM public.authorized_sales`},
		{"invalid outer hides inner SQL", `{"outer":{"sql":"SELECT 1 FROM public.authorized_sales"}, broken}`, ""},
		{"truncated outer hides later SQL", `{"outer":{"sql":"SELECT 1 FROM public.authorized_sales"} then {"sql":"SELECT 2 FROM public.authorized_sales"}`, ""},
		{"first valid object wins", `{"note":1} {"sql":"SELECT 1 FROM public.authorized_sales"}`, ""},
		{"first valid array wins", `[] {"sql":"SELECT 1 FROM public.authorized_sales"}`, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": test.content}}},
				})
			}))
			defer server.Close()
			got, err := (OpenAIModel{}).Generate(context.Background(), ModelConfig{Model: "fixture", Endpoint: server.URL + "/v1"}, "pg", "question", []Column{{"public", "authorized_sales", "amount", "numeric"}})
			if test.wantSQL == "" {
				if err == nil {
					t.Fatalf("accepted invalid or later JSON: %q", got.SQL)
				}
				return
			}
			if err != nil || got.SQL != test.wantSQL {
				t.Fatalf("model result SQL = %q, err = %v; want %q", got.SQL, err, test.wantSQL)
			}
		})
	}
}
