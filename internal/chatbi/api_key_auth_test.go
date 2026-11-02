package chatbi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func signedAskToken(t *testing.T, key AccountAPIKey, expiry int64) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	claim, err := json.Marshal(map[string]any{"access_key": key.AccessKey, "exp": expiry})
	if err != nil {
		t.Fatal(err)
	}
	part := header + "." + base64.RawURLEncoding.EncodeToString(claim)
	mac := hmac.New(sha256.New, []byte(key.SecretKey))
	_, _ = mac.Write([]byte(part))
	return part + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func askRequest(handler http.Handler, path, token string, extra bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Header.Set("X-SQLBOT-ASK-TOKEN", "sk "+token)
	if extra {
		r.Header.Set("X-SQLBOT-TOKEN", "Bearer conflicting-token")
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestAskTokenChecksKeyStatusSignatureAndRoute(t *testing.T) {
	service, handler, adminToken, _ := adminUsersFixture(t)
	created := accountRequest(t, handler, http.MethodPost, "/api/v1/system/apikey", `{}`, adminToken)
	if created.Code != http.StatusOK {
		t.Fatalf("key creation: %d", created.Code)
	}
	var envelope struct {
		Data ID `json:"data"`
	}
	if json.Unmarshal(created.Body.Bytes(), &envelope) != nil {
		t.Fatal("cannot decode key ID")
	}
	var key AccountAPIKey
	if _, err := service.Store.get(context.Background(), apiKeyObject(envelope.Data, "1"), &key); err != nil {
		t.Fatal(err)
	}
	token := signedAskToken(t, key, time.Now().Add(time.Hour).Unix())
	if response := askRequest(handler, "/api/v1/chat/list", token, false); response.Code != http.StatusOK {
		t.Fatalf("signed key could not read own chats: %d %s", response.Code, response.Body.String())
	}
	for _, response := range []*httptest.ResponseRecorder{
		askRequest(handler, "/api/v1/system/apikey", token, false),
		askRequest(handler, "/api/v1/chat/list", token, true),
		askRequest(handler, "/api/v1/chat/list", token+"tampered", false),
		askRequest(handler, "/api/v1/chat/list", signedAskToken(t, key, time.Now().Add(-time.Hour).Unix()), false),
	} {
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("invalid ask token or route accepted: %d %s", response.Code, response.Body.String())
		}
	}
	if changed := accountRequest(t, handler, http.MethodPut, "/api/v1/system/apikey/status", `{"id":`+string(key.ID)+`,"status":false}`, adminToken); changed.Code != http.StatusOK {
		t.Fatalf("disable key: %d", changed.Code)
	}
	if response := askRequest(handler, "/api/v1/chat/list", token, false); response.Code != http.StatusUnauthorized {
		t.Fatalf("disabled key remained valid: %d", response.Code)
	}
	if changed := accountRequest(t, handler, http.MethodPut, "/api/v1/system/apikey/status", `{"id":`+string(key.ID)+`,"status":true}`, adminToken); changed.Code != http.StatusOK {
		t.Fatalf("enable key: %d", changed.Code)
	}
	if response := askRequest(handler, "/api/v1/chat/list", token, false); response.Code != http.StatusOK {
		t.Fatalf("re-enabled key not valid: %d", response.Code)
	}
	if deleted := accountRequest(t, handler, http.MethodDelete, "/api/v1/system/apikey/"+string(key.ID), "", adminToken); deleted.Code != http.StatusOK {
		t.Fatalf("delete key: %d", deleted.Code)
	}
	if response := askRequest(handler, "/api/v1/chat/list", token, false); response.Code != http.StatusUnauthorized || strings.Contains(response.Body.String(), key.SecretKey) {
		t.Fatalf("deleted key remained valid or leaked secret: %d %s", response.Code, response.Body.String())
	}
}
