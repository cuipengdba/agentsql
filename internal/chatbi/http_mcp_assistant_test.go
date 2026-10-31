package chatbi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func assistantCatalogRequest(t *testing.T, handler http.Handler, input mcpAssistantInput) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/mcp/mcp_assistant", bytes.NewReader(raw))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestMCPAssistantOneRegisteredCatalogSourceUsesControlledQuery(t *testing.T) {
	service, p, gateway, model, _ := serviceFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":[{"id":81,"name":"Orders","password":"must-not-leak"}]}`))
	}))
	defer server.Close()
	auth := `[{"key":"token","value":"synthetic-token","target":"header"}]`
	bridge := AssistantCatalogBridge{Endpoint: server.URL + "/sources", Authorization: auth,
		Workspace: p.Workspace, User: p.User.ID, Sources: []AssistantCatalogSource{{CatalogID: "81", Datasource: "10"}},
		AllowedCIDRs: []string{"127.0.0.1/32"}, AllowHTTP: true}
	if err := service.Store.ConfigureConnections(context.Background(), ConnectionConfig{AssistantCatalogs: []AssistantCatalogBridge{bridge}}); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	stream := false
	input := mcpAssistantInput{Question: "How many orders?", URL: bridge.Endpoint, Authorization: auth, Stream: &stream}
	result := assistantCatalogRequest(t, handler, input)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"success":true`) ||
		!strings.Contains(result.Body.String(), `"amount":"123"`) || gateway.queries != 1 || model.calls != 1 ||
		strings.Contains(result.Body.String(), "must-not-leak") {
		t.Fatalf("controlled catalog question failed: HTTP %d %s, model=%d gateway=%d", result.Code, result.Body.String(), model.calls, gateway.queries)
	}
	input.Stream = nil
	streamed := assistantCatalogRequest(t, handler, input)
	if streamed.Code != http.StatusOK || !strings.Contains(streamed.Header().Get("Content-Type"), "text/event-stream") ||
		!strings.Contains(streamed.Body.String(), `data:{"content":"execute-success","type":"sql-data"}`) ||
		!strings.Contains(streamed.Body.String(), `data:{"type":"finish"}`) ||
		strings.Contains(streamed.Body.String(), "must-not-leak") || gateway.queries != 2 {
		t.Fatalf("controlled catalog stream failed: HTTP %d %s", streamed.Code, streamed.Body.String())
	}
	input.Authorization = `[{"key":"token","value":"wrong-token","target":"header"}]`
	denied := assistantCatalogRequest(t, handler, input)
	if denied.Code != http.StatusUnauthorized || gateway.queries != 2 || strings.Contains(denied.Body.String(), "wrong-token") {
		t.Fatalf("wrong catalog credential reached gateway or leaked: HTTP %d %s", denied.Code, denied.Body.String())
	}
	audits, err := service.Store.list(context.Background(), "management-audit", "system", "system")
	if err != nil {
		t.Fatal(err)
	}
	accesses := 0
	for _, audit := range audits {
		if strings.Contains(string(audit), "synthetic-token") || strings.Contains(string(audit), bridge.Endpoint) {
			t.Fatal("catalog credential or URL leaked into management audit")
		}
		if strings.Contains(string(audit), "assistant-catalog.access") {
			accesses++
		}
	}
	if accesses != 2 {
		t.Fatalf("successful catalog questions lacked scoped audit: %d", accesses)
	}
}

func TestMCPAssistantCatalogRevocationDuringModelPreventsQuery(t *testing.T) {
	service, p, gateway, model, _ := serviceFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":[{"id":81,"name":"Orders"}]}`))
	}))
	defer server.Close()
	auth := `[{"key":"token","value":"synthetic-token","target":"header"}]`
	bridge := AssistantCatalogBridge{Endpoint: server.URL + "/sources", Authorization: auth,
		Workspace: p.Workspace, User: p.User.ID, Sources: []AssistantCatalogSource{{CatalogID: "81", Datasource: "10"}},
		AllowedCIDRs: []string{"127.0.0.1/32"}, AllowHTTP: true}
	if err := service.Store.ConfigureConnections(context.Background(), ConnectionConfig{AssistantCatalogs: []AssistantCatalogBridge{bridge}}); err != nil {
		t.Fatal(err)
	}
	var revokeErr error
	model.after = func() {
		disabled := false
		bridge.Enabled = &disabled
		revokeErr = service.Store.ConfigureConnections(context.Background(), ConnectionConfig{AssistantCatalogs: []AssistantCatalogBridge{bridge}})
	}
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	stream := false
	result := assistantCatalogRequest(t, handler, mcpAssistantInput{Question: "How many orders?", URL: bridge.Endpoint, Authorization: auth, Stream: &stream})
	if revokeErr != nil || result.Code == http.StatusOK || gateway.queries != 0 || strings.Contains(result.Body.String(), "123") {
		t.Fatalf("revoked catalog released data: HTTP %d %s, revoke=%v", result.Code, result.Body.String(), revokeErr)
	}
}

func TestMCPAssistantRejectsMultipleSourcesBeforeModelOrGateway(t *testing.T) {
	service, p, gateway, model, _ := serviceFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":[{"id":81,"name":"Orders"},{"id":82,"name":"Other"}]}`))
	}))
	defer server.Close()
	auth := `[{"key":"token","value":"synthetic-token","target":"header"}]`
	bridge := AssistantCatalogBridge{Endpoint: server.URL + "/sources", Authorization: auth,
		Workspace: p.Workspace, User: p.User.ID,
		Sources:      []AssistantCatalogSource{{CatalogID: "81", Datasource: "10"}, {CatalogID: "82", Datasource: "10"}},
		AllowedCIDRs: []string{"127.0.0.1/32"}, AllowHTTP: true}
	if err := service.Store.ConfigureConnections(context.Background(), ConnectionConfig{AssistantCatalogs: []AssistantCatalogBridge{bridge}}); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	result := assistantCatalogRequest(t, handler, mcpAssistantInput{Question: "How many orders?", URL: bridge.Endpoint, Authorization: auth})
	if result.Code != http.StatusUnprocessableEntity || gateway.queries != 0 || model.calls != 0 {
		t.Fatalf("multiple sources were silently chosen: HTTP %d %s", result.Code, result.Body.String())
	}
}

func TestMCPAssistantAuditFailureStopsModelAndQuery(t *testing.T) {
	service, p, gateway, model, _ := serviceFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":[{"id":81,"name":"Orders"}]}`))
	}))
	defer server.Close()
	auth := `[{"key":"token","value":"synthetic-token","target":"header"}]`
	bridge := AssistantCatalogBridge{Endpoint: server.URL + "/sources", Authorization: auth,
		Workspace: p.Workspace, User: p.User.ID, Sources: []AssistantCatalogSource{{CatalogID: "81", Datasource: "10"}},
		AllowedCIDRs: []string{"127.0.0.1/32"}, AllowHTTP: true}
	ctx := context.Background()
	if err := service.Store.ConfigureConnections(ctx, ConnectionConfig{AssistantCatalogs: []AssistantCatalogBridge{bridge}}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Store.db.ExecContext(ctx, `CREATE TRIGGER reject_catalog_access_audit BEFORE INSERT ON chatbi_objects WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(ABORT,'synthetic audit fault'); END`); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	result := assistantCatalogRequest(t, handler, mcpAssistantInput{Question: "How many orders?", URL: bridge.Endpoint, Authorization: auth})
	if result.Code == http.StatusOK || gateway.queries != 0 || model.calls != 0 || strings.Contains(result.Body.String(), "synthetic-token") {
		t.Fatalf("catalog access ran after audit failure: HTTP %d %s", result.Code, result.Body.String())
	}
}
