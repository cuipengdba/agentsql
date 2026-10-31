package chatbi

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func accountRequest(t *testing.T, handler http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestAccountPasswordChangeRevokesSessionsAndAudits(t *testing.T) {
	service, _, _, _, token := serviceFixture(t)
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/user/pwd"
	if response := accountRequest(t, handler, http.MethodPut, path, `{"pwd":"wrong","new_pwd":"Another-password-123"}`, token); response.Code != http.StatusUnauthorized {
		t.Fatalf("wrong current password accepted: %d", response.Code)
	}
	if response := accountRequest(t, handler, http.MethodPut, path, `{"pwd":"Synthetic-password-1","new_pwd":"Another-password-123"}`, token); response.Code != http.StatusOK {
		t.Fatalf("valid change failed: %d %s", response.Code, response.Body.String())
	}
	if _, err := service.Identity.Verify(context.Background(), token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("old session survived password change")
	}
	if _, _, err := service.Identity.Login(context.Background(), "analyst", "Synthetic-password-1"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("old password survived change")
	}
	if _, _, err := service.Identity.Login(context.Background(), "analyst", "Another-password-123"); err != nil {
		t.Fatalf("new password rejected: %v", err)
	}
	audits, err := service.Store.list(context.Background(), "management-audit", "system", "system")
	if err != nil || len(audits) != 1 || !strings.Contains(string(audits[0]), "user.password.change") {
		t.Fatalf("password change was not durably audited: %v %#v", err, audits)
	}
}

func TestAccountPasswordChangeRollsBackWhenAuditFails(t *testing.T) {
	service, _, _, _, token := serviceFixture(t)
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Store.db.ExecContext(context.Background(), `CREATE TRIGGER reject_password_audit BEFORE INSERT ON chatbi_objects WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(ABORT,'synthetic audit fault'); END`); err != nil {
		t.Fatal(err)
	}
	response := accountRequest(t, handler, http.MethodPut, "/api/v1/user/pwd", `{"pwd":"Synthetic-password-1","new_pwd":"Another-password-123"}`, token)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("audit failure did not close password change: %d", response.Code)
	}
	if _, err := service.Identity.Verify(context.Background(), token); err != nil {
		t.Fatal("audit failure revoked current session")
	}
	if _, _, err := service.Identity.Login(context.Background(), "analyst", "Synthetic-password-1"); err != nil {
		t.Fatal("audit failure changed the old password")
	}
	if _, _, err := service.Identity.Login(context.Background(), "analyst", "Another-password-123"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("audit failure committed the replacement password")
	}
}

func TestAccountLanguageChangeRollsBackOnAuditFailure(t *testing.T) {
	service, p, _, _, token := serviceFixture(t)
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/user/language"
	if response := accountRequest(t, handler, http.MethodPut, path, `{"language":"invalid"}`, token); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unsupported language accepted: %d", response.Code)
	}
	if _, err := service.Store.db.ExecContext(context.Background(), `CREATE TRIGGER reject_account_audit BEFORE INSERT ON chatbi_objects WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(ABORT,'synthetic audit fault'); END`); err != nil {
		t.Fatal(err)
	}
	if response := accountRequest(t, handler, http.MethodPut, path, `{"language":"en-US"}`, token); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("audit failure was not closed: %d", response.Code)
	}
	stored, _, err := service.Store.User(context.Background(), p.User.ID)
	if err != nil || stored.Language != "zh-CN" {
		t.Fatal("audit failure changed account language")
	}
	if _, err := service.Store.db.ExecContext(context.Background(), `DROP TRIGGER reject_account_audit`); err != nil {
		t.Fatal(err)
	}
	if response := accountRequest(t, handler, http.MethodPut, path, `{"language":"en-US"}`, token); response.Code != http.StatusOK {
		t.Fatalf("valid language change failed: %d", response.Code)
	}
	stored, _, err = service.Store.User(context.Background(), p.User.ID)
	if err != nil || stored.Language != "en-US" {
		t.Fatal("language was not persisted")
	}
	if _, err := service.Identity.Verify(context.Background(), token); err != nil {
		t.Fatal("language change unexpectedly revoked account token")
	}
}

func TestLoginPasswordChangeRequiresCurrentPassword(t *testing.T) {
	service, _, _, _, token := serviceFixture(t)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	service.Identity.privateKey = key
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/login/change-pwd"
	encode := func(value string) string {
		cipher, err := rsa.EncryptPKCS1v15(rand.Reader, &key.PublicKey, []byte(value))
		if err != nil {
			t.Fatal(err)
		}
		return base64.StdEncoding.EncodeToString(cipher)
	}
	body := func(current string) string {
		encoded, err := json.Marshal(map[string]string{"account": encode("analyst"), "pwd": encode(current), "new_pwd": encode("New-password-123")})
		if err != nil {
			t.Fatal(err)
		}
		return string(encoded)
	}
	if response := accountRequest(t, handler, http.MethodPost, path, `{"account":"analyst","pwd":"Synthetic-password-1","new_pwd":"New-password-123"}`, ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("plaintext legacy change accepted: %d", response.Code)
	}
	if response := accountRequest(t, handler, http.MethodPost, path, body("incorrect"), ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password accepted: %d", response.Code)
	}
	if response := accountRequest(t, handler, http.MethodPost, path, body("Synthetic-password-1"), ""); response.Code != http.StatusOK {
		t.Fatalf("login password change failed: %d %s", response.Code, response.Body.String())
	}
	if _, err := service.Identity.Verify(context.Background(), token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("old token survived login password change")
	}
}
