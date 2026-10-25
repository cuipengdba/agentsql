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
