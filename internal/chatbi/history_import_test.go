package chatbi

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func historyFixture() HistorySnapshot {
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	return HistorySnapshot{FormatVersion: 1, SourceVersion: "v1.10.2", SourceCommit: sqlbotHistoryCommit,
		Chats: []HistoricalChat{{ID: "101", Workspace: "1", Creator: "1", Created: created, Brief: "past sales", Datasource: "10", Origin: 0,
			Records: []HistoricalRecord{{ID: "201", Question: "How many sales?", Created: created.Add(time.Minute)}}}}}
}

func TestMetadataOnlyHistoryImportIsAtomicAndNeverReplaysOldResults(t *testing.T) {
	ctx := context.Background()
	service, p, gateway, model, _ := serviceFixture(t)
	report, err := service.Store.ImportHistory(ctx, historyFixture())
	if err != nil || report.ImportedChats != 1 || report.ImportedQuestions != 1 || report.CompleteMigration || report.HistoricalRowsUsed {
		t.Fatalf("history report mismatch: %#v %v", report, err)
	}
	chat, err := service.ReadChat(ctx, p, "101", true)
	if err != nil || len(chat.Records) != 1 || chat.Records[0].Question != "How many sales?" || chat.Records[0].Data != nil || chat.Records[0].SQL != "" || gateway.queries != 0 {
		t.Fatalf("imported data was replayed: %#v %v", chat, err)
	}
	record, err := service.Record(ctx, p, "201", true)
	if err != nil || record.Data != nil || gateway.queries != 0 {
		t.Fatal("imported record executed historical SQL")
	}
	if _, err := service.Store.ImportHistory(ctx, historyFixture()); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate import did not fail closed")
	}
	chats, err := service.Chats(ctx, p)
	if err != nil || len(chats) != 1 || model.calls != 0 {
		t.Fatal("failed duplicate changed existing history")
	}
	audits, err := service.Store.list(ctx, "management-audit", "system", "system")
	if err != nil || len(audits) != 1 {
		t.Fatal("history import audit missing or duplicated")
	}
}

func TestHistoryImportRejectsUnknownFieldsMissingBindingAndAuditFault(t *testing.T) {
	ctx := context.Background()
	service, p, _, _, _ := serviceFixture(t)
	snapshot := historyFixture()
	path := filepath.Join(t.TempDir(), "history.private.json")
	raw, _ := json.Marshal(snapshot)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadHistory(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.TrimSuffix(string(raw), "}")+`,"data":"SECRET_ROWS"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadHistory(path); err == nil {
		t.Fatal("raw row field was accepted")
	}
	if err := service.Store.delete(ctx, bindingKey(p, "10"), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Store.ImportHistory(ctx, snapshot); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing binding did not block import")
	}
	chats, err := service.Chats(ctx, p)
	if err != nil || len(chats) != 0 {
		t.Fatal("failed import wrote history")
	}
	if err := service.Store.put(ctx, bindingKey(p, "10"), Binding{Datasource: "10", Workspace: "1", User: "1", Name: "fixture", Dialect: "pg", BindingID: "binding", ConnectorToken: "fixture-connector", AgentKey: "fixture-agent"}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Store.db.ExecContext(ctx, `CREATE TRIGGER reject_history_audit BEFORE INSERT ON chatbi_objects WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(ABORT,'synthetic audit fault'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Store.ImportHistory(ctx, snapshot); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("audit fault did not block import: %v", err)
	}
	chats, err = service.Chats(ctx, p)
	if err != nil || len(chats) != 0 {
		t.Fatal("audit failure partially imported history")
	}
}

func TestPinnedSQLBotHistoryExport(t *testing.T) {
	path := os.Getenv("AGENTSQL_SQLBOT_HISTORY_EXPORT")
	if path == "" {
		t.Skip("pinned SQLBot history fixture is not configured")
	}
	snapshot, err := ReadHistory(path)
	if err != nil {
		t.Fatalf("real SQLBot metadata export was rejected: %v", err)
	}
	questions := 0
	for _, chat := range snapshot.Chats {
		questions += len(chat.Records)
	}
	if questions == 0 {
		t.Fatal("reference export contained no questions")
	}
	t.Logf("pinned source metadata: chats=%d questions=%d", len(snapshot.Chats), questions)
}

func TestPinnedSQLBotHistoryImport(t *testing.T) {
	path := os.Getenv("AGENTSQL_SQLBOT_HISTORY_EXPORT")
	if path == "" {
		t.Skip("pinned SQLBot history fixture is not configured")
	}
	snapshot, err := ReadHistory(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	store := testStore(t)
	users := map[string]bool{}
	bindings := map[string]bool{}
	for _, chat := range snapshot.Chats {
		userKeyText := string(chat.Workspace) + "/" + string(chat.Creator)
		if !users[userKeyText] {
			user := testUser(t, string(chat.Creator), "synthetic-"+string(chat.Creator))
			user.Workspace = chat.Workspace
			user.Memberships = []Membership{{Workspace: chat.Workspace, Name: "Synthetic"}}
			if err := store.CreateUser(ctx, user); err != nil {
				t.Fatal(err)
			}
			users[userKeyText] = true
		}
		bindingKeyText := userKeyText + "/" + string(chat.Datasource)
		if !bindings[bindingKeyText] {
			p := Principal{User: User{ID: chat.Creator}, Workspace: chat.Workspace}
			binding := Binding{Datasource: chat.Datasource, Workspace: chat.Workspace, User: chat.Creator, Name: "synthetic", Dialect: "pg", BindingID: "synthetic", ConnectorToken: "synthetic", AgentKey: "synthetic"}
			if err := store.put(ctx, bindingKey(p, chat.Datasource), binding, 0); err != nil {
				t.Fatal(err)
			}
			bindings[bindingKeyText] = true
		}
	}
	report, err := store.ImportHistory(ctx, snapshot)
	if err != nil || report.ImportedChats != len(snapshot.Chats) || report.CompleteMigration || report.HistoricalRowsUsed {
		t.Fatalf("pinned source import failed: %#v %v", report, err)
	}
	t.Logf("pinned source metadata imported: chats=%d questions=%d", report.ImportedChats, report.ImportedQuestions)
}
