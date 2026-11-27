package chatbi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

func TestGatewayResolveDistinguishesRevokedMappingFromAuditFailure(t *testing.T) {
	status := http.StatusUnauthorized
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
	defer server.Close()
	gateway := &ControlledGateway{Endpoint: server.URL + "/mcp", AllowHTTP: true}
	principal := Principal{User: User{ID: "7"}, Workspace: "1"}
	binding := Binding{Datasource: "10", Workspace: "1", User: "7", Name: "sales", Dialect: "pg", BindingID: "binding-1", ConnectorToken: "connector-secret", AgentKey: "agent-secret"}
	_, err := gateway.resolve(context.Background(), principal, binding)
	if !errors.Is(err, ErrGatewaySourceDenied) || !errors.Is(err, ErrGateway) {
		t.Fatalf("401 must identify the revoked connector and remain a gateway refusal: %v", err)
	}
	status = http.StatusForbidden
	_, err = gateway.resolve(context.Background(), principal, binding)
	if !errors.Is(err, ErrGatewaySourceDenied) || !errors.Is(err, ErrGateway) {
		t.Fatalf("403 must identify the revoked mapping and remain a gateway refusal: %v", err)
	}
	status = http.StatusServiceUnavailable
	_, err = gateway.resolve(context.Background(), principal, binding)
	if !errors.Is(err, ErrGateway) || !errors.Is(err, ErrGatewayIdentityUnavailable) || errors.Is(err, ErrGatewaySourceDenied) {
		t.Fatalf("audit outage must remain a hard gateway failure: %v", err)
	}
	if got := tableIndexPrewarmFailureReason(err); got != "identity_unavailable" {
		t.Fatalf("audit outage prewarm category = %q, want identity_unavailable", got)
	}
}

func TestControlledGatewayUsesAuthenticatedIdentityAndDurableAudit(t *testing.T) {
	for _, mode := range []string{"allow", "warn", "missing-audit", "deny", "unsupported-binder-mode"} {
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
				if mode == "deny" || mode == "unsupported-binder-mode" {
					decision = "deny"
				}
				if mode == "warn" {
					decision = "warn"
				}
				envelope := map[string]any{"decision": decision, "data": map[string]any{"audit_id": audit, "result": map[string]any{"Columns": []string{"amount"}, "Rows": [][]string{{"123"}}}}}
				if mode == "unsupported-binder-mode" {
					envelope["error_code"] = "AUTH_BINDER_MODE_REQUIRED"
					envelope["reason"] = "private fixture gateway detail"
				}
				data, _ := json.Marshal(envelope)
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
			if mode == "unsupported-binder-mode" && (!errors.Is(err, ErrGateway) ||
				!strings.Contains(err.Error(), "current PostgreSQL column authorization mode") ||
				strings.Contains(err.Error(), "private fixture gateway detail")) {
				t.Fatalf("unsupported query must provide safe mode guidance: %v", err)
			}
			if calls != 1 || resolved != 1 {
				t.Fatalf("unexpected retry or skipped identity resolution: calls=%d resolve=%d", calls, resolved)
			}
			binding.SelectedTables = `[{"schema":"public","table":"sales"}]`
			if _, err := gateway.Query(context.Background(), p, binding, "SELECT name FROM public.people"); err == nil || calls != 1 || resolved != 1 {
				t.Fatal("unselected database table reached gateway resolution or execution")
			}
			p.Workspace = "2"
			if _, err := gateway.Query(context.Background(), p, binding, "SELECT 1"); err == nil || resolved != 1 {
				t.Fatal("cross-workspace request reached gateway")
			}
		})
	}
}

func TestGatewayResolveUsesManagedChildNameBehindExcelDisplayName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/integrations/sqlbot/resolve" ||
			r.Header.Get("Authorization") != "Bearer child-connector" {
			t.Error("child connector identity was not used")
			http.Error(w, "invalid", http.StatusForbidden)
			return
		}
		var body map[string]string
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["datasource_name"] != "agentsql-import-33333333333333333333333333333333" ||
			body["binding_id"] != "child-binding" {
			t.Error("display name was confused with child binding identity")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": resolvedBinding{
			ID: "child-binding", Revision: 1, Agent: "agent-1", Datasource: "read-1", Schema: "agentsql_files",
			Dialect: "pg", Token: "SQB1.synthetic", Expires: time.Now().Add(time.Minute),
		}})
	}))
	defer server.Close()
	gateway := &ControlledGateway{Endpoint: server.URL + "/mcp", AllowHTTP: true}
	p := Principal{User: User{ID: "7"}, Workspace: "1"}
	b := Binding{Datasource: "123", Workspace: "1", User: "7", Name: "Sales CSV", Type: "excel",
		ResolveName: "agentsql-import-33333333333333333333333333333333", Dialect: "pg",
		BindingID: "child-binding", ConnectorToken: "child-connector", AgentKey: "agent-secret"}
	_, err := gateway.resolve(context.Background(), p, b)
	if err != nil {
		t.Fatalf("child binding could not resolve: %v", err)
	}
}

func TestManagedGatewayNullBitmapAndLegacyRows(t *testing.T) {
	const table = "agentsql_file_752687ad0e4cf6a22385c9444fb203c3"
	config, err := encodeSQLBotExcelConfig("orders.csv", []importedFileSheet{{SheetName: "Sheet1", TableName: table, Rows: 3}})
	require.NoError(t, err)
	p := Principal{User: User{ID: "7"}, Workspace: "1"}
	for _, test := range []struct {
		name   string
		bitmap []byte
		want   any
		deny   bool
	}{
		{name: "null remains nil", bitmap: []byte{1}},
		{name: "older gateway string rows", want: ""},
		{name: "cannot hide nonempty row", bitmap: []byte{4}, deny: true},
		{name: "invalid padding", bitmap: []byte{8}, deny: true},
		{name: "out of bounds", bitmap: []byte{1, 1}, deny: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
			server.AddTool(&mcp.Tool{Name: "query", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				encoded, err := json.Marshal(map[string]any{"decision": "allow", "data": map[string]any{
					"audit_id": 1, "null_bitmap": test.bitmap, "result": map[string]any{"Columns": []string{"amount"}, "Rows": [][]string{{""}, {""}, {"0"}}},
				}})
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}}}, err
			})
			stream := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{JSONResponse: true})
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/integrations/sqlbot/resolve" {
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": resolvedBinding{
						ID: "child-binding", Revision: 1, Agent: "agent-1", Datasource: "read-1", Schema: "agentsql_files",
						Dialect: "pg", Token: "SQB1.synthetic", Expires: time.Now().Add(time.Minute),
					}})
					return
				}
				stream.ServeHTTP(w, r)
			}))
			defer endpoint.Close()
			gateway := &ControlledGateway{Endpoint: endpoint.URL + "/mcp", AllowHTTP: true}
			binding := Binding{Datasource: "11", Workspace: "1", User: "7", Name: "orders", Type: "excel",
				ManagedFileID: "22222222222222222222222222222222", Configuration: config,
				ResolveName: "agentsql-import-33333333333333333333333333333333", Dialect: "pg", BindingID: "child-binding",
				ConnectorToken: "connector-secret", AgentKey: "agent-secret"}
			result, err := gateway.Query(context.Background(), p, binding, "SELECT amount FROM agentsql_files."+table)
			if test.deny {
				require.ErrorIs(t, err, ErrGateway)
				require.Empty(t, result.Data)
				return
			}
			require.NoError(t, err)
			require.Equal(t, []map[string]any{{"amount": test.want}, {"amount": ""}, {"amount": "0"}}, result.Data)
			require.EqualValues(t, 1, result.AuditID)
		})
	}
}
