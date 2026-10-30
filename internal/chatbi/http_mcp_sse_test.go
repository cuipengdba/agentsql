package chatbi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestLegacyMCPSSEUsesCheckedHelperRoutes(t *testing.T) {
	service, _, gateway, _, token := serviceFixture(t)
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "acceptance", Version: "test"}, nil)
	session, err := client.Connect(ctx, &mcp.SSEClientTransport{Endpoint: server.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range listed.Tools {
		names[tool.Name] = true
		schema, err := json.Marshal(tool.InputSchema)
		if err != nil || bytes.Contains(schema, []byte(`"required":null`)) {
			t.Fatalf("invalid MCP input schema for %s", tool.Name)
		}
	}
	for _, name := range []string{"access_token", "mcp_start", "mcp_ws_list", "mcp_datasource_list", "mcp_model_list", "mcp_question"} {
		if !names[name] {
			t.Fatalf("legacy MCP tool %s missing", name)
		}
	}
	if names["mcp_assistant"] {
		t.Fatal("unsupported unauthenticated assistant tool advertised")
	}
	call := func(name string, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s transport: %v", name, err)
		}
		return result
	}
	text := func(result *mcp.CallToolResult) string {
		t.Helper()
		if len(result.Content) != 1 {
			t.Fatalf("expected one text block, got %d", len(result.Content))
		}
		block, ok := result.Content[0].(*mcp.TextContent)
		if !ok {
			t.Fatalf("unexpected MCP content: %T", result.Content[0])
		}
		return block.Text
	}
	ws := call("mcp_ws_list", map[string]any{"token": token})
	if ws.IsError || !strings.Contains(text(ws), `"code":0`) || !strings.Contains(text(ws), `"name":"One"`) {
		t.Fatalf("legacy MCP workspace listing failed: %t %s", ws.IsError, text(ws))
	}
	start := call("mcp_start", map[string]any{"token": token})
	var envelope struct {
		Data struct {
			Chat ID `json:"chat_id"`
		} `json:"data"`
	}
	if start.IsError || json.Unmarshal([]byte(text(start)), &envelope) != nil || !envelope.Data.Chat.Valid() {
		t.Fatalf("legacy MCP start failed: %t %s", start.IsError, text(start))
	}
	answer := call("mcp_question", map[string]any{"token": token, "chat_id": string(envelope.Data.Chat), "question": "sales?", "stream": false})
	if answer.IsError || !strings.Contains(text(answer), `"success":true`) || gateway.queries != 1 {
		t.Fatalf("legacy MCP question bypassed audited gateway: %t %s", answer.IsError, text(answer))
	}
	denied := call("mcp_question", map[string]any{"token": "invalid", "chat_id": string(envelope.Data.Chat), "question": "sales?", "stream": false})
	if !denied.IsError || gateway.queries != 1 || strings.Contains(text(denied), "123") {
		t.Fatal("invalid MCP token reached or leaked gateway rows")
	}
	gateway.audit = 0
	unauditedStart := call("mcp_start", map[string]any{"token": token})
	var unaudited struct {
		Data struct {
			Chat ID `json:"chat_id"`
		} `json:"data"`
	}
	if unauditedStart.IsError || json.Unmarshal([]byte(text(unauditedStart)), &unaudited) != nil {
		t.Fatal("could not prepare MCP audit-failure case")
	}
	unauditedAnswer := call("mcp_question", map[string]any{"token": token, "chat_id": string(unaudited.Data.Chat), "question": "sales?", "stream": false})
	if !unauditedAnswer.IsError || gateway.queries != 2 || strings.Contains(text(unauditedAnswer), "123") {
		t.Fatal("MCP SSE released a result without gateway audit")
	}
	model := call("mcp_model_list", map[string]any{"token": token, "oid": 1})
	if model.IsError || !strings.Contains(text(model), `"code":0`) {
		t.Fatal("authenticated MCP model list failed")
	}
	if anonymous := call("mcp_model_list", map[string]any{"oid": 1}); !anonymous.IsError {
		t.Fatal("anonymous MCP model enumeration succeeded")
	}
}

func TestLegacyMCPSSERejectsCrossOriginBeforeSession(t *testing.T) {
	service, _, _, _, _ := serviceFixture(t)
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	request, err := http.NewRequest(http.MethodGet, server.URL+"/mcp", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", "https://evil.invalid")
	response, err := (&http.Client{Timeout: 3 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin SSE session accepted: %d", response.StatusCode)
	}
}
