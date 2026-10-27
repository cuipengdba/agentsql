package chatbi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func workspaceAdminFixture(t *testing.T) (*Service, http.Handler, string, string) {
	t.Helper()
	service, _, _, bobToken := adminUsersFixture(t)
	admin, revision, err := service.Store.User(context.Background(), "1")
	if err != nil {
		t.Fatal(err)
	}
	admin.Memberships = append(admin.Memberships, Membership{Workspace: "2", Name: "Two", Weight: 1})
	if err := service.Store.put(context.Background(), userKey(admin.ID), admin, revision); err != nil {
		t.Fatal(err)
	}
	alice := testUser(t, "3", "alice")
	if err := service.Store.CreateUser(context.Background(), alice); err != nil {
		t.Fatal(err)
	}
	_, adminToken, err := service.Identity.Login(context.Background(), "analyst", "Synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	return service, handler, adminToken, bobToken
}

func TestWorkspaceMemberBatchIsAuthorizedAuditedAndRevocable(t *testing.T) {
	service, handler, adminToken, bobToken := workspaceAdminFixture(t)
	list := accountRequest(t, handler, http.MethodGet, "/api/v1/system/workspace", "", adminToken)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"name":"One"`) || !strings.Contains(list.Body.String(), `"name":"Two"`) {
		t.Fatalf("workspace list: %d %s", list.Code, list.Body.String())
	}
	if denied := accountRequest(t, handler, http.MethodGet, "/api/v1/system/workspace", "", bobToken); denied.Code != http.StatusUnauthorized {
		t.Fatalf("non-admin listed workspaces: %d", denied.Code)
	}
	if missing := accountRequest(t, handler, http.MethodGet, "/api/v1/system/workspace/3", "", adminToken); missing.Code != http.StatusNotFound {
		t.Fatalf("unknown workspace was returned: %d", missing.Code)
	}
	candidates := accountRequest(t, handler, http.MethodGet, "/api/v1/system/workspace/uws/option/pager/1/10?oid=2", "", adminToken)
	if candidates.Code != http.StatusOK || !strings.Contains(candidates.Body.String(), `"total":2`) ||
		strings.Contains(candidates.Body.String(), "password_hash") || strings.Contains(candidates.Body.String(), "bob@example.test") {
		t.Fatalf("unsafe member candidates: %d %s", candidates.Code, candidates.Body.String())
	}
	if denied := accountRequest(t, handler, http.MethodGet, "/api/v1/system/workspace/uws/option/pager/1/10?oid=2", "", bobToken); denied.Code != http.StatusUnauthorized {
		t.Fatalf("non-admin listed candidates: %d", denied.Code)
	}
	if missing := accountRequest(t, handler, http.MethodGet, "/api/v1/system/workspace/uws/option/pager/1/10?oid=3", "", adminToken); missing.Code != http.StatusNotFound {
		t.Fatalf("unknown workspace supplied candidates: %d %s", missing.Code, missing.Body.String())
	}
	grant := accountRequest(t, handler, http.MethodPost, "/api/v1/system/workspace/uws", `{"uid_list":[2,3],"oid":2,"weight":0}`, adminToken)
	if grant.Code != http.StatusOK {
		t.Fatalf("grant batch: %d %s", grant.Code, grant.Body.String())
	}
	for _, id := range []ID{"2", "3"} {
		user, _, err := service.Store.User(context.Background(), id)
		if err != nil || !user.canUse("2") {
			t.Fatalf("member %s was not granted: %v", id, err)
		}
	}
	members := accountRequest(t, handler, http.MethodGet, "/api/v1/system/workspace/uws/pager/1/10?oid=2", "", adminToken)
	if members.Code != http.StatusOK || !strings.Contains(members.Body.String(), `"total":2`) ||
		strings.Contains(members.Body.String(), "password_hash") {
		t.Fatalf("unsafe member page: %d %s", members.Code, members.Body.String())
	}
	if _, err := service.Identity.Verify(context.Background(), bobToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("grant did not invalidate prior member token")
	}
	_, bobAfterGrant, err := service.Identity.Login(context.Background(), "bob", "Synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	weight := accountRequest(t, handler, http.MethodPut, "/api/v1/system/workspace/uws", `{"uid":2,"oid":2,"weight":1}`, adminToken)
	if weight.Code != http.StatusOK {
		t.Fatalf("member weight: %d %s", weight.Code, weight.Body.String())
	}
	if _, err := service.Identity.Verify(context.Background(), bobAfterGrant); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("weight change did not invalidate prior member token")
	}
	bob, _, err := service.Store.User(context.Background(), "2")
	if err != nil || bob.Memberships[1].Weight != 1 {
		t.Fatalf("member weight was not persisted: %+v %v", bob, err)
	}
	revoke := accountRequest(t, handler, http.MethodDelete, "/api/v1/system/workspace/uws", `{"uid_list":[2,3],"oid":2}`, adminToken)
	if revoke.Code != http.StatusOK {
		t.Fatalf("revoke batch: %d %s", revoke.Code, revoke.Body.String())
	}
	for _, id := range []ID{"2", "3"} {
		user, _, err := service.Store.User(context.Background(), id)
		if err != nil || user.canUse("2") || !user.canUse("1") {
			t.Fatalf("member %s revoke failed: %+v %v", id, user, err)
		}
	}
	audits, err := service.Store.list(context.Background(), "management-audit", "system", "system")
	if err != nil || len(audits) != 3 || !strings.Contains(string(audits[0]), "workspace.member.") {
		t.Fatalf("membership audit chain: %v %#v", err, audits)
	}
}

func TestWorkspaceMemberBatchRollsBackOnAuditFailure(t *testing.T) {
	service, handler, adminToken, bobToken := workspaceAdminFixture(t)
	if _, err := service.Store.db.ExecContext(context.Background(), `CREATE TRIGGER reject_member_audit BEFORE INSERT ON chatbi_objects WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(ABORT,'synthetic audit fault'); END`); err != nil {
		t.Fatal(err)
	}
	grant := accountRequest(t, handler, http.MethodPost, "/api/v1/system/workspace/uws", `{"uid_list":[2,3],"oid":2}`, adminToken)
	if grant.Code != http.StatusServiceUnavailable {
		t.Fatalf("audit failure accepted batch: %d %s", grant.Code, grant.Body.String())
	}
	for _, id := range []ID{"2", "3"} {
		user, _, err := service.Store.User(context.Background(), id)
		if err != nil || user.canUse("2") || user.AuthRevision != 1 {
			t.Fatalf("failed batch partially changed user %s: %+v %v", id, user, err)
		}
	}
	if _, err := service.Identity.Verify(context.Background(), bobToken); err != nil {
		t.Fatalf("failed batch revoked original token: %v", err)
	}
}

func TestWorkspaceMemberAdministratorScope(t *testing.T) {
	service, handler, adminToken, _ := workspaceAdminFixture(t)
	ctx := context.Background()
	alice, revision, err := service.Store.User(ctx, "3")
	if err != nil {
		t.Fatal(err)
	}
	alice.Memberships[0].Weight = 1
	if err := service.Store.put(ctx, userKey(alice.ID), alice, revision); err != nil {
		t.Fatal(err)
	}
	charlie := testUser(t, "4", "charlie")
	charlie.Workspace = "2"
	charlie.Memberships = []Membership{{Workspace: "2", Name: "Two"}}
	if err := service.Store.CreateUser(ctx, charlie); err != nil {
		t.Fatal(err)
	}
	_, aliceToken, err := service.Identity.Login(ctx, "alice", "Synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	if denied := accountRequest(t, handler, http.MethodPost, "/api/v1/system/workspace/uws", `{"uid_list":[4],"oid":2}`, aliceToken); denied.Code != http.StatusUnauthorized {
		t.Fatalf("workspace admin changed another workspace: %d %s", denied.Code, denied.Body.String())
	}
	if denied := accountRequest(t, handler, http.MethodGet, "/api/v1/system/workspace/uws/pager/1/10?oid=2", "", aliceToken); denied.Code != http.StatusUnauthorized {
		t.Fatalf("workspace admin listed another workspace: %d %s", denied.Code, denied.Body.String())
	}
	candidate := accountRequest(t, handler, http.MethodGet, "/api/v1/system/workspace/uws/option?keyword=charlie", "", aliceToken)
	if candidate.Code != http.StatusOK || !strings.Contains(candidate.Body.String(), `"account":"charlie"`) {
		t.Fatalf("workspace admin could not find candidate: %d %s", candidate.Code, candidate.Body.String())
	}
	if denied := accountRequest(t, handler, http.MethodPut, "/api/v1/system/workspace/uws", `{"uid":4,"oid":2,"weight":1}`, aliceToken); denied.Code != http.StatusUnauthorized {
		t.Fatalf("workspace admin changed member roles: %d %s", denied.Code, denied.Body.String())
	}
	if denied := accountRequest(t, handler, http.MethodPost, "/api/v1/system/workspace/uws", `{"uid_list":[4],"oid":1,"weight":1}`, aliceToken); denied.Code != http.StatusUnauthorized {
		t.Fatalf("workspace admin promoted new member: %d %s", denied.Code, denied.Body.String())
	}
	grant := accountRequest(t, handler, http.MethodPost, "/api/v1/system/workspace/uws", `{"uid_list":[4],"oid":1}`, aliceToken)
	if grant.Code != http.StatusOK {
		t.Fatalf("workspace admin could not grant own member: %d %s", grant.Code, grant.Body.String())
	}
	member, _, err := service.Store.User(ctx, "4")
	if err != nil || !member.canUse("1") {
		t.Fatalf("membership grant failed: %+v %v", member, err)
	}
	if denied := accountRequest(t, handler, http.MethodPut, "/api/v1/system/workspace/uws", `{"uid":4,"oid":1,"weight":1}`, aliceToken); denied.Code != http.StatusUnauthorized {
		t.Fatalf("workspace admin promoted member: %d %s", denied.Code, denied.Body.String())
	}
	revoke := accountRequest(t, handler, http.MethodDelete, "/api/v1/system/workspace/uws", `{"uid_list":[4],"oid":1}`, aliceToken)
	if revoke.Code != http.StatusOK {
		t.Fatalf("workspace admin could not revoke own member: %d %s", revoke.Code, revoke.Body.String())
	}
	if denied := accountRequest(t, handler, http.MethodPost, "/api/v1/system/workspace/uws", `{"uid_list":[4],"oid":2}`, adminToken); denied.Code != http.StatusConflict {
		t.Fatalf("duplicate admin grant was accepted: %d %s", denied.Code, denied.Body.String())
	}
}
