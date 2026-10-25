package chatbi

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSemanticWorkbookContainsLiteralStringsAndValidOOXML(t *testing.T) {
	data, err := semanticWorkbook([][]string{{"term", "description"}, {"=HYPERLINK(\"https://example.invalid\")", "GMV < total & tax"}})
	if err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.File) != 5 {
		t.Fatal("incomplete XLSX package")
	}
	for _, part := range archive.File {
		reader, err := part.Open()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(reader)
		_ = reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		decoder := xml.NewDecoder(bytes.NewReader(raw))
		for {
			if _, err := decoder.Token(); err == io.EOF {
				break
			} else if err != nil {
				t.Fatalf("invalid OOXML part %s: %v", part.Name, err)
			}
		}
		if part.Name == "xl/worksheets/sheet1.xml" {
			if !bytes.Contains(raw, []byte(`t="inlineStr"`)) || bytes.Contains(raw, []byte("<f>")) || !bytes.Contains(raw, []byte("GMV &lt; total &amp; tax")) {
				t.Fatal("export escaped text as executable formula or malformed XML")
			}
		}
	}
	if _, err := semanticWorkbook([][]string{{"term"}, {strings.Repeat("x", 32768)}}); err == nil {
		t.Fatal("overlong Excel cell accepted")
	}
	if _, err := semanticWorkbook([][]string{{"term"}, {"bad\x01cell"}}); err == nil {
		t.Fatal("XML control character accepted")
	}
}

func TestSemanticExportRequiresAdminAndDurableAudit(t *testing.T) {
	service, p, _, _, token := serviceFixture(t)
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	request := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := request("/api/v1/system/terminology/template"); w.Code == http.StatusOK {
		t.Fatal("ordinary user downloaded admin template")
	}
	user, rev, err := service.Store.User(context.Background(), p.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	user.Admin = true
	if err := service.Store.put(context.Background(), userKey(user.ID), user, rev); err != nil {
		t.Fatal(err)
	}
	p, err = service.Identity.Verify(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SaveTerminology(context.Background(), p, Terminology{Word: "GMV", Description: "synthetic total"}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/system/terminology/template", "/api/v1/system/data-training/template", "/api/v1/system/terminology/export", "/api/v1/system/data-training/export"} {
		w := request(path)
		if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" || !bytes.HasPrefix(w.Body.Bytes(), []byte("PK")) {
			t.Fatalf("semantic download unavailable: %s %d", path, w.Code)
		}
	}
	audits, err := service.Store.list(context.Background(), "management-audit", "system", "system")
	if err != nil || len(audits) != 3 {
		t.Fatalf("expected save and two export audits, got %d: %v", len(audits), err)
	}
	if _, err := service.Store.db.ExecContext(context.Background(), `CREATE TRIGGER reject_semantic_export_audit BEFORE INSERT ON chatbi_objects WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(ABORT,'synthetic audit fault'); END`); err != nil {
		t.Fatal(err)
	}
	if w := request("/api/v1/system/terminology/export"); w.Code == http.StatusOK || bytes.HasPrefix(w.Body.Bytes(), []byte("PK")) {
		t.Fatal("export released bytes without durable audit")
	}
}
