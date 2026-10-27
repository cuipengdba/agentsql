package chatbi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestManagedWorkspaceCreateBecomesMemberTarget(t *testing.T) {
	service, handler, adminToken, bobToken := adminUsersFixture(t)
	if denied := accountRequest(t, handler, http.MethodPost, "/api/v1/system/workspace", `{"name":"Research"}`, bobToken); denied.Code != http.StatusUnauthorized {
		t.Fatalf("ordinary user created workspace: %d %s", denied.Code, denied.Body.String())
	}
	created := accountRequest(t, handler, http.MethodPost, "/api/v1/system/workspace", `{"name":"Research"}`, adminToken)
	if created.Code != http.StatusOK {
		t.Fatalf("workspace create: %d %s", created.Code, created.Body.String())
	}
	var envelope struct {
		Data struct {
			ID ID `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &envelope); err != nil || !envelope.Data.ID.Valid() {
		t.Fatalf("workspace ID: %v %s", err, created.Body.String())
	}
	admin, _, err := service.Store.User(context.Background(), "1")
	if err != nil || !admin.canUse(envelope.Data.ID) {
		t.Fatalf("creator cannot enter new workspace: %+v %v", admin, err)
	}
	if _, err := service.Identity.Verify(context.Background(), adminToken); err != nil {
		t.Fatalf("workspace create unexpectedly revoked admin token: %v", err)
	}
	listed := accountRequest(t, handler, http.MethodGet, "/api/v1/system/workspace", "", adminToken)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"name":"Research"`) {
		t.Fatalf("empty workspace missing from catalog: %d %s", listed.Code, listed.Body.String())
	}
	grant := accountRequest(t, handler, http.MethodPost, "/api/v1/system/workspace/uws", `{"uid_list":[2],"oid":`+string(envelope.Data.ID)+`}`, adminToken)
	if grant.Code != http.StatusOK {
		t.Fatalf("created workspace cannot accept members: %d %s", grant.Code, grant.Body.String())
	}
	bob, _, err := service.Store.User(context.Background(), "2")
	if err != nil || !bob.canUse(envelope.Data.ID) {
		t.Fatalf("membership not persisted: %+v %v", bob, err)
	}
	if blocked := accountRequest(t, handler, http.MethodDelete, "/api/v1/system/workspace/"+string(envelope.Data.ID), "", adminToken); blocked.Code != http.StatusConflict {
		t.Fatalf("workspace with a member was deleted: %d %s", blocked.Code, blocked.Body.String())
	}
	audits, err := service.Store.list(context.Background(), "management-audit", "system", "system")
	foundCreate := false
	for _, audit := range audits {
		foundCreate = foundCreate || strings.Contains(string(audit), "workspace.create")
	}
	if err != nil || len(audits) != 2 || !foundCreate {
		t.Fatalf("workspace creation audit: %v %#v", err, audits)
	}
}

func TestManagedWorkspaceRenameUpdatesImportedMembersAtomically(t *testing.T) {
	service, handler, adminToken, bobToken := adminUsersFixture(t)
	updated := accountRequest(t, handler, http.MethodPut, "/api/v1/system/workspace", `{"id":1,"name":"Core"}`, adminToken)
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"name":"Core"`) {
		t.Fatalf("workspace rename: %d %s", updated.Code, updated.Body.String())
	}
	for _, id := range []ID{"1", "2"} {
		user, _, err := service.Store.User(context.Background(), id)
		if err != nil || user.Memberships[0].Name != "Core" {
			t.Fatalf("member name not synchronized: %+v %v", user, err)
		}
	}
	if _, err := service.Identity.Verify(context.Background(), bobToken); err != nil {
		t.Fatalf("rename incorrectly revoked session: %v", err)
	}
	listed := accountRequest(t, handler, http.MethodGet, "/api/v1/system/workspace", "", adminToken)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"name":"Core"`) || strings.Contains(listed.Body.String(), `"name":"One"`) {
		t.Fatalf("renamed workspace missing: %d %s", listed.Code, listed.Body.String())
	}
}

func TestManagedWorkspaceDeleteEmptyCatalog(t *testing.T) {
	service, handler, adminToken, _ := adminUsersFixture(t)
	created := accountRequest(t, handler, http.MethodPost, "/api/v1/system/workspace", `{"name":"Temporary"}`, adminToken)
	var envelope struct {
		Data struct {
			ID ID `json:"id"`
		} `json:"data"`
	}
	if created.Code != http.StatusOK || json.Unmarshal(created.Body.Bytes(), &envelope) != nil || !envelope.Data.ID.Valid() {
		t.Fatalf("workspace create: %d %s", created.Code, created.Body.String())
	}
	if protected := accountRequest(t, handler, http.MethodDelete, "/api/v1/system/workspace/1", "", adminToken); protected.Code != http.StatusUnprocessableEntity {
		t.Fatalf("default workspace deleted: %d", protected.Code)
	}
	deleted := accountRequest(t, handler, http.MethodDelete, "/api/v1/system/workspace/"+string(envelope.Data.ID), "", adminToken)
	if deleted.Code != http.StatusOK {
		t.Fatalf("workspace delete: %d %s", deleted.Code, deleted.Body.String())
	}
	admin, _, err := service.Store.User(context.Background(), "1")
	if err != nil || admin.canUse(envelope.Data.ID) {
		t.Fatalf("deleted workspace remained in creator membership: %+v %v", admin, err)
	}
	listed := accountRequest(t, handler, http.MethodGet, "/api/v1/system/workspace", "", adminToken)
	if listed.Code != http.StatusOK || strings.Contains(listed.Body.String(), `"name":"Temporary"`) {
		t.Fatalf("deleted workspace still listed: %d %s", listed.Code, listed.Body.String())
	}
}

func TestManagedWorkspaceCreateRollsBackOnAuditFailure(t *testing.T) {
	service, handler, adminToken, _ := adminUsersFixture(t)
	if _, err := service.Store.db.ExecContext(context.Background(), `CREATE TRIGGER reject_workspace_audit BEFORE INSERT ON chatbi_objects WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(ABORT,'synthetic audit fault'); END`); err != nil {
		t.Fatal(err)
	}
	created := accountRequest(t, handler, http.MethodPost, "/api/v1/system/workspace", `{"name":"Research"}`, adminToken)
	if created.Code != http.StatusServiceUnavailable {
		t.Fatalf("audit failure accepted workspace: %d %s", created.Code, created.Body.String())
	}
	listed := accountRequest(t, handler, http.MethodGet, "/api/v1/system/workspace", "", adminToken)
	if listed.Code != http.StatusOK || strings.Contains(listed.Body.String(), `"name":"Research"`) {
		t.Fatalf("failed workspace persisted: %d %s", listed.Code, listed.Body.String())
	}
	admin, _, err := service.Store.User(context.Background(), "1")
	if err != nil || len(admin.Memberships) != 1 {
		t.Fatalf("failed creation altered creator membership: %+v %v", admin, err)
	}
}

func TestManagedWorkspaceRenameRollsBackOnAuditFailure(t *testing.T) {
	service, handler, adminToken, _ := adminUsersFixture(t)
	if _, err := service.Store.db.ExecContext(context.Background(), `CREATE TRIGGER reject_workspace_rename_audit BEFORE INSERT ON chatbi_objects WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(ABORT,'synthetic audit fault'); END`); err != nil {
		t.Fatal(err)
	}
	updated := accountRequest(t, handler, http.MethodPut, "/api/v1/system/workspace", `{"id":1,"name":"Core"}`, adminToken)
	if updated.Code != http.StatusServiceUnavailable {
		t.Fatalf("audit failure accepted rename: %d %s", updated.Code, updated.Body.String())
	}
	for _, id := range []ID{"1", "2"} {
		user, _, err := service.Store.User(context.Background(), id)
		if err != nil || user.Memberships[0].Name != "One" {
			t.Fatalf("failed rename changed member: %+v %v", user, err)
		}
	}
	listed := accountRequest(t, handler, http.MethodGet, "/api/v1/system/workspace", "", adminToken)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"name":"One"`) {
		t.Fatalf("failed rename changed catalog: %d %s", listed.Code, listed.Body.String())
	}
}

func TestManagedWorkspaceDeleteRollsBackOnAuditFailure(t *testing.T) {
	service, handler, adminToken, _ := adminUsersFixture(t)
	created := accountRequest(t, handler, http.MethodPost, "/api/v1/system/workspace", `{"name":"Temporary"}`, adminToken)
	var envelope struct {
		Data struct {
			ID ID `json:"id"`
		} `json:"data"`
	}
	if created.Code != http.StatusOK || json.Unmarshal(created.Body.Bytes(), &envelope) != nil || !envelope.Data.ID.Valid() {
		t.Fatalf("workspace create: %d %s", created.Code, created.Body.String())
	}
	if _, err := service.Store.db.ExecContext(context.Background(), `CREATE TRIGGER reject_workspace_delete_audit BEFORE INSERT ON chatbi_objects WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(ABORT,'synthetic audit fault'); END`); err != nil {
		t.Fatal(err)
	}
	deleted := accountRequest(t, handler, http.MethodDelete, "/api/v1/system/workspace/"+string(envelope.Data.ID), "", adminToken)
	if deleted.Code != http.StatusServiceUnavailable {
		t.Fatalf("audit failure accepted delete: %d %s", deleted.Code, deleted.Body.String())
	}
	listed := accountRequest(t, handler, http.MethodGet, "/api/v1/system/workspace", "", adminToken)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"name":"Temporary"`) {
		t.Fatalf("failed delete removed workspace: %d %s", listed.Code, listed.Body.String())
	}
	admin, _, err := service.Store.User(context.Background(), "1")
	if err != nil || !admin.canUse(envelope.Data.ID) {
		t.Fatalf("failed delete removed creator membership: %+v %v", admin, err)
	}
}
