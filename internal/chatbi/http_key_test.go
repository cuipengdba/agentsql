package chatbi

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestSQLBotBrowserWorkspaceBootstrapIsCurrentUserOnly(t *testing.T) {
	service, _, _, _, token := serviceFixture(t)
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	request := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/user/ws", nil)
		r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	w := request()
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"id":1`) ||
		!strings.Contains(w.Body.String(), `"name":"One"`) {
		t.Fatalf("browser workspace bootstrap: %d %s", w.Code, w.Body.String())
	}
	if err := service.Identity.Logout(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	if w := request(); w.Code != http.StatusUnauthorized || strings.Contains(w.Body.String(), `"name":"One"`) {
		t.Fatal("logged-out browser session received workspace metadata")
	}
}

func TestSQLBotBrowserPublicKeyAndEncryptedLogin(t *testing.T) {
	service, _, _, _, _ := serviceFixture(t)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	service.Identity.privateKey = key
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	keyResponse := httptest.NewRecorder()
	handler.ServeHTTP(keyResponse, httptest.NewRequest(http.MethodGet, "/api/v1/system/config/key", nil))
	var envelope struct {
		Data struct {
			PublicKey string `json:"public_key"`
			Salt      string `json:"salt"`
		} `json:"data"`
	}
	if keyResponse.Code != http.StatusOK || json.Unmarshal(keyResponse.Body.Bytes(), &envelope) != nil ||
		len(envelope.Data.Salt) != 16 || strings.Contains(keyResponse.Body.String(), "PRIVATE KEY") {
		t.Fatalf("browser public key response: %d %s", keyResponse.Code, keyResponse.Body.String())
	}
	block, rest := pem.Decode([]byte(envelope.Data.PublicKey))
	if block == nil || block.Type != "PUBLIC KEY" || len(rest) != 0 {
		t.Fatal("browser key is not a PKIX public key")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	public, ok := parsed.(*rsa.PublicKey)
	if !ok || public.N.Cmp(key.N) != 0 {
		t.Fatal("browser received a different RSA key")
	}
	encrypt := func(value string) string {
		t.Helper()
		ciphertext, err := rsa.EncryptPKCS1v15(rand.Reader, public, []byte(value))
		if err != nil {
			t.Fatal(err)
		}
		return base64.StdEncoding.EncodeToString(ciphertext)
	}
	form := url.Values{"username": {encrypt("analyst")}, "password": {encrypt("Synthetic-password-1")}}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/login/access-token", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	login := httptest.NewRecorder()
	handler.ServeHTTP(login, request)
	if login.Code != http.StatusOK || !strings.Contains(login.Body.String(), `"access_token"`) {
		t.Fatalf("browser encrypted login: %d %s", login.Code, login.Body.String())
	}
	service.Identity.privateKey = nil
	unavailable := httptest.NewRecorder()
	handler.ServeHTTP(unavailable, httptest.NewRequest(http.MethodGet, "/api/v1/system/config/key", nil))
	if unavailable.Code != http.StatusServiceUnavailable || strings.Contains(unavailable.Body.String(), "PUBLIC KEY") {
		t.Fatal("missing legacy login key exposed an apparent browser login path")
	}
}
