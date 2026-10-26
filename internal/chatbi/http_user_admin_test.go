package chatbi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func adminUsersFixture(t *testing.T) (*Service, http.Handler, string, string) {
	t.Helper()
	service, p, _, _, adminToken := serviceFixture(t)
	admin, revision, err := service.Store.User(context.Background(), p.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	admin.Admin = true
	if err := service.Store.put(context.Background(), userKey(admin.ID), admin, revision); err != nil {
		t.Fatal(err)
	}
	bob := testUser(t, "2", "bob")
	bob.Name, bob.Email, bob.Created = "Bob", "bob@example.test", 1730000000000
	if err := service.Store.CreateUser(context.Background(), bob); err != nil {
		t.Fatal(err)
	}
	_, bobToken, err := service.Identity.Login(context.Background(), "bob", "Synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	return service, handler, adminToken, bobToken
}

func TestAdminUserPageReadAndStatusRevocation(t *testing.T) {
	service, handler, adminToken, bobToken := adminUsersFixture(t)
	page := accountRequest(t, handler, http.MethodGet, "/api/v1/user/pager/1/10?keyword=bob&status=1&origins=0&oidlist=1", "", adminToken)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `"total":1`) ||
		!strings.Contains(page.Body.String(), `"email":"bob@example.test"`) ||
		strings.Contains(page.Body.String(), "password_hash") || strings.Contains(page.Body.String(), "Synthetic-password-1") {
		t.Fatalf("unsafe user page: %d %s", page.Code, page.Body.String())
	}
	empty := accountRequest(t, handler, http.MethodGet, "/api/v1/user/pager/1/10?oidlist=2", "", adminToken)
	if empty.Code != http.StatusOK || !strings.Contains(empty.Body.String(), `"total":0`) {
		t.Fatalf("workspace filter ignored: %d %s", empty.Code, empty.Body.String())
	}
	read := accountRequest(t, handler, http.MethodGet, "/api/v1/user/2", "", adminToken)
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), `"account":"bob"`) || strings.Contains(read.Body.String(), "password_hash") {
		t.Fatalf("unsafe user read: %d %s", read.Code, read.Body.String())
	}
	for _, path := range []string{"/api/v1/user/2", "/api/v1/user/pager/1/10"} {
		if denied := accountRequest(t, handler, http.MethodGet, path, "", bobToken); denied.Code != http.StatusUnauthorized {
			t.Fatalf("non-admin read %s: %d", path, denied.Code)
		}
	}
	if denied := accountRequest(t, handler, http.MethodPatch, "/api/v1/user/status", `{"id":1,"status":0}`, adminToken); denied.Code != http.StatusForbidden {
		t.Fatalf("admin could disable own account: %d", denied.Code)
	}
	disabled := accountRequest(t, handler, http.MethodPatch, "/api/v1/user/status", `{"id":2,"status":0}`, adminToken)
	if disabled.Code != http.StatusOK {
		t.Fatalf("disable user: %d %s", disabled.Code, disabled.Body.String())
	}
	if _, err := service.Identity.Verify(context.Background(), bobToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("disabled user retained a live token")
	}
	audits, err := service.Store.list(context.Background(), "management-audit", "system", "system")
	if err != nil || len(audits) != 1 || !strings.Contains(string(audits[0]), "user.status.change") {
		t.Fatalf("status change lacked durable audit: %v %#v", err, audits)
	}
	reenabled := accountRequest(t, handler, http.MethodPatch, "/api/v1/user/status", `{"id":2,"status":1}`, adminToken)
	if reenabled.Code != http.StatusOK {
		t.Fatalf("reenable user: %d", reenabled.Code)
	}
	if _, err := service.Identity.Verify(context.Background(), bobToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("old user token survived status rotation")
	}
	if _, _, err := service.Identity.Login(context.Background(), "bob", "Synthetic-password-1"); err != nil {
		t.Fatalf("reenabled user could not log in: %v", err)
	}
}

func TestAdminUserStatusRollsBackOnAuditFailure(t *testing.T) {
	service, handler, adminToken, bobToken := adminUsersFixture(t)
	if _, err := service.Store.db.ExecContext(context.Background(), `CREATE TRIGGER reject_user_audit BEFORE INSERT ON chatbi_objects WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(ABORT,'synthetic audit fault'); END`); err != nil {
		t.Fatal(err)
	}
	response := accountRequest(t, handler, http.MethodPatch, "/api/v1/user/status", `{"id":2,"status":0}`, adminToken)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("audit failure did not reject status update: %d", response.Code)
	}
	if _, err := service.Identity.Verify(context.Background(), bobToken); err != nil {
		t.Fatalf("failed status update revoked user token: %v", err)
	}
	user, _, err := service.Store.User(context.Background(), "2")
	if err != nil || user.Status != 1 || user.AuthRevision != 1 {
		t.Fatalf("audit failure changed user: %+v %v", user, err)
	}
}
