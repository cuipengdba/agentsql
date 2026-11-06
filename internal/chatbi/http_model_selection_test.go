package chatbi

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestActiveCatalogQuestionAndMCPUseSelectedMappedModel(t *testing.T) {
	ctx := context.Background()
	service, principal, gateway, generator, token := serviceFixture(t)
	activateModelCatalogFixture(t, service.Store, ModelConfig{ID: "30", Model: "global", Endpoint: "https://example.invalid/v1"})
	selectedID := ID("31")
	if err := service.Store.put(ctx, modelCatalogKey(selectedID),
		ModelConfig{ID: selectedID, Model: "specialized", Endpoint: "https://example.invalid/v1"}, 0); err != nil {
		t.Fatal(err)
	}
	if err := service.Store.put(ctx, modelMappingKey(selectedID, principal.Workspace),
		catalogModelMapping{Model: selectedID, Workspace: principal.Workspace}, 0); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	chat, err := service.Start(ctx, principal, "10", "fixture", 0)
	if err != nil {
		t.Fatal(err)
	}
	question := accountRequest(t, handler, http.MethodPost, "/api/v1/chat/question",
		`{"chat_id":`+string(chat.ID)+`,"question":"amount?","custom_model":31}`, token)
	if question.Code != http.StatusOK || !strings.Contains(question.Body.String(), `"type":"sql"`) ||
		generator.last.ID != selectedID || gateway.queries != 1 {
		t.Fatalf("chat ignored selected model: %d %s, selected=%s queries=%d", question.Code, question.Body.String(), generator.last.ID, gateway.queries)
	}
	started := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_start", `{"token":"`+token+`"}`, "")
	var session struct {
		Chat ID `json:"chat_id"`
	}
	if started.Code != http.StatusOK || decodeMCPData(started.Body.Bytes(), &session) != nil {
		t.Fatalf("MCP start: %d %s", started.Code, started.Body.String())
	}
	mcp := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_question",
		`{"token":"`+token+`","chat_id":`+string(session.Chat)+`,"question":"amount?","custom_model":"31","stream":false}`, "")
	if mcp.Code != http.StatusOK || !strings.Contains(mcp.Body.String(), `"success":true`) ||
		generator.last.ID != selectedID || gateway.queries != 2 {
		t.Fatalf("MCP ignored selected model: %d %s, selected=%s queries=%d", mcp.Code, mcp.Body.String(), generator.last.ID, gateway.queries)
	}
	for _, value := range []string{`999`, `{"id":31}`, `"not-an-id"`} {
		denied := accountRequest(t, handler, http.MethodPost, "/api/v1/mcp/mcp_question",
			`{"token":"`+token+`","chat_id":`+string(session.Chat)+`,"question":"amount?","custom_model":`+value+`,"stream":false}`, "")
		if denied.Code == http.StatusOK || gateway.queries != 2 || generator.calls != 2 {
			t.Fatalf("invalid or unmapped model reached MCP query: %d %s", denied.Code, denied.Body.String())
		}
	}
}
