package chatbi

import (
	"context"
	"os"
	"strings"
	"testing"
)

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
