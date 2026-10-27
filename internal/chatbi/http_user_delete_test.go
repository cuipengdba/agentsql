package chatbi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestAdminUserDeleteRevokesAndPurgesPrivateData(t *testing.T) {
	service, handler, adminToken, bobToken := adminUsersFixture(t)
	ctx := context.Background()
	privateKey := objectKey{"chat", "1", "2", "private-chat"}
	if err := service.Store.put(ctx, privateKey, "private", 0); err != nil {
		t.Fatal(err)
	}
	if denied := accountRequest(t, handler, http.MethodDelete, "/api/v1/user/2", "", bobToken); denied.Code != http.StatusUnauthorized {
		t.Fatalf("ordinary user deleted account: %d", denied.Code)
	}
	if denied := accountRequest(t, handler, http.MethodDelete, "/api/v1/user/1", "", adminToken); denied.Code != http.StatusUnprocessableEntity {
		t.Fatalf("admin deleted own account: %d", denied.Code)
	}
	deleted := accountRequest(t, handler, http.MethodDelete, "/api/v1/user/2", "", adminToken)
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete user: %d %s", deleted.Code, deleted.Body.String())
	}
	if _, err := service.Identity.Verify(ctx, bobToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("deleted user retained token: %v", err)
	}
	if _, _, err := service.Identity.Login(ctx, "bob", "Synthetic-password-1"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("deleted account still logs in: %v", err)
	}
	var private string
	if _, err := service.Store.get(ctx, privateKey, &private); !errors.Is(err, ErrNotFound) {
		t.Fatalf("private data survived deletion: %v", err)
	}
	var accountID string
	if _, err := service.Store.get(ctx, service.Store.accountKey("bob"), &accountID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("account index survived deletion: %v", err)
	}
	audits, err := service.Store.list(ctx, "management-audit", "system", "system")
	if err != nil || len(audits) != 1 || !strings.Contains(string(audits[0]), "user.delete") {
		t.Fatalf("delete audit missing: %v %#v", err, audits)
	}
}

func TestAdminUserBatchDeleteAtomic(t *testing.T) {
	service, handler, adminToken, bobToken := adminUsersFixture(t)
	invalid := accountRequest(t, handler, http.MethodDelete, "/api/v1/user", `[2,999]`, adminToken)
	if invalid.Code != http.StatusNotFound {
		t.Fatalf("batch with missing user accepted: %d %s", invalid.Code, invalid.Body.String())
	}
	if _, err := service.Identity.Verify(context.Background(), bobToken); err != nil {
		t.Fatalf("partial batch removed first user: %v", err)
	}
	if duplicate := accountRequest(t, handler, http.MethodDelete, "/api/v1/user", `[2,2]`, adminToken); duplicate.Code != http.StatusUnprocessableEntity {
		t.Fatalf("duplicate batch accepted: %d", duplicate.Code)
	}
	deleted := accountRequest(t, handler, http.MethodDelete, "/api/v1/user", `[2]`, adminToken)
	if deleted.Code != http.StatusOK {
		t.Fatalf("batch delete: %d %s", deleted.Code, deleted.Body.String())
	}
	if _, err := service.Identity.Verify(context.Background(), bobToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("batch deleted user retained token: %v", err)
	}
}

func TestAdminUserDeleteAllowsFreshAccountWithoutOldToken(t *testing.T) {
	service, handler, adminToken, oldToken := adminUsersFixture(t)
	deleted := accountRequest(t, handler, http.MethodDelete, "/api/v1/user/2", "", adminToken)
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete user: %d %s", deleted.Code, deleted.Body.String())
	}
	created := accountRequest(t, handler, http.MethodPost, "/api/v1/user", `{"account":"bob","name":"New Bob","email":"bob-new@example.test","oid_list":[1],"initial_password":"New-password-123"}`, adminToken)
	if created.Code != http.StatusOK || strings.Contains(created.Body.String(), `"id":2`) {
		t.Fatalf("account reuse failed or reused identity: %d %s", created.Code, created.Body.String())
	}
	if _, err := service.Identity.Verify(context.Background(), oldToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("old token accessed recreated account: %v", err)
	}
	if _, _, err := service.Identity.Login(context.Background(), "bob", "New-password-123"); err != nil {
		t.Fatalf("new account login failed: %v", err)
	}
}

func TestAdminUserDeleteRollsBackOnAuditFailure(t *testing.T) {
	service, handler, adminToken, bobToken := adminUsersFixture(t)
	ctx := context.Background()
	privateKey := objectKey{"chat", "1", "2", "private-chat"}
	if err := service.Store.put(ctx, privateKey, "private", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Store.db.ExecContext(ctx, `CREATE TRIGGER reject_delete_audit BEFORE INSERT ON chatbi_objects WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(ABORT,'synthetic audit fault'); END`); err != nil {
		t.Fatal(err)
	}
	deleted := accountRequest(t, handler, http.MethodDelete, "/api/v1/user/2", "", adminToken)
	if deleted.Code != http.StatusServiceUnavailable {
		t.Fatalf("audit failure accepted deletion: %d %s", deleted.Code, deleted.Body.String())
	}
	if _, err := service.Identity.Verify(ctx, bobToken); err != nil {
		t.Fatalf("rollback revoked user: %v", err)
	}
	var private string
	if _, err := service.Store.get(ctx, privateKey, &private); err != nil || private != "private" {
		t.Fatalf("rollback removed private data: %q %v", private, err)
	}
}
