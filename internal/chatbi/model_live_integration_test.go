package chatbi

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

type liveRequestSignalTransport struct{ started chan<- struct{} }

func (transport liveRequestSignalTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	select {
	case transport.started <- struct{}{}:
	default:
	}
	return http.DefaultTransport.RoundTrip(request)
}

// Run explicitly with a locally provisioned OpenAI-compatible model. CI has
// no model or weights and therefore skips this semantic integration check.
func TestLiveMultiTurnModel(t *testing.T) {
	endpoint := os.Getenv("AGENTSQL_LOCAL_MODEL_ENDPOINT")
	if endpoint == "" {
		t.Skip("local model endpoint is not configured")
	}
	modelName := os.Getenv("AGENTSQL_LOCAL_MODEL_NAME")
	if modelName == "" {
		modelName = "agentsql-local-qwen"
	}
	model := OpenAIModel{}
	config := ModelConfig{Model: modelName, Endpoint: endpoint}
	columns := []Column{{Schema: "public", Table: "sales", Name: "region", DataType: "text"}, {Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"}}
	firstQuestion := "显示 public.sales 的 region 和 amount 两列，最多 3 条"
	first, err := model.Generate(context.Background(), config, "pg", firstQuestion, columns)
	if err != nil {
		t.Fatalf("first local model turn failed: %v", err)
	}
	second, err := model.GenerateWithHistory(context.Background(), config, "pg", "改成最多 2 条，其他条件不变", columns, []TurnContext{{Question: firstQuestion, SQL: first.SQL}})
	if err != nil {
		t.Fatalf("follow-up local model turn failed: %v", err)
	}
	sql := strings.ToLower(second.SQL)
	if !strings.Contains(sql, "region") || !strings.Contains(sql, "amount") || !strings.Contains(sql, "limit 2") {
		t.Fatalf("follow-up did not preserve requested columns and new limit: %s", second.SQL)
	}
	t.Logf("first SQL: %s", first.SQL)
	t.Logf("follow-up SQL: %s", second.SQL)
}

func TestLiveSemanticVocabularyModel(t *testing.T) {
	endpoint := os.Getenv("AGENTSQL_LOCAL_MODEL_ENDPOINT")
	if endpoint == "" {
		t.Skip("local model endpoint is not configured")
	}
	modelName := os.Getenv("AGENTSQL_LOCAL_MODEL_NAME")
	if modelName == "" {
		modelName = "agentsql-local-qwen"
	}
	model := OpenAIModel{}
	config := ModelConfig{Model: modelName, Endpoint: endpoint, AllowHTTP: true}
	columns := []Column{{Schema: "public", Table: "sales", Name: "region", DataType: "text"}, {Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"}}
	hints := []SemanticHint{{Word: "GMV", Description: "GMV means the total of the amount column in the currently authorized sales table."}}
	result, err := model.GenerateWithSemantics(context.Background(), config, "pg", "What is the GMV of public.sales? Return one row.", columns, nil, hints)
	if err != nil {
		t.Fatalf("local semantic model request failed: %v", err)
	}
	sql := strings.ToLower(result.SQL)
	if !strings.Contains(sql, "pg_catalog.sum") || !strings.Contains(sql, "amount") || !strings.Contains(sql, "sales") {
		t.Fatalf("local model did not apply the business term: %s", result.SQL)
	}
	t.Logf("semantic SQL: %s", result.SQL)
}

func TestLiveGroupedAggregateModel(t *testing.T) {
	endpoint := os.Getenv("AGENTSQL_LOCAL_MODEL_ENDPOINT")
	if endpoint == "" {
		t.Skip("local model endpoint is not configured")
	}
	modelName := os.Getenv("AGENTSQL_LOCAL_MODEL_NAME")
	if modelName == "" {
		modelName = "agentsql-local-qwen"
	}
	model := OpenAIModel{}
	config := ModelConfig{Model: modelName, Endpoint: endpoint, AllowHTTP: true}
	columns := []Column{{Schema: "public", Table: "sales", Name: "region", DataType: "text"}, {Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"}}
	result, err := model.GenerateWithHistory(context.Background(), config, "pg", "在 public.sales 中按 region 分组汇总 amount，按汇总金额降序，只显示前 3 个地区", columns, nil)
	if err != nil {
		t.Fatalf("local aggregate model request failed: %v", err)
	}
	sql := strings.ToLower(result.SQL)
	if !strings.Contains(sql, "group by") || !strings.Contains(sql, "region") || !strings.Contains(sql, "pg_catalog.sum(amount)") || !strings.Contains(sql, "limit 3") {
		t.Fatalf("local model missed the aggregate request: %s", result.SQL)
	}
	t.Logf("aggregate SQL: %s", result.SQL)
}

func TestLiveNarrativeModel(t *testing.T) {
	endpoint := os.Getenv("AGENTSQL_LOCAL_MODEL_ENDPOINT")
	if endpoint == "" {
		t.Skip("local model endpoint is not configured")
	}
	modelName := os.Getenv("AGENTSQL_LOCAL_MODEL_NAME")
	if modelName == "" {
		modelName = "agentsql-local-qwen"
	}
	data := QueryData{Fields: []string{"region", "amount"}, Data: []map[string]any{{"region": "east", "amount": 10}, {"region": "west", "amount": 20}}, AuditID: 1}
	answer, err := (OpenAIModel{}).Narrate(context.Background(), ModelConfig{Model: modelName, Endpoint: endpoint, AllowHTTP: true}, "analysis", "比较东部和西部金额", data)
	if err != nil || strings.TrimSpace(answer) == "" {
		t.Fatalf("local narrative model failed: %v", err)
	}
	t.Logf("local narrative returned %d runes", len([]rune(answer)))
}

func TestLiveNarrativeCancellationLeavesModelUsable(t *testing.T) {
	endpoint := os.Getenv("AGENTSQL_LOCAL_MODEL_ENDPOINT")
	if endpoint == "" {
		t.Skip("local model endpoint is not configured")
	}
	modelName := os.Getenv("AGENTSQL_LOCAL_MODEL_NAME")
	if modelName == "" {
		modelName = "agentsql-local-qwen"
	}
	config := ModelConfig{Model: modelName, Endpoint: endpoint, AllowHTTP: true}
	data := QueryData{Fields: []string{"region", "amount"}, Data: []map[string]any{
		{"region": "east", "amount": 10}, {"region": "west", "amount": 20},
	}, AuditID: 1}
	started := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		answer string
		err    error
	}
	interrupted := make(chan result, 1)
	go func() {
		answer, err := (OpenAIModel{Transport: liveRequestSignalTransport{started: started}}).Narrate(
			ctx, config, "analysis", "请详细比较两个地区的金额，并说明数据范围和不确定性。", data)
		interrupted <- result{answer, err}
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("local model request did not start")
	}
	time.Sleep(250 * time.Millisecond)
	cancel()
	select {
	case stopped := <-interrupted:
		if stopped.err != ErrModel || stopped.answer != "" {
			t.Fatalf("interrupted model request returned data: %q, %v", stopped.answer, stopped.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("interrupted model request did not stop promptly")
	}
	healthyCtx, stopHealthy := context.WithTimeout(context.Background(), 90*time.Second)
	defer stopHealthy()
	answer, err := (OpenAIModel{}).Narrate(healthyCtx, config, "analysis", "简要比较 east 和 west 的金额。", data)
	if err != nil || strings.TrimSpace(answer) == "" {
		t.Fatalf("local model did not recover after cancellation: %v", err)
	}
	t.Logf("model recovered after cancellation with %d answer runes", len([]rune(answer)))
}
