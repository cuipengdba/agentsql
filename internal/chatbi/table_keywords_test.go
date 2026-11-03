package chatbi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOpenAITableKeywordsSendOnlyUserQuestionsAndFailClosed(t *testing.T) {
	var requestBody, responseBody atomic.Value
	requestBody.Store("")
	responseBody.Store(`{"choices":[{"message":{"content":"gross sales"},"finish_reason":"stop"}]}`)
	var status atomic.Int64
	status.Store(http.StatusOK)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected model path: %s", r.URL.Path)
		}
		var payload json.RawMessage
		if json.NewDecoder(r.Body).Decode(&payload) != nil {
			t.Error("invalid model request")
		}
		requestBody.Store(string(payload))
		w.WriteHeader(int(status.Load()))
		_, _ = w.Write([]byte(responseBody.Load().(string)))
	}))
	defer server.Close()
	model := OpenAIModel{}
	config := ModelConfig{Model: "local", Endpoint: server.URL}
	words, err := model.ExtractTableKeywords(context.Background(), config, "How much revenue?",
		[]TurnContext{{Question: "Show orders", SQL: "SELECT SECRET FROM private.payroll"}})
	if err != nil || words != "gross sales" || strings.Contains(requestBody.Load().(string), "SECRET") ||
		!strings.Contains(requestBody.Load().(string), "Show orders") || !strings.Contains(requestBody.Load().(string), "How much revenue?") {
		t.Fatalf("unsafe extraction payload or result: words=%q err=%v body=%q", words, err, requestBody.Load().(string))
	}
	responseBody.Store(`{"choices":[{"message":{"content":"EMPTY"},"finish_reason":"stop"}]}`)
	if words, err := model.ExtractTableKeywords(context.Background(), config, "hello", nil); err != nil || words != "" {
		t.Fatalf("EMPTY should use original question: %q %v", words, err)
	}
	status.Store(http.StatusInternalServerError)
	if _, err := model.ExtractTableKeywords(context.Background(), config, "orders", nil); !errors.Is(err, ErrModel) {
		t.Fatalf("failed keyword model did not fail closed: %v", err)
	}
}

func TestTableSelectorUsesExtractedVectorQueryOnlyWhenKeywordEnabled(t *testing.T) {
	columns := []Column{{Schema: "public", Table: "sales", Name: "id", DataType: "integer"},
		{Schema: "public", Table: "weather", Name: "id", DataType: "integer"}}
	for _, item := range []struct {
		enabled bool
		want    string
	}{{true, "weather"}, {false, "sales"}} {
		selected, _, err := selectModelTablesWithKeywords(context.Background(), tableFixtureEmbedding{},
			"orders", "weather", columns, nil, 1, 1, item.enabled, nil)
		if err != nil || len(selected) != 1 || selected[0].Table != item.want {
			t.Fatalf("keywordEnabled=%v selected=%v err=%v", item.enabled, selected, err)
		}
	}
}

func TestLocalQwenTableKeywordExtraction(t *testing.T) {
	endpoint := os.Getenv("AGENTSQL_TEST_LOCAL_MODEL_ENDPOINT")
	if endpoint == "" {
		t.Skip("local model endpoint not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	words, err := (OpenAIModel{}).ExtractTableKeywords(ctx,
		ModelConfig{Model: "/models/Qwen3-4B-Instruct-2507-Q4_K_M.gguf", Endpoint: endpoint},
		"统计本月订单销售额", []TurnContext{{Question: "上个月订单有多少", SQL: "SELECT secret FROM private.payroll"}})
	if err != nil || words == "" || len(words) > 1024 || strings.Contains(strings.ToLower(words), "private.payroll") {
		t.Fatalf("local model keyword extraction: words=%q err=%v", words, err)
	}
	t.Logf("local model keywords: %s", words)
}

func TestTableKeywordExpansionUsesOnlyEnabledCurrentDatasourceTerms(t *testing.T) {
	ctx := context.Background()
	service, p, _, _, _ := serviceFixture(t)
	for _, term := range []Terminology{
		{ID: 1, Word: "GMV", OtherWords: []string{"gross sales"}, Enabled: true, SpecificDS: true, DatasourceIDs: []int64{10}},
		{ID: 2, Word: "PRIVATE", OtherWords: []string{"gross sales"}, Enabled: true, SpecificDS: true, DatasourceIDs: []int64{11}},
		{ID: 3, Word: "DISABLED", OtherWords: []string{"gross sales"}, Enabled: false},
	} {
		if err := service.Store.put(ctx, semanticKey("terminology", p.Workspace, term.ID), term, 0); err != nil {
			t.Fatal(err)
		}
	}
	words, err := service.expandTableKeywords(ctx, p, "10", "gross sales")
	if err != nil || words != "gross sales,GMV" {
		t.Fatalf("cross-source or disabled synonym affected selection: %q %v", words, err)
	}
}

type keywordRevokingModel struct {
	tableCaptureModel
	change     func()
	extractErr error
}

func (model *keywordRevokingModel) ExtractTableKeywords(_ context.Context, _ ModelConfig, _ string, _ []TurnContext) (string, error) {
	if model.change != nil {
		model.change()
	}
	if model.extractErr != nil {
		return "", model.extractErr
	}
	return "sales", nil
}

func TestAskKeywordModelFailurePreventsGenerationAndQuery(t *testing.T) {
	ctx := context.Background()
	service, p, gateway, _, token := serviceFixture(t)
	gateway.columns = []Column{{Schema: "public", Table: "sales", Name: "id", DataType: "integer"},
		{Schema: "public", Table: "other", Name: "id", DataType: "integer"}}
	model := &keywordRevokingModel{extractErr: ErrModel}
	service.Model = model
	service.Embedding = tableFixtureEmbedding{}
	settings := defaultChatSettings()
	settings.Embedding.TableCount = 1
	if err := service.Store.put(ctx, chatSettingsKey(), settings, 0); err != nil {
		t.Fatal(err)
	}
	chat, err := service.Start(ctx, p, "10", "sales", 0)
	if err != nil {
		t.Fatal(err)
	}
	check := func(ctx context.Context) error { _, err := service.Identity.Verify(ctx, token); return err }
	if _, err := service.Ask(ctx, p, chat.ID, "sales", check, nil); !errors.Is(err, ErrModel) ||
		len(model.columns) != 0 || gateway.queries != 0 {
		t.Fatalf("failed extraction reached generator or query: columns=%v queries=%d err=%v", model.columns, gateway.queries, err)
	}
}

func TestAskKeywordExtractionClosesAfterPermissionRevocation(t *testing.T) {
	ctx := context.Background()
	service, p, gateway, _, token := serviceFixture(t)
	gateway.columns = []Column{{Schema: "public", Table: "sales", Name: "region", DataType: "text"},
		{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"},
		{Schema: "public", Table: "other", Name: "id", DataType: "integer"}}
	rule := testActiveRowPermission(t, "east")
	if err := service.Store.put(ctx, permissionRuleKey("1", rule.ID), rule, 0); err != nil {
		t.Fatal(err)
	}
	model := &keywordRevokingModel{change: func() {
		revoked := rule
		revoked.Enable = false
		if err := service.Store.put(ctx, permissionRuleKey("1", rule.ID), revoked, 1); err != nil {
			t.Fatal(err)
		}
	}}
	service.Model = model
	service.Embedding = tableFixtureEmbedding{}
	settings := defaultChatSettings()
	settings.Embedding.TableCount = 1
	if err := service.Store.put(ctx, chatSettingsKey(), settings, 0); err != nil {
		t.Fatal(err)
	}
	chat, err := service.Start(ctx, p, "10", "sales", 0)
	if err != nil {
		t.Fatal(err)
	}
	check := func(ctx context.Context) error { _, err := service.Identity.Verify(ctx, token); return err }
	if _, err := service.Ask(ctx, p, chat.ID, "sales", check, nil); !errors.Is(err, ErrGateway) ||
		len(model.columns) != 0 || gateway.queries != 0 {
		t.Fatalf("revoked context reached generator or query: columns=%v queries=%d err=%v", model.columns, gateway.queries, err)
	}
}
