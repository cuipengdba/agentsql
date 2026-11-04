package chatbi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestOriginalFieldEnumUsesCurrentVisibleColumnAndAuditedGateway(t *testing.T) {
	service, principal, gateway, _, token := serviceFixture(t)
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	fieldID := strconv.FormatInt(browserFieldID(browserTableID("10", "public", "sales"), "amount"), 10)
	request := func(datasource, id string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/api/v1/datasource/fieldEnum/"+datasource+"/"+id, nil)
		r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	result := request("10", fieldID)
	var envelope struct {
		Data []string `json:"data"`
	}
	if result.Code != http.StatusOK || json.Unmarshal(result.Body.Bytes(), &envelope) != nil || len(envelope.Data) != 1 || envelope.Data[0] != "123" ||
		gateway.lastQuery != `SELECT DISTINCT "amount" FROM "public"."sales" LIMIT 100` {
		t.Fatalf("original field enum: %d %s sql=%s", result.Code, result.Body.String(), gateway.lastQuery)
	}
	before := gateway.queries
	for _, pair := range [][2]string{{"11", fieldID}, {"10", "999"}, {"10", "0"}, {"10", "01"}} {
		if denied := request(pair[0], pair[1]); denied.Code == http.StatusOK || gateway.queries != before {
			t.Fatalf("invalid or foreign field reached gateway: %d %s", denied.Code, denied.Body.String())
		}
	}
	gateway.columns = []Column{{"public", "sales", "other", "text"}}
	if denied := request("10", fieldID); denied.Code == http.StatusOK || gateway.queries != before {
		t.Fatal("a field removed from the live gateway schema still returned values")
	}
	gateway.columns = nil
	gateway.schemaErr = ErrGateway
	if denied := request("10", fieldID); denied.Code == http.StatusOK || strings.Contains(denied.Body.String(), "123") || gateway.queries != before {
		t.Fatal("gateway schema fault returned field values")
	}
	gateway.schemaErr = nil
	gateway.audit = 0
	if denied := request("10", fieldID); denied.Code == http.StatusOK || strings.Contains(denied.Body.String(), "123") {
		t.Fatal("unaudited field values returned")
	}
	gateway.audit = 1
	gateway.afterQuery = func() {
		gateway.afterQuery = nil
		if err := service.Store.delete(context.Background(), bindingKey(principal, "10"), 1); err != nil {
			t.Fatal(err)
		}
	}
	if denied := request("10", fieldID); denied.Code == http.StatusOK || strings.Contains(denied.Body.String(), "123") {
		t.Fatal("revoked field values returned after gateway query")
	}
}

func TestOriginalFieldEnumQualifiesMySQLSchema(t *testing.T) {
	service, principal, gateway, _, token := serviceFixture(t)
	binding, err := service.binding(context.Background(), principal, "10")
	if err != nil {
		t.Fatal(err)
	}
	binding.Dialect = "mysql"
	if err := service.Store.put(context.Background(), bindingKey(principal, "10"), binding, 1); err != nil {
		t.Fatal(err)
	}
	gateway.columns = []Column{{"agentsql_demo", "orders", "amount", "decimal"}}
	gateway.queryData = &QueryData{Fields: []string{"amount"}, Data: []map[string]any{{"amount": "123"}, {"amount": "123"}, {"amount": "456"}}, AuditID: gateway.audit}
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	fieldID := strconv.FormatInt(browserFieldID(browserTableID("10", "agentsql_demo", "orders"), "amount"), 10)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/datasource/fieldEnum/10/"+fieldID, nil)
	r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	var envelope struct {
		Data []string `json:"data"`
	}
	if w.Code != http.StatusOK || gateway.lastQuery != "SELECT `amount` FROM `agentsql_demo`.`orders` LIMIT 100" ||
		json.Unmarshal(w.Body.Bytes(), &envelope) != nil || len(envelope.Data) != 2 || envelope.Data[0] != "123" || envelope.Data[1] != "456" {
		t.Fatalf("MySQL enum did not use current qualified schema: %d %s sql=%s", w.Code, w.Body.String(), gateway.lastQuery)
	}
}

func TestFieldEnumCannotBypassActiveRowOrColumnPermission(t *testing.T) {
	service, principal, gateway, _, token := serviceFixture(t)
	gateway.columns = []Column{{Schema: "public", Table: "sales", Name: "region", DataType: "text"},
		{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"}}
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	tableID := browserTableID("10", "public", "sales")
	fieldID := strconv.FormatInt(browserFieldID(tableID, "amount"), 10)
	request := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/datasource/fieldEnum/10/"+fieldID, nil)
		r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}

	row := testActiveRowPermission(t, "east")
	if err := service.Store.put(context.Background(), permissionRuleKey(principal.Workspace, row.ID), row, 0); err != nil {
		t.Fatal(err)
	}
	before := gateway.queries
	response := request()
	if response.Code != http.StatusOK || gateway.queries != before+1 ||
		!strings.Contains(gateway.lastQuery, "region = 'east'") {
		t.Fatalf("field enum must use an audited row-filtered query: %d %s sql=%s", response.Code, response.Body.String(), gateway.lastQuery)
	}

	column := row
	column.Permissions = []json.RawMessage{json.RawMessage(`{"type":"column","ds_id":10,"table_id":` + strconv.FormatInt(tableID, 10) +
		`,"permissions":[{"field_id":` + strconv.FormatInt(browserFieldID(tableID, "region"), 10) +
		`,"enable":true},{"field_id":` + fieldID + `,"enable":false}]}`)}
	if err := service.Store.put(context.Background(), permissionRuleKey(principal.Workspace, row.ID), column, 1); err != nil {
		t.Fatal(err)
	}
	before = gateway.queries
	response = request()
	if response.Code == http.StatusOK || gateway.queries != before || strings.Contains(response.Body.String(), "123") {
		t.Fatalf("hidden column enum reached gateway: %d %s", response.Code, response.Body.String())
	}
}
