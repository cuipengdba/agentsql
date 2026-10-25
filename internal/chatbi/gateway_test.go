package chatbi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestControlledGatewayUsesAuthenticatedIdentityAndDurableAudit(t *testing.T) {
	for _, mode := range []string{"allow", "warn", "missing-audit", "deny"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			resolved := 0
			server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
			server.AddTool(&mcp.Tool{Name: "query", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				calls++
				var args map[string]string
				if json.Unmarshal(request.Params.Arguments, &args) != nil || args["datasource_id"] != "gateway-datasource" || args["sql"] != "SELECT amount FROM public.sales" {
					t.Error("incorrect gateway scope")
				}
				audit := 1
				if mode == "missing-audit" {
					audit = 0
				}
				decision := "allow"
				if mode == "deny" {
					decision = "deny"
				}
				if mode == "warn" {
					decision = "warn"
				}
				data, _ := json.Marshal(map[string]any{"decision": decision, "data": map[string]any{"audit_id": audit, "result": map[string]any{"Columns": []string{"amount"}, "Rows": [][]string{{"123"}}}}})
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}, nil
			})
			stream := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{JSONResponse: true})
			httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/integrations/sqlbot/resolve" {
					resolved++
					if r.Header.Get("Authorization") != "Bearer connector-secret" {
						t.Error("missing connector credential")
					}
					var input map[string]string
					_ = json.NewDecoder(r.Body).Decode(&input)
					if input["workspace_id"] != "1" || input["sqlbot_user_id"] != "7" || input["datasource_name"] != "sales" || input["binding_id"] != "binding-1" {
						t.Error("untrusted identity assertion")
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": resolvedBinding{ID: "binding-1", Revision: 1, Agent: "agent-1", Datasource: "gateway-datasource", Schema: "public", Dialect: "pg", Token: "SQB1.synthetic", Expires: time.Now().Add(time.Minute)}})
					return
				}
				if r.Header.Get("Authorization") != "Bearer agent-secret" || r.Header.Get("X-AgentSQL-User-Authorization") != "Bearer SQB1.synthetic" {
					t.Error("gateway dual credentials missing")
				}
				stream.ServeHTTP(w, r)
			}))
			defer httpServer.Close()
			gateway := &ControlledGateway{Endpoint: httpServer.URL + "/mcp"}
			p := Principal{User: User{ID: "7"}, Workspace: "1"}
			binding := Binding{Datasource: "10", Workspace: "1", User: "7", Name: "sales", Dialect: "pg", BindingID: "binding-1", ConnectorToken: "connector-secret", AgentKey: "agent-secret"}
			result, err := gateway.Query(context.Background(), p, binding, "SELECT amount FROM public.sales")
			if mode == "allow" || mode == "warn" {
				if err != nil || result.AuditID != 1 || result.Data[0]["amount"] != "123" {
					t.Fatalf("controlled result failed: %v", err)
				}
			} else if err == nil || result.Data != nil {
				t.Fatal("denied or unaudited results released")
			}
			if calls != 1 || resolved != 1 {
				t.Fatalf("unexpected retry or skipped identity resolution: calls=%d resolve=%d", calls, resolved)
			}
			p.Workspace = "2"
			if _, err := gateway.Query(context.Background(), p, binding, "SELECT 1"); err == nil || resolved != 1 {
				t.Fatal("cross-workspace request reached gateway")
			}
		})
	}
}
