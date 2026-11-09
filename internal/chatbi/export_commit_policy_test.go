package chatbi

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Export receipts are durable writes after the controlled query. A control
// change committed with that receipt must still fence the buffered file.
func TestExportRechecksPolicyAfterReceiptCommit(t *testing.T) {
	for _, format := range []string{"csv", "xlsx"} {
		for _, change := range []string{"row", "column", "removed-rule", "identity"} {
			t.Run(format+"/"+change, func(t *testing.T) {
				ctx := context.Background()
				s, p, gateway, _, token := serviceFixture(t)
				gateway.columns = []Column{{Schema: "public", Table: "sales", Name: "region", DataType: "text"},
					{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"}}
				rule := testActiveRowPermission(t, "east")
				key := permissionRuleKey(p.Workspace, rule.ID)
				if err := s.Store.put(ctx, key, rule, 0); err != nil {
					t.Fatal(err)
				}
				record := finishedFixtureRecord(t, s, p, token)
				var changed any
				switch change {
				case "row":
					changed = testActiveRowPermission(t, "west")
				case "column":
					tableID := browserTableID("10", "public", "sales")
					fields, err := json.Marshal([]map[string]any{
						{"field_id": browserFieldID(tableID, "region"), "enable": true},
						{"field_id": browserFieldID(tableID, "amount"), "enable": false},
					})
					if err != nil {
						t.Fatal(err)
					}
					item, err := json.Marshal(map[string]any{"type": "column", "ds_id": 10,
						"table_id": tableID, "permissions": string(fields)})
					if err != nil {
						t.Fatal(err)
					}
					rule.Permissions = []json.RawMessage{item}
					changed = rule
				case "removed-rule":
					rule.Enable = false
					changed = rule
				case "identity":
					user, _, err := s.Store.User(ctx, p.User.ID)
					if err != nil {
						t.Fatal(err)
					}
					user.Name = "changed-after-query"
					key, changed = userKey(p.User.ID), user
				}
				body, err := s.Store.seal(key, changed)
				if err != nil {
					t.Fatal(err)
				}
				trigger := fmt.Sprintf(`CREATE TRIGGER change_on_export AFTER INSERT ON chatbi_objects
					WHEN NEW.kind='export-receipt' BEGIN
					UPDATE chatbi_objects SET revision=revision+1,body=X'%s'
					WHERE kind='%s' AND scope='%s' AND owner='%s' AND id='%s'; END`,
					hex.EncodeToString(body), key.Kind, key.Scope, key.Owner, key.ID)
				if _, err := s.Store.db.ExecContext(ctx, trigger); err != nil {
					t.Fatal(err)
				}
				handler, err := NewHandler(s, "test")
				if err != nil {
					t.Fatal(err)
				}
				path := "/api/v1/agentsql/record/" + string(record.ID) + "/export.csv"
				if format == "xlsx" {
					path = "/api/v1/chat/record/" + string(record.ID) + "/excel/export/" + string(record.Chat)
				}
				r := httptest.NewRequest(http.MethodGet, path, nil)
				r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				if w.Code == http.StatusOK || strings.Contains(w.Body.String(), "123") ||
					strings.HasPrefix(w.Header().Get("Content-Type"), "text/csv") ||
					strings.HasPrefix(w.Header().Get("Content-Type"), "application/vnd.openxmlformats") {
					t.Fatalf("changed control released %s: status=%d", format, w.Code)
				}
				receipts, err := s.Store.list(ctx, "export-receipt", string(p.Workspace), string(p.User.ID))
				if err != nil || len(receipts) != 1 {
					t.Fatalf("receipt commit was not reached: %d %v", len(receipts), err)
				}
			})
		}
	}
}

func TestExportReceiptFailureSendsNoFileAndRecovers(t *testing.T) {
	for _, format := range []string{"csv", "xlsx"} {
		t.Run(format, func(t *testing.T) {
			ctx := context.Background()
			s, p, _, _, token := serviceFixture(t)
			record := finishedFixtureRecord(t, s, p, token)
			h, err := NewHandler(s, "test")
			if err != nil {
				t.Fatal(err)
			}
			path := "/api/v1/agentsql/record/" + string(record.ID) + "/export.csv"
			if format == "xlsx" {
				path = "/api/v1/chat/record/" + string(record.ID) + "/excel/export/" + string(record.Chat)
			}
			request := func() *httptest.ResponseRecorder {
				r := httptest.NewRequest(http.MethodGet, path, nil)
				r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				return w
			}
			if _, err := s.Store.db.ExecContext(ctx, `CREATE TRIGGER reject_export_receipt BEFORE INSERT ON chatbi_objects
				WHEN NEW.kind='export-receipt' BEGIN SELECT RAISE(ABORT,'synthetic receipt fault'); END`); err != nil {
				t.Fatal(err)
			}
			w := request()
			if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "123") || w.Header().Get("X-AgentSQL-Audit-ID") != "" {
				t.Fatalf("failed receipt released file: %d", w.Code)
			}
			receipts, err := s.Store.list(ctx, "export-receipt", string(p.Workspace), string(p.User.ID))
			if err != nil || len(receipts) != 0 {
				t.Fatalf("failed receipt persisted: %d %v", len(receipts), err)
			}
			if _, err := s.Store.db.ExecContext(ctx, `DROP TRIGGER reject_export_receipt`); err != nil {
				t.Fatal(err)
			}
			if got := request(); got.Code != http.StatusOK {
				t.Fatalf("export did not recover: %d", got.Code)
			}
		})
	}
}
