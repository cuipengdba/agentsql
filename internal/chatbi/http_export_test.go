package chatbi

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWorkbookExportUsesLiteralCellsAndRequiresAuditedFullResult(t *testing.T) {
	fields := make([]string, 17)
	values := map[string]any{}
	for index := range fields {
		fields[index] = "column" + string(rune('A'+index))
		values[fields[index]] = "value"
	}
	values[fields[0]] = "=HYPERLINK(A1)"
	data := QueryData{AuditID: 8, Fields: fields, Data: []map[string]any{values}}
	encoded, err := encodeWorkbookExport(data)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(encoded), int64(len(encoded)))
	if err != nil {
		t.Fatal(err)
	}
	var sheet []byte
	for _, file := range archive.File {
		if file.Name != "xl/worksheets/sheet1.xml" {
			continue
		}
		reader, openErr := file.Open()
		if openErr != nil {
			t.Fatal(openErr)
		}
		sheet, err = io.ReadAll(reader)
		_ = reader.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Contains(sheet, []byte(`t="inlineStr"`)) || !bytes.Contains(sheet, []byte("=HYPERLINK(A1)")) || bytes.Contains(sheet, []byte("<f>")) {
		t.Fatal("workbook did not preserve formula-like text as literal cells")
	}
	data.AuditID = 0
	if _, err := encodeWorkbookExport(data); err == nil {
		t.Fatal("unaudited workbook export accepted")
	}
	data.AuditID, data.Truncated = 8, true
	if _, err := encodeWorkbookExport(data); err == nil {
		t.Fatal("truncated workbook export accepted")
	}
}

func TestSQLBotWorkbookExportRequeriesAndFencesBeforeRelease(t *testing.T) {
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
	request := func(chatID, bearer string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/chat/record/"+string(record.ID)+"/excel/export/"+chatID, nil)
		if bearer != "" {
			r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	if got := request(string(chat.ID), ""); got.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous workbook export returned %d", got.Code)
	}
	beforeQueries := gateway.queries
	if got := request("999", token); got.Code == http.StatusOK || gateway.queries != beforeQueries {
		t.Fatal("mismatched chat ID reached the gateway")
	}
	response := request(string(chat.ID), token)
	if response.Code != http.StatusOK || gateway.queries != beforeQueries+1 ||
		response.Header().Get("Content-Type") != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" ||
		response.Header().Get("X-AgentSQL-Audit-ID") != "1" || !bytes.HasPrefix(response.Body.Bytes(), []byte("PK\x03\x04")) {
		t.Fatalf("original workbook export failed: %d", response.Code)
	}
	receipts, err := s.Store.list(ctx, "export-receipt", string(p.Workspace), string(p.User.ID))
	if err != nil || len(receipts) != 1 {
		t.Fatal("workbook export has no local audit receipt")
	}
	gateway.audit = 0
	if got := request(string(chat.ID), token); got.Code == http.StatusOK || got.Header().Get("Content-Type") == "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" {
		t.Fatal("unaudited workbook result was released")
	}
	gateway.audit = 1
	gateway.afterQuery = func() {
		var binding Binding
		revision, getErr := s.Store.get(ctx, bindingKey(p, "10"), &binding)
		if getErr != nil || s.Store.delete(ctx, bindingKey(p, "10"), revision) != nil {
			t.Fatal("binding revocation failed")
		}
	}
	if got := request(string(chat.ID), token); got.Code == http.StatusOK || got.Header().Get("Content-Type") == "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" {
		t.Fatal("revoked workbook result was released")
	}
	receipts, err = s.Store.list(ctx, "export-receipt", string(p.Workspace), string(p.User.ID))
	if err != nil || len(receipts) != 1 {
		t.Fatal("failed workbook export created an audit receipt")
	}
}

func TestSQLBotWorkbookExportSuppressesInFlightRowRuleChange(t *testing.T) {
	ctx := context.Background()
	s, principal, gateway, _, token := serviceFixture(t)
	chat, err := s.Start(ctx, principal, "10", "fixture", 0)
	if err != nil {
		t.Fatal(err)
	}
	gateway.columns = []Column{{Schema: "public", Table: "sales", Name: "region", DataType: "text"},
		{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"}}
	rule := testActiveRowPermission(t, "east")
	if err := s.Store.put(ctx, permissionRuleKey("1", rule.ID), rule, 0); err != nil {
		t.Fatal(err)
	}
	record, err := s.Ask(ctx, principal, chat.ID, "amount", func(context.Context) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewHandler(s, "test")
	if err != nil {
		t.Fatal(err)
	}
	beforeQueries := gateway.queries
	gateway.afterQuery = func() {
		changed := testActiveRowPermission(t, "west")
		if err := s.Store.put(ctx, permissionRuleKey("1", changed.ID), changed, 1); err != nil {
			t.Fatal(err)
		}
		gateway.afterQuery = nil
	}
	request := httptest.NewRequest(http.MethodGet,
		"/api/v1/chat/record/"+string(record.ID)+"/excel/export/"+string(chat.ID), nil)
	request.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code == http.StatusOK ||
		response.Header().Get("Content-Type") == "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" ||
		bytes.HasPrefix(response.Body.Bytes(), []byte("PK\x03\x04")) ||
		gateway.queries != beforeQueries+1 || !strings.Contains(gateway.lastQuery, "region = 'east'") {
		t.Fatalf("in-flight row-rule change released a workbook: status=%d queries=%d sql=%q",
			response.Code, gateway.queries-beforeQueries, gateway.lastQuery)
	}
	receipts, err := s.Store.list(ctx, "export-receipt", string(principal.Workspace), string(principal.User.ID))
	if err != nil || len(receipts) != 0 {
		t.Fatalf("denied workbook export created a receipt: %v, %d", err, len(receipts))
	}
}

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
	receipts, err := s.Store.list(ctx, "export-receipt", string(p.Workspace), string(p.User.ID))
	if err != nil || len(receipts) != 1 {
		t.Fatalf("export receipt missing: %v", err)
	}
	var receipt map[string]json.RawMessage
	if json.Unmarshal(receipts[0], &receipt) != nil || len(receipt) != 5 || string(receipt["record"]) != `"`+string(record.ID)+`"` || string(receipt["rows"]) != "1" || string(receipt["audit_id"]) != "1" {
		t.Fatalf("export receipt has unexpected fields or values: %s", receipts[0])
	}
	for _, field := range []string{"record", "datasource", "audit_id", "rows", "time"} {
		if _, okay := receipt[field]; !okay {
			t.Fatalf("export receipt missing %q: %s", field, receipts[0])
		}
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
	receipts, err = s.Store.list(ctx, "export-receipt", string(p.Workspace), string(p.User.ID))
	if err != nil || len(receipts) != 1 {
		t.Fatal("revoked export created a receipt")
	}
}
