package chatbi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestManagedUserCreateReservesAccountAndAudits(t *testing.T) {
	service, handler, adminToken, bobToken := adminUsersFixture(t)
	input := `{"account":"charlie","name":"Charlie","email":"charlie@example.test","oid_list":[1],"initial_password":"Synthetic-password-4"}`
	if denied := accountRequest(t, handler, http.MethodPost, "/api/v1/user", input, bobToken); denied.Code != http.StatusUnauthorized {
		t.Fatalf("ordinary user created an account: %d %s", denied.Code, denied.Body.String())
	}
	created := accountRequest(t, handler, http.MethodPost, "/api/v1/user", input, adminToken)
	if created.Code != http.StatusOK || !strings.Contains(created.Body.String(), `"account":"charlie"`) ||
		strings.Contains(created.Body.String(), "Synthetic-password-4") || strings.Contains(created.Body.String(), "password_hash") {
		t.Fatalf("managed create: %d %s", created.Code, created.Body.String())
	}
	if _, _, err := service.Identity.Login(context.Background(), "charlie", "Synthetic-password-4"); err != nil {
		t.Fatalf("created account cannot log in: %v", err)
	}
	if duplicate := accountRequest(t, handler, http.MethodPost, "/api/v1/user", input, adminToken); duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate account accepted: %d %s", duplicate.Code, duplicate.Body.String())
	}
	if invalid := accountRequest(t, handler, http.MethodPost, "/api/v1/user", `{"account":"dave","name":"Dave","oid_list":[5],"initial_password":"Synthetic-password-4"}`, adminToken); invalid.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown workspace accepted: %d %s", invalid.Code, invalid.Body.String())
	}
	audits, err := service.Store.list(context.Background(), "management-audit", "system", "system")
	if err != nil || len(audits) != 1 || !strings.Contains(string(audits[0]), "user.create") {
		t.Fatalf("managed create audit: %v %#v", err, audits)
	}
}

func TestManagedUserCreateRejectsRevokedAdministratorInsideTransaction(t *testing.T) {
	service, _, adminToken, _ := adminUsersFixture(t)
	ctx := context.Background()
	p, err := service.Identity.Verify(ctx, adminToken)
	if err != nil {
		t.Fatal(err)
	}
	admin, revision, err := service.Store.User(ctx, p.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	admin.AuthRevision++
	if err := service.Store.put(ctx, userKey(admin.ID), admin, revision); err != nil {
		t.Fatal(err)
	}
	newUser := testUser(t, "4", "dave")
	if err := service.Store.CreateUserManaged(ctx, newUser, p); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("revoked administrator created a user: %v", err)
	}
	if _, _, err := service.Identity.Login(ctx, "dave", "Synthetic-password-1"); err == nil {
		t.Fatal("user survived revoked administrator")
	}
}

func TestManagedUserCreateRollsBackWithoutAudit(t *testing.T) {
	service, handler, adminToken, _ := adminUsersFixture(t)
	if _, err := service.Store.db.ExecContext(context.Background(), `CREATE TRIGGER reject_user_create_audit BEFORE INSERT ON chatbi_objects WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(ABORT,'synthetic audit fault'); END`); err != nil {
		t.Fatal(err)
	}
	created := accountRequest(t, handler, http.MethodPost, "/api/v1/user", `{"account":"charlie","name":"Charlie","oid_list":[1],"initial_password":"Synthetic-password-4"}`, adminToken)
	if created.Code != http.StatusServiceUnavailable {
		t.Fatalf("audit failure accepted new account: %d %s", created.Code, created.Body.String())
	}
	if _, _, err := service.Identity.Login(context.Background(), "charlie", "Synthetic-password-4"); err == nil {
		t.Fatal("account survived failed audit")
	}
}

func TestManagedUserCreateAcceptsConfiguredInitialPassword(t *testing.T) {
	t.Setenv("AGENTSQL_CHATBI_INITIAL_USER_PASSWORD", "Synthetic-password-5")
	service, handler, adminToken, _ := adminUsersFixture(t)
	created := accountRequest(t, handler, http.MethodPost, "/api/v1/user", `{"account":"charlie","name":"Charlie","email":"charlie@example.test","oid_list":[1],"status":1}`, adminToken)
	if created.Code != http.StatusOK || strings.Contains(created.Body.String(), "Synthetic-password-5") {
		t.Fatalf("configured password create: %d %s", created.Code, created.Body.String())
	}
	if _, _, err := service.Identity.Login(context.Background(), "charlie", "Synthetic-password-5"); err != nil {
		t.Fatalf("created account cannot use configured password: %v", err)
	}
}
