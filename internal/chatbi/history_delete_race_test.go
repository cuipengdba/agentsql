package chatbi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A history read reexecutes saved SQL. Deleting the conversation while that
// gateway request is in flight must fence the result before it reaches a
// history response or an export download.
func TestDeletedChatDuringHistoricalQueryReleasesNothing(t *testing.T) {
	for _, path := range []string{"chat", "record", "csv", "xlsx"} {
		t.Run(path, func(t *testing.T) {
			ctx := context.Background()
			service, principal, gateway, _, token := serviceFixture(t)
			chat, err := service.Start(ctx, principal, "10", "fixture", 0)
			if err != nil {
				t.Fatal(err)
			}
			record, err := service.Ask(ctx, principal, chat.ID, "amount", func(context.Context) error { return nil }, nil)
			if err != nil {
				t.Fatal(err)
			}
			gateway.afterQuery = func() {
				gateway.afterQuery = nil
				if err := service.Delete(ctx, principal, chat.ID); err != nil {
					t.Fatalf("delete during gateway query: %v", err)
				}
			}
			switch path {
			case "chat":
				got, err := service.ReadChat(ctx, principal, chat.ID, true)
				if err == nil || len(got.Records) != 0 {
					t.Fatalf("deleted chat returned historical records: %#v, %v", got, err)
				}
			case "record":
				got, err := service.Record(ctx, principal, record.ID, true)
				if err == nil || got.Data != nil || got.SQL != "" {
					t.Fatalf("deleted chat returned historical record: %#v, %v", got, err)
				}
			default:
				handler, err := NewHandler(service, "test")
				if err != nil {
					t.Fatal(err)
				}
				url := "/api/v1/agentsql/record/" + string(record.ID) + "/export.csv"
				if path == "xlsx" {
					url = "/api/v1/chat/record/" + string(record.ID) + "/excel/export/" + string(chat.ID)
				}
				request := httptest.NewRequest(http.MethodGet, url, nil)
				request.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if response.Code == http.StatusOK || strings.Contains(response.Body.String(), "123") ||
					strings.HasPrefix(response.Header().Get("Content-Type"), "text/csv") ||
					strings.HasPrefix(response.Header().Get("Content-Type"), "application/vnd.openxmlformats") {
					t.Fatalf("deleted chat released %s export: status=%d", path, response.Code)
				}
				receipts, err := service.Store.list(ctx, "export-receipt", string(principal.Workspace), string(principal.User.ID))
				if err != nil || len(receipts) != 0 {
					t.Fatalf("deleted chat created an export receipt: %v, %d", err, len(receipts))
				}
			}
			if gateway.queries != 2 {
				t.Fatalf("test did not reach a historical gateway query: %d", gateway.queries)
			}
			if _, err := service.ReadChat(ctx, principal, chat.ID, false); !errors.Is(err, ErrNotFound) {
				t.Fatalf("deleted chat became visible again: %v", err)
			}
		})
	}
}
