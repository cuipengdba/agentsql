package chatbi

import (
	"context"
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCSVExportNeutralizesSpreadsheetFormulas(t *testing.T) {
	data := QueryData{AuditID: 8, Fields: []string{"name", "=header"}, Data: []map[string]any{
		{"name": "=1+1", "=header": "  @SUM(A1)"},
		{"name": "west, region", "=header": "\u00a0=HYPERLINK(A1)"},
	}}
	encoded, err := encodeCSVExport(data)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(strings.NewReader(string(encoded))).ReadAll()
	if err != nil || len(rows) != 3 || rows[0][1] != "'=header" || rows[1][0] != "'=1+1" || rows[1][1] != "'  @SUM(A1)" || rows[2][0] != "west, region" || rows[2][1] != "'\u00a0=HYPERLINK(A1)" {
		t.Fatalf("unsafe or malformed CSV: %#v %v", rows, err)
	}
	if _, err := encodeCSVExport(QueryData{AuditID: 0, Fields: []string{"name"}, Data: data.Data}); err == nil {
		t.Fatal("unaudited result accepted")
	}
	data.Truncated = true
	if _, err := encodeCSVExport(data); err == nil {
		t.Fatal("truncated result presented as a complete export")
	}
}

func TestCSVExportRequeriesAndAuditsBeforeRelease(t *testing.T) {
	ctx := context.Background()
	s, p, gateway, _, token := serviceFixture(t)
	chat, err := s.Start(ctx, p, "10", "fixture", 0)
	if err != nil {
		t.Fatal(err)
	}
	record, err := s.Ask(ctx, p, chat.ID, "amount", func(context.Context) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewHandler(s, "test")
	if err != nil {
		t.Fatal(err)
	}
	request := func(bearer string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/agentsql/record/"+string(record.ID)+"/export.csv", nil)
		if bearer != "" {
			r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	if got := request(""); got.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous export returned %d", got.Code)
	}
	before, err := s.Store.list(ctx, "management-audit", "system", "system")
	if err != nil {
		t.Fatal(err)
	}
	previousQueries := gateway.queries
	response := request(token)
	if response.Code != http.StatusOK || gateway.queries != previousQueries+1 || response.Header().Get("X-AgentSQL-Audit-ID") != "1" || !strings.Contains(response.Header().Get("Content-Disposition"), string(record.ID)) || !strings.Contains(response.Body.String(), "123") {
		t.Fatalf("controlled export failed: %d", response.Code)
	}
	after, err := s.Store.list(ctx, "management-audit", "system", "system")
	if err != nil || len(after) != len(before)+1 {
		t.Fatalf("export audit missing: %v", err)
	}
	if receipts, err := s.Store.list(ctx, "export-receipt", string(p.Workspace), string(p.User.ID)); err != nil || len(receipts) != 1 || strings.Contains(string(receipts[0]), "123") {
		t.Fatalf("export receipt leaked rows or was missing: %v", err)
	}
	gateway.audit = 0
	unaudited := request(token)
	if unaudited.Code == http.StatusOK || strings.Contains(unaudited.Body.String(), "123") {
		t.Fatalf("unaudited historical query was exported: %d", unaudited.Code)
	}
	gateway.audit = 1
	previousQueries = gateway.queries
	gateway.afterQuery = func() {
		var binding Binding
		revision, err := s.Store.get(ctx, bindingKey(p, "10"), &binding)
		if err != nil || s.Store.delete(ctx, bindingKey(p, "10"), revision) != nil {
			t.Fatal("binding revocation failed")
		}
	}
	denied := request(token)
	if denied.Code == http.StatusOK || strings.Contains(denied.Body.String(), "123") || gateway.queries != previousQueries+1 {
		t.Fatalf("revoked export released rows: %d", denied.Code)
	}
	receipts, err := s.Store.list(ctx, "export-receipt", string(p.Workspace), string(p.User.ID))
	if err != nil || len(receipts) != 1 {
		t.Fatal("revoked export created a receipt")
	}
}
