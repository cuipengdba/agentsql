package chatbi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestSystemVariablesAreScopedAuditedAndDoNotLeakToMembers(t *testing.T) {
	service, handler, adminToken, bobToken := adminUsersFixture(t)
	create := accountRequest(t, handler, http.MethodPost, "/api/v1/sys_variable/save",
		`{"name":"region","var_type":"text","value":["east","west"]}`, adminToken)
	if create.Code != http.StatusOK {
		t.Fatalf("create: %d %s", create.Code, create.Body.String())
	}
	items, err := service.Store.SystemVariables(context.Background(), "1", "region")
	if err != nil || len(items) != 1 || items[0].CreatedBy != "1" {
		t.Fatalf("stored variable: %+v %v", items, err)
	}
	if page := accountRequest(t, handler, http.MethodPost, "/api/v1/sys_variable/listPage/1/10", `{}`, bobToken); page.Code != http.StatusUnauthorized {
		t.Fatalf("ordinary user saw variable page: %d", page.Code)
	}
	if all := accountRequest(t, handler, http.MethodPost, "/api/v1/sys_variable/listAll", `{}`, bobToken); all.Code != http.StatusUnauthorized {
		t.Fatalf("ordinary user saw variable list: %d", all.Code)
	}
	bob, revision, err := service.Store.User(context.Background(), "2")
	if err != nil {
		t.Fatal(err)
	}
	bob.Memberships[0].Weight = 1
	if err := service.Store.put(context.Background(), userKey(bob.ID), bob, revision); err != nil {
		t.Fatal(err)
	}
	if all := accountRequest(t, handler, http.MethodPost, "/api/v1/sys_variable/listAll", `{}`, bobToken); all.Code != http.StatusOK || !strings.Contains(all.Body.String(), "region") {
		t.Fatalf("workspace admin could not list variables: %d %s", all.Code, all.Body.String())
	}
	if denied := accountRequest(t, handler, http.MethodPost, "/api/v1/sys_variable/save", `{"name":"unauthorized","var_type":"text","value":["x"]}`, bobToken); denied.Code != http.StatusUnauthorized {
		t.Fatalf("workspace admin wrote global-admin variable: %d", denied.Code)
	}
	page := accountRequest(t, handler, http.MethodPost, "/api/v1/sys_variable/listPage/1/10", `{"name":"reg"}`, adminToken)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `"total":1`) || !strings.Contains(page.Body.String(), `"region"`) {
		t.Fatalf("admin page: %d %s", page.Code, page.Body.String())
	}
	input, _ := json.Marshal(map[string]any{"id": items[0].ID, "name": "division", "var_type": "number", "value": []int{1, 10}})
	update := accountRequest(t, handler, http.MethodPost, "/api/v1/sys_variable/save", string(input), adminToken)
	if update.Code != http.StatusOK {
		t.Fatalf("update: %d %s", update.Code, update.Body.String())
	}
	previous, _ := service.Store.SystemVariables(context.Background(), "1", "region")
	if len(previous) != 0 {
		t.Fatal("old name survived update")
	}
	current, _ := service.Store.SystemVariables(context.Background(), "1", "division")
	if len(current) != 1 || current[0].CreatedBy != "1" || current[0].Created.IsZero() {
		t.Fatalf("update lost provenance: %+v", current)
	}
	if invalid := accountRequest(t, handler, http.MethodPost, "/api/v1/sys_variable/save", `{"name":"bad","var_type":"number","value":["1"]}`, adminToken); invalid.Code != http.StatusUnprocessableEntity {
		t.Fatalf("string accepted as number: %d", invalid.Code)
	}
	if duplicate := accountRequest(t, handler, http.MethodPost, "/api/v1/sys_variable/save", `{"name":"DIVISION","var_type":"text","value":["x"]}`, adminToken); duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate name accepted: %d", duplicate.Code)
	}
	// The same name in another workspace stays separate; a token in workspace 1 cannot read it.
	other := SystemVariable{ID: 9, Name: "other", VarType: "text", Type: "custom", Value: json.RawMessage(`["private"]`)}
	if err := service.Store.put(context.Background(), systemVariableKey("2", other.ID), other, 0); err != nil {
		t.Fatal(err)
	}
	all := accountRequest(t, handler, http.MethodPost, "/api/v1/sys_variable/listAll", `{}`, adminToken)
	if all.Code != http.StatusOK || strings.Contains(all.Body.String(), "private") {
		t.Fatalf("cross-workspace value leaked: %d %s", all.Code, all.Body.String())
	}
	deleteIDs, _ := json.Marshal([]int64{items[0].ID})
	deleted := accountRequest(t, handler, http.MethodPost, "/api/v1/sys_variable/delete", string(deleteIDs), adminToken)
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", deleted.Code, deleted.Body.String())
	}
	remaining, _ := service.Store.SystemVariables(context.Background(), "1", "")
	if len(remaining) != 0 {
		t.Fatal("deleted variable remains")
	}
	audits, err := service.Store.list(context.Background(), "management-audit", "system", "system")
	if err != nil || len(audits) != 3 {
		t.Fatalf("variable operations lacked audit: %d %v", len(audits), err)
	}
}

func TestSystemVariableAuditFailureAndBatchDeletionRollback(t *testing.T) {
	service, handler, adminToken, _ := adminUsersFixture(t)
	create := accountRequest(t, handler, http.MethodPost, "/api/v1/sys_variable/save", `{"name":"region","var_type":"text","value":["east"]}`, adminToken)
	if create.Code != http.StatusOK {
		t.Fatal(create.Body.String())
	}
	items, _ := service.Store.SystemVariables(context.Background(), "1", "")
	ids, _ := json.Marshal([]int64{items[0].ID, 999})
	if deleted := accountRequest(t, handler, http.MethodPost, "/api/v1/sys_variable/delete", string(ids), adminToken); deleted.Code != http.StatusNotFound {
		t.Fatalf("missing member of batch: %d", deleted.Code)
	}
	if items, _ := service.Store.SystemVariables(context.Background(), "1", ""); len(items) != 1 {
		t.Fatal("partial batch deletion")
	}
	if _, err := service.Store.db.ExecContext(context.Background(), `CREATE TRIGGER reject_variable_audit BEFORE INSERT ON chatbi_objects WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(ABORT,'synthetic audit fault'); END`); err != nil {
		t.Fatal(err)
	}
	if changed := accountRequest(t, handler, http.MethodPost, "/api/v1/sys_variable/save", `{"name":"country","var_type":"text","value":["CN"]}`, adminToken); changed.Code != http.StatusServiceUnavailable {
		t.Fatalf("audit failure accepted: %d %s", changed.Code, changed.Body.String())
	}
	if items, _ := service.Store.SystemVariables(context.Background(), "1", ""); len(items) != 1 {
		t.Fatal("audit failure left variable")
	}
}
