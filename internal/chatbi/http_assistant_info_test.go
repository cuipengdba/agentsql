package chatbi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func originalSDKSignHeader(t *testing.T, key *rsa.PublicKey, payload assistantSignPayload) string {
	t.Helper()
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	var parts []string
	for offset := 0; offset < len(raw); offset += 245 {
		end := offset + 245
		if end > len(raw) {
			end = len(raw)
		}
		ciphertext, err := rsa.EncryptPKCS1v15(rand.Reader, key, raw[offset:end])
		require.NoError(t, err)
		parts = append(parts, base64.StdEncoding.EncodeToString(ciphertext))
	}
	return strings.Join(parts, "|")
}

func TestOriginalAssistantInfoRequiresPublishedGuestAndCurrentPermission(t *testing.T) {
	ctx := context.Background()
	service, admin, _, _, _ := serviceFixture(t)
	loginKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	service.Identity.privateKey = loginKey
	guest := testUser(t, "2", "sdk-guest")
	require.NoError(t, service.Store.CreateUser(ctx, guest))
	adminUser, revision, err := service.Store.User(ctx, admin.User.ID)
	require.NoError(t, err)
	adminUser.Admin = true
	require.NoError(t, service.Store.put(ctx, userKey(adminUser.ID), adminUser, revision))
	admin.User = adminUser
	binding, err := service.binding(ctx, admin, "10")
	require.NoError(t, err)
	binding.User = guest.ID
	binding.BindingID = "guest-fixture-binding"
	binding.ConnectorToken = "guest-fixture-connector"
	require.NoError(t, service.Store.ConfigureConnections(ctx, ConnectionConfig{Bindings: []Binding{binding}}))
	app, err := service.SaveAssistant(ctx, admin, AssistantApplication{
		Name: "Published synthetic", Type: 0, Domain: "https://app.example",
		DatasourceIDs: []ID{"10"}, PublicGuestUser: guest.ID, Enabled: true,
		Configuration: `{"oid":1,"public_list":[10],"welcome":"Hello","secret":"DO_NOT_LEAK"}`,
	})
	require.NoError(t, err)
	handler, err := NewHandler(service, "test")
	require.NoError(t, err)
	path := "/api/v1/system/assistant/info/" + string(app.ID) + "?virtual=untrusted-userFlag"
	claims := assistantSignPayload{AssistantID: app.ID,
		SignTime: time.Now().UTC().Format(time.RFC3339), Target: "https://app.example",
		UserAgent: "Original SDK browser", Timezone: "UTC"}
	header := originalSDKSignHeader(t, &service.Identity.privateKey.PublicKey, claims)
	call := func(method, route, origin, sign, userAgent, token string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, route, nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if sign != "" {
			r.Header.Set("SQLBOT-EMBEDDED-SIGN", sign)
		}
		if userAgent != "" {
			r.Header.Set("User-Agent", userAgent)
		}
		if token != "" {
			r.Header.Set("X-SQLBOT-ASSISTANT-TOKEN", "Assistant "+token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	preflight := call(http.MethodOptions, path, claims.Target, "", "", "")
	require.Equal(t, http.StatusNoContent, preflight.Code)
	require.Contains(t, preflight.Header().Get("Access-Control-Allow-Headers"), "SQLBOT-EMBEDDED-SIGN")
	w := call(http.MethodGet, path, claims.Target, header, claims.UserAgent, "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, claims.Target, w.Header().Get("Access-Control-Allow-Origin"))
	require.NotContains(t, w.Body.String(), "DO_NOT_LEAK")
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			Token         string `json:"token"`
			Configuration string `json:"configuration"`
			AppSecret     any    `json:"app_secret"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
	require.Zero(t, envelope.Code)
	require.NotEmpty(t, envelope.Data.Token)
	require.Nil(t, envelope.Data.AppSecret)
	require.JSONEq(t, `{"welcome":"Hello"}`, envelope.Data.Configuration)
	principal, err := service.Identity.VerifyEmbedded(ctx, envelope.Data.Token)
	require.NoError(t, err)
	require.Equal(t, guest.ID, principal.User.ID)
	require.True(t, validVisitorScope(principal.VisitorScope))
	ds := call(http.MethodGet, "/api/v1/system/assistant/ds", "", "", "", envelope.Data.Token)
	require.Equal(t, http.StatusOK, ds.Code, ds.Body.String())
	require.True(t, bytes.Contains(ds.Body.Bytes(), []byte(`"id":"10"`)))
	start := httptest.NewRequest(http.MethodPost, "/api/v1/chat/assistant/start",
		strings.NewReader(`{"datasource":"10","question":"","origin":2}`))
	start.Header.Set("Content-Type", "application/json")
	start.Header.Set("X-SQLBOT-ASSISTANT-TOKEN", "Assistant "+envelope.Data.Token)
	started := httptest.NewRecorder()
	handler.ServeHTTP(started, start)
	require.Equal(t, http.StatusOK, started.Code, started.Body.String())
	var startedChat struct {
		Data struct {
			ID ID `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(started.Body.Bytes(), &startedChat))
	require.True(t, startedChat.Data.ID.Valid())
	require.Equal(t, http.StatusOK, call(http.MethodGet,
		"/api/v1/chat/"+string(startedChat.Data.ID), "", "", "", envelope.Data.Token).Code)
	second := call(http.MethodGet, path, claims.Target, header, claims.UserAgent, "")
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	var secondEnvelope struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(second.Body.Bytes(), &secondEnvelope))
	require.NotEqual(t, envelope.Data.Token, secondEnvelope.Data.Token)
	require.NotEqual(t, http.StatusOK, call(http.MethodGet,
		"/api/v1/chat/"+string(startedChat.Data.ID), "", "", "", secondEnvelope.Data.Token).Code)
	require.Equal(t, http.StatusUnauthorized, call(http.MethodGet, path, claims.Target, "", claims.UserAgent, "").Code)
	require.Equal(t, http.StatusForbidden, call(http.MethodGet, path, "https://evil.example", header, claims.UserAgent, "").Code)
	require.Equal(t, http.StatusUnauthorized, call(http.MethodGet, path, claims.Target, header, "Different browser", "").Code)
	stale := claims
	stale.SignTime = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	require.Equal(t, http.StatusUnauthorized, call(http.MethodGet, path, claims.Target,
		originalSDKSignHeader(t, &service.Identity.privateKey.PublicKey, stale), claims.UserAgent, "").Code)
	unconfigured, err := service.SaveAssistant(ctx, admin, AssistantApplication{
		Name: "Not published", Type: 0, Domain: claims.Target,
		DatasourceIDs: []ID{"10"}, Enabled: true,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, call(http.MethodGet,
		"/api/v1/system/assistant/info/"+string(unconfigured.ID), claims.Target, header, claims.UserAgent, "").Code)
	guestBindingKey := bindingKey(Principal{User: guest, Workspace: admin.Workspace}, "10")
	guestBindingRevision, err := service.Store.get(ctx, guestBindingKey, &binding)
	require.NoError(t, err)
	require.NoError(t, service.Store.delete(ctx, guestBindingKey, guestBindingRevision))
	require.NotEqual(t, http.StatusOK, call(http.MethodGet,
		"/api/v1/system/assistant/ds", "", "", "", envelope.Data.Token).Code)
	require.Equal(t, http.StatusUnauthorized, call(http.MethodGet, path, claims.Target, header, claims.UserAgent, "").Code)
	require.NoError(t, service.Store.put(ctx, guestBindingKey, binding, 0))
	app.Enabled = false
	_, err = service.SaveAssistant(ctx, admin, app)
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, call(http.MethodGet, "/api/v1/system/assistant/ds", "", "", "", envelope.Data.Token).Code)
}
