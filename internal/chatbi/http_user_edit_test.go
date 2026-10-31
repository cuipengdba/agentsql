package chatbi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestManagedUserUpdateChangesMembershipAndRevokesToken(t *testing.T) {
	service, handler, adminToken, bobToken := workspaceAdminFixture(t)
	input := `{"id":2,"account":"bob","name":"Robert","email":"robert@example.test","status":0,"oid_list":[2]}`
	if denied := accountRequest(t, handler, http.MethodPut, "/api/v1/user", input, bobToken); denied.Code != http.StatusUnauthorized {
		t.Fatalf("ordinary user edited an account: %d %s", denied.Code, denied.Body.String())
	}
	updated := accountRequest(t, handler, http.MethodPut, "/api/v1/user", input, adminToken)
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"name":"Robert"`) {
		t.Fatalf("managed update: %d %s", updated.Code, updated.Body.String())
	}
	bob, _, err := service.Store.User(context.Background(), "2")
	if err != nil || bob.Status != 0 || bob.Workspace != "2" || len(bob.Memberships) != 1 || bob.Memberships[0].Workspace != "2" || bob.Name != "Robert" || bob.AuthRevision != 2 {
		t.Fatalf("managed user update was not persisted: %+v %v", bob, err)
	}
	if _, err := service.Identity.Verify(context.Background(), bobToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("old token survived user edit")
	}
	if changedAccount := accountRequest(t, handler, http.MethodPut, "/api/v1/user", `{"id":2,"account":"different","name":"Robert","oid_list":[2]}`, adminToken); changedAccount.Code != http.StatusUnprocessableEntity {
		t.Fatalf("account rename was accepted: %d %s", changedAccount.Code, changedAccount.Body.String())
	}
	audits, err := service.Store.list(context.Background(), "management-audit", "system", "system")
	if err != nil || len(audits) != 1 || !strings.Contains(string(audits[0]), "user.update") {
		t.Fatalf("managed edit audit: %v %#v", err, audits)
	}
}

func TestManagedUserUpdateRollsBackOnAuditFailure(t *testing.T) {
	service, handler, adminToken, bobToken := adminUsersFixture(t)
	if _, err := service.Store.db.ExecContext(context.Background(), `CREATE TRIGGER reject_user_update_audit BEFORE INSERT ON chatbi_objects WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(ABORT,'synthetic audit fault'); END`); err != nil {
		t.Fatal(err)
	}
	updated := accountRequest(t, handler, http.MethodPut, "/api/v1/user", `{"id":2,"account":"bob","name":"Robert","oid_list":[1]}`, adminToken)
	if updated.Code != http.StatusServiceUnavailable {
		t.Fatalf("audit failure accepted edit: %d %s", updated.Code, updated.Body.String())
	}
	bob, _, err := service.Store.User(context.Background(), "2")
	if err != nil || bob.Name != "Bob" || bob.AuthRevision != 1 {
		t.Fatalf("failed audit changed account: %+v %v", bob, err)
	}
	if _, err := service.Identity.Verify(context.Background(), bobToken); err != nil {
		t.Fatalf("failed edit revoked original token: %v", err)
	}
}
