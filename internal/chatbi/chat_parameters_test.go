package chatbi

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func chatParameterForm(t *testing.T, handler http.Handler, token string, values string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	if err := form.WriteField("data", values); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/system/parameter", &body)
	r.Header.Set("Content-Type", form.FormDataContentType())
	if token != "" {
		r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestChatParametersPersistAndBoundModelHistory(t *testing.T) {
	service, handler, adminToken, memberToken := adminUsersFixture(t)
	ctx := context.Background()
	memberRead := accountRequest(t, handler, http.MethodGet, "/api/v1/system/parameter/chat", "", memberToken)
	if memberRead.Code != http.StatusOK {
		t.Fatalf("member chat settings: %d", memberRead.Code)
	}
	if denied := accountRequest(t, handler, http.MethodGet, "/api/v1/system/parameter", "", memberToken); denied.Code != http.StatusUnauthorized {
		t.Fatalf("member read system settings: %d", denied.Code)
	}
	if denied := chatParameterForm(t, handler, memberToken, `[{"pkey":"chat.show_sql","pval":false}]`); denied.Code != http.StatusUnauthorized {
		t.Fatalf("member changed system settings: %d", denied.Code)
	}
	change := `[{"pkey":"chat.sqlbot_name","pval":"AgentSQL Ops"},{"pkey":"chat.hide_thinking_block","pval":true},{"pkey":"chat.context_record_count","pval":0},{"pkey":"chat.limit_rows","pval":true}]`
	if saved := chatParameterForm(t, handler, adminToken, change); saved.Code != http.StatusOK {
		t.Fatalf("save chat settings: %d %s", saved.Code, saved.Body.String())
	}
	settings, err := service.Store.ChatSettings(ctx)
	if err != nil || settings.Name != "AgentSQL Ops" || !settings.HideThinking || settings.ContextRecords != 0 || !settings.LimitRows {
		t.Fatalf("stored chat settings: %+v %v", settings, err)
	}
	if denied := accountRequest(t, handler, http.MethodGet, "/api/v1/agentsql/chat-parameters", "", memberToken); denied.Code != http.StatusUnauthorized {
		t.Fatalf("member read own settings page: %d", denied.Code)
	}
	native := accountRequest(t, handler, http.MethodGet, "/api/v1/agentsql/chat-parameters", "", adminToken)
	if native.Code != http.StatusOK || !strings.Contains(native.Body.String(), `"context_records":0`) {
		t.Fatalf("own settings page read: %d %s", native.Code, native.Body.String())
	}
	nativeSave := accountRequest(t, handler, http.MethodPut, "/api/v1/agentsql/chat-parameters",
		`{"name":"AgentSQL Ops","hide_thinking":true,"expand_thinking":false,"limit_rows":true,"context_records":0,"show_sql":false,"show_log":true}`, adminToken)
	if nativeSave.Code != http.StatusOK {
		t.Fatalf("own settings page write: %d %s", nativeSave.Code, nativeSave.Body.String())
	}
	settings, err = service.Store.ChatSettings(ctx)
	if err != nil || settings.ShowSQL {
		t.Fatalf("own settings change not persisted: %+v %v", settings, err)
	}
	if denied := accountRequest(t, handler, http.MethodPut, "/api/v1/agentsql/chat-parameters",
		`{"name":"AgentSQL Ops","context_records":0}`, adminToken); denied.Code != http.StatusUnprocessableEntity {
		t.Fatalf("own page disabled row cap: %d", denied.Code)
	}
	all := accountRequest(t, handler, http.MethodGet, "/api/v1/system/parameter", "", adminToken)
	if all.Code != http.StatusOK || !strings.Contains(all.Body.String(), `"pval":"AgentSQL Ops"`) || !strings.Contains(all.Body.String(), `"pval":"0"`) {
		t.Fatalf("original settings page read: %d %s", all.Code, all.Body.String())
	}
	if unsafe := chatParameterForm(t, handler, adminToken, `[{"pkey":"chat.limit_rows","pval":false}]`); unsafe.Code != http.StatusUnprocessableEntity {
		t.Fatalf("row cap was disabled: %d %s", unsafe.Code, unsafe.Body.String())
	}
	if duplicate := chatParameterForm(t, handler, adminToken, `[{"pkey":"chat.show_sql","pval":true},{"pkey":"chat.show_sql","pval":false}]`); duplicate.Code != http.StatusUnprocessableEntity {
		t.Fatalf("duplicate parameter key accepted: %d", duplicate.Code)
	}
	if unsafeName := chatParameterForm(t, handler, adminToken, `[{"pkey":"chat.sqlbot_name","pval":"<img>"}]`); unsafeName.Code != http.StatusUnprocessableEntity {
		t.Fatalf("markup in assistant name accepted: %d", unsafeName.Code)
	}
	if _, err := service.Store.db.ExecContext(ctx, `CREATE TRIGGER reject_chat_parameter_audit BEFORE INSERT ON chatbi_objects WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(ABORT,'synthetic audit fault'); END`); err != nil {
		t.Fatal(err)
	}
	if failed := chatParameterForm(t, handler, adminToken, `[{"pkey":"chat.context_record_count","pval":2}]`); failed.Code != http.StatusServiceUnavailable {
		t.Fatalf("audit failure did not close settings write: %d %s", failed.Code, failed.Body.String())
	}
	settings, err = service.Store.ChatSettings(ctx)
	if err != nil || settings.ContextRecords != 0 {
		t.Fatalf("audit failure changed settings: %+v %v", settings, err)
	}
	if _, err := service.Store.db.ExecContext(ctx, `DROP TRIGGER reject_chat_parameter_audit`); err != nil {
		t.Fatal(err)
	}

	principal, err := service.Identity.Verify(ctx, adminToken)
	if err != nil {
		t.Fatal(err)
	}
	model := &historyModel{}
	service.Model = model
	chat, err := service.Start(ctx, principal, "10", "first", 0)
	if err != nil {
		t.Fatal(err)
	}
	check := func(ctx context.Context) error { _, err := service.Identity.Verify(ctx, adminToken); return err }
	if _, err := service.Ask(ctx, principal, chat.ID, "first", check, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Ask(ctx, principal, chat.ID, "second", check, nil); err != nil {
		t.Fatal(err)
	}
	if len(model.histories) != 2 || len(model.histories[1]) != 0 {
		t.Fatalf("disabled model context still received previous turn: %#v", model.histories)
	}
	if saved := chatParameterForm(t, handler, adminToken, `[{"pkey":"chat.context_record_count","pval":1}]`); saved.Code != http.StatusOK {
		t.Fatal(saved.Body.String())
	}
	if _, err := service.Ask(ctx, principal, chat.ID, "third", check, nil); err != nil {
		t.Fatal(err)
	}
	if len(model.histories) != 3 || len(model.histories[2]) != 1 || model.histories[2][0].Question != "second" {
		t.Fatalf("configured context not applied: %#v", model.histories)
	}
}
