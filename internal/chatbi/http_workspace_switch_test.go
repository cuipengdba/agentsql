package chatbi

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func memberWorkspaceSwitchFixture(t *testing.T) (*Service, http.Handler, string) {
	t.Helper()
	service, p, _, _, token := serviceFixture(t)
	user, revision, err := service.Store.User(context.Background(), p.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	user.Memberships = append(user.Memberships, Membership{Workspace: "2", Name: "Two"})
	if err := service.Store.put(context.Background(), userKey(user.ID), user, revision); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	return service, handler, token
}

func TestMemberWorkspaceSwitchPersistsForCurrentAndNewTokens(t *testing.T) {
	service, handler, token := memberWorkspaceSwitchFixture(t)
	if denied := accountRequest(t, handler, http.MethodPut, "/api/v1/user/ws/3", "", token); denied.Code != http.StatusUnauthorized {
		t.Fatalf("nonmember switched workspace: %d", denied.Code)
	}
	switched := accountRequest(t, handler, http.MethodPut, "/api/v1/user/ws/2", "", token)
	if switched.Code != http.StatusOK {
		t.Fatalf("switch: %d %s", switched.Code, switched.Body.String())
	}
	current, err := service.Identity.Verify(context.Background(), token)
	if err != nil || current.Workspace != "2" {
		t.Fatalf("old token did not select new workspace: %+v %v", current, err)
	}
	info := accountRequest(t, handler, http.MethodGet, "/api/v1/user/info", "", token)
	if info.Code != http.StatusOK || !strings.Contains(info.Body.String(), `"oid":"2"`) {
		t.Fatalf("user info retained old workspace: %d %s", info.Code, info.Body.String())
	}
	user, _, err := service.Store.User(context.Background(), "1")
	if err != nil || user.Workspace != "2" {
		t.Fatalf("new login default not saved: %+v %v", user, err)
	}
	_, freshToken, err := service.Identity.Login(context.Background(), user.Account, "Synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := service.Identity.Verify(context.Background(), freshToken)
	if err != nil || fresh.Workspace != "2" {
		t.Fatalf("new token did not start in selected workspace: %+v %v", fresh, err)
	}
	back := accountRequest(t, handler, http.MethodPut, "/api/v1/user/ws/1", "", token)
	if back.Code != http.StatusOK {
		t.Fatalf("switch back: %d %s", back.Code, back.Body.String())
	}
	current, err = service.Identity.Verify(context.Background(), token)
	if err != nil || current.Workspace != "1" {
		t.Fatalf("token selection CAS failed: %+v %v", current, err)
	}
}

func TestMemberWorkspaceSwitchRollsBackOnAuditFailure(t *testing.T) {
	service, handler, token := memberWorkspaceSwitchFixture(t)
	if _, err := service.Store.db.ExecContext(context.Background(), `CREATE TRIGGER reject_switch_audit BEFORE INSERT ON chatbi_objects WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(ABORT,'synthetic audit fault'); END`); err != nil {
		t.Fatal(err)
	}
	switched := accountRequest(t, handler, http.MethodPut, "/api/v1/user/ws/2", "", token)
	if switched.Code != http.StatusServiceUnavailable {
		t.Fatalf("audit failure allowed switch: %d %s", switched.Code, switched.Body.String())
	}
	current, err := service.Identity.Verify(context.Background(), token)
	if err != nil || current.Workspace != "1" {
		t.Fatalf("failed switch changed token scope: %+v %v", current, err)
	}
	user, _, err := service.Store.User(context.Background(), "1")
	if err != nil || user.Workspace != "1" {
		t.Fatalf("failed switch changed default: %+v %v", user, err)
	}
}
