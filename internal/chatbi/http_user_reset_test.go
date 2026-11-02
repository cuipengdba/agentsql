package chatbi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestAdminResetPasswordRevokesSessionsAndAudits(t *testing.T) {
	service, handler, adminToken, bobToken := adminUsersFixture(t)
	if denied := accountRequest(t, handler, http.MethodPatch, "/api/v1/user/pwd/2", `{"new_password":"Synthetic-password-6"}`, bobToken); denied.Code != http.StatusUnauthorized {
		t.Fatalf("ordinary user reset password: %d %s", denied.Code, denied.Body.String())
	}
	reset := accountRequest(t, handler, http.MethodPatch, "/api/v1/user/pwd/2", `{"new_password":"Synthetic-password-6"}`, adminToken)
	if reset.Code != http.StatusOK || strings.Contains(reset.Body.String(), "Synthetic-password-6") {
		t.Fatalf("admin password reset: %d %s", reset.Code, reset.Body.String())
	}
	if _, err := service.Identity.Verify(context.Background(), bobToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("old user token survived password reset")
	}
	if _, _, err := service.Identity.Login(context.Background(), "bob", "Synthetic-password-1"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("old password remained valid: %v", err)
	}
	if _, _, err := service.Identity.Login(context.Background(), "bob", "Synthetic-password-6"); err != nil {
		t.Fatalf("reset password cannot log in: %v", err)
	}
	audits, err := service.Store.list(context.Background(), "management-audit", "system", "system")
	if err != nil || len(audits) != 1 || !strings.Contains(string(audits[0]), "user.password.reset") {
		t.Fatalf("reset audit missing: %v %#v", err, audits)
	}
}

func TestAdminResetPasswordUsesConfiguredValueAndRollsBack(t *testing.T) {
	t.Setenv("AGENTSQL_CHATBI_INITIAL_USER_PASSWORD", "Synthetic-password-7")
	service, handler, adminToken, bobToken := adminUsersFixture(t)
	if invalid := accountRequest(t, handler, http.MethodPatch, "/api/v1/user/pwd/2", `{"new_password":"short"}`, adminToken); invalid.Code != http.StatusUnprocessableEntity {
		t.Fatalf("weak reset password accepted: %d", invalid.Code)
	}
	if _, err := service.Store.db.ExecContext(context.Background(), `CREATE TRIGGER reject_reset_audit BEFORE INSERT ON chatbi_objects WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(ABORT,'synthetic audit fault'); END`); err != nil {
		t.Fatal(err)
	}
	reset := accountRequest(t, handler, http.MethodPatch, "/api/v1/user/pwd/2", "", adminToken)
	if reset.Code != http.StatusServiceUnavailable {
		t.Fatalf("audit failure accepted reset: %d %s", reset.Code, reset.Body.String())
	}
	if _, err := service.Identity.Verify(context.Background(), bobToken); err != nil {
		t.Fatalf("failed reset revoked session: %v", err)
	}
	if _, _, err := service.Identity.Login(context.Background(), "bob", "Synthetic-password-7"); err == nil {
		t.Fatal("failed reset changed password")
	}
}
