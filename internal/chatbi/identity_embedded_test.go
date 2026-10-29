package chatbi

import (
	"bytes"
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

func signedEmbeddedFixture(t *testing.T, secret string, claims map[string]any) string {
	return signedEmbeddedFixtureHeader(t, secret, claims, `{"alg":"HS256","typ":"JWT"}`)
}

func signedEmbeddedFixtureHeader(t *testing.T, secret string, claims map[string]any, header string) string {
	t.Helper()
	body, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	part := base64.RawURLEncoding.EncodeToString([]byte(header)) + "." + base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(part))
	return part + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func TestPageEmbedTokenUsesUserScopeAndRevokesOnAppChange(t *testing.T) {
	ctx := context.Background()
	service, principal, _, _, ordinaryToken := serviceFixture(t)
	adminState := func(admin bool) {
		user, revision, err := service.Store.User(ctx, principal.User.ID)
		if err != nil {
			t.Fatal(err)
		}
		user.Admin = admin
		if err := service.Store.put(ctx, userKey(user.ID), user, revision); err != nil {
			t.Fatal(err)
		}
		principal.User = user
	}
	adminState(true)
	app, err := service.SaveAssistant(ctx, principal, AssistantApplication{Name: "Embedded page", Type: 4, Domain: "https://app.example", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	adminState(false)
	user, revision, err := service.Store.User(ctx, principal.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	user.Memberships = append(user.Memberships, Membership{Workspace: "2", Name: "Other workspace"})
	if err := service.Store.put(ctx, userKey(user.ID), user, revision); err != nil {
		t.Fatal(err)
	}
	principal.User = user
	claims := map[string]any{"account": principal.User.Account, "embeddedId": app.ID, "appId": app.AppID, "exp": time.Now().Add(time.Hour).Unix()}
	jwt := signedEmbeddedFixture(t, app.AppSecret, claims)
	embedded, err := service.Identity.VerifyEmbedded(ctx, jwt)
	if err != nil || embedded.User.ID != principal.User.ID || embedded.EmbeddedApp != app.ID || embedded.EmbeddedRevision != app.Revision {
		t.Fatalf("scoped embedded identity: %+v, %v", embedded.EmbeddedApp, err)
	}
	router, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, credential string, ordinary bool) int {
		r := httptest.NewRequest(method, path, nil)
		if ordinary {
			r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+credential)
		} else {
			r.Header.Set("X-SQLBOT-ASSISTANT-TOKEN", "Embedded "+credential)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w.Code
	}
	if status := call(http.MethodGet, "/api/v1/user/info", jwt, false); status != 200 {
		t.Fatalf("embedded user info: %d", status)
	}
	infoRequest := httptest.NewRequest(http.MethodGet, "/api/v1/user/info", nil)
	infoRequest.Header.Set("X-SQLBOT-ASSISTANT-TOKEN", "Embedded "+jwt)
	infoResponse := httptest.NewRecorder()
	router.ServeHTTP(infoResponse, infoRequest)
	if bytes.Contains(infoResponse.Body.Bytes(), []byte(`"2"`)) {
		t.Fatal("embedded user info disclosed another workspace")
	}
	if status := call(http.MethodGet, "/api/v1/datasource/list", jwt, false); status != 200 {
		t.Fatalf("embedded datasource list: %d", status)
	}
	start := func(path, credential string, ordinary bool, origin int) int {
		body, _ := json.Marshal(map[string]any{"datasource": "10", "origin": origin})
		r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		if ordinary {
			r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+credential)
		} else {
			r.Header.Set("X-SQLBOT-ASSISTANT-TOKEN", "Embedded "+credential)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w.Code
	}
	if status := start("/api/v1/chat/assistant/start", jwt, false, 2); status != 200 {
		t.Fatalf("embedded chat start: %d", status)
	}
	if status := start("/api/v1/chat/start", jwt, false, 0); status != 401 {
		t.Fatalf("embedded ordinary start: %d", status)
	}
	if status := start("/api/v1/chat/assistant/start", ordinaryToken, true, 2); status != 401 {
		t.Fatalf("ordinary token entered embed start: %d", status)
	}
	if status := call(http.MethodGet, "/api/v1/system/embedded/1/10", jwt, false); status != 401 {
		t.Fatalf("embedded management access: %d", status)
	}
	if err := service.semanticAdmin(ctx, embedded); err == nil {
		t.Fatal("embedded identity reached administrator scope")
	}
	if status := call(http.MethodPost, "/api/v1/datasource/add", jwt, false); status != 401 {
		t.Fatalf("embedded datasource mutation: %d", status)
	}
	if status := call(http.MethodGet, "/api/v1/user/info", ordinaryToken, true); status != 200 {
		t.Fatalf("ordinary token regression: %d", status)
	}
	dual := httptest.NewRequest(http.MethodGet, "/api/v1/user/info", nil)
	dual.Header.Set("X-SQLBOT-TOKEN", "Bearer "+ordinaryToken)
	dual.Header.Set("X-SQLBOT-ASSISTANT-TOKEN", "Embedded "+jwt)
	dualResponse := httptest.NewRecorder()
	router.ServeHTTP(dualResponse, dual)
	if dualResponse.Code != 401 {
		t.Fatalf("ambiguous dual-token request returned %d", dualResponse.Code)
	}
	dualAuthorization := httptest.NewRequest(http.MethodGet, "/api/v1/user/info", nil)
	dualAuthorization.Header.Set("Authorization", "Bearer "+ordinaryToken)
	dualAuthorization.Header.Set("X-SQLBOT-ASSISTANT-TOKEN", "Embedded "+jwt)
	dualResponse = httptest.NewRecorder()
	router.ServeHTTP(dualResponse, dualAuthorization)
	if dualResponse.Code != 401 {
		t.Fatalf("authorization plus embed token returned %d", dualResponse.Code)
	}
	for _, originCase := range []struct {
		origin string
		want   int
	}{{"https://app.example", 200}, {"https://app.example.evil", 401}, {"http://app.example", 401}} {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/user/info", nil)
		r.Header.Set("X-SQLBOT-ASSISTANT-TOKEN", "Embedded "+jwt)
		r.Header.Set("X-SQLBOT-HOST-ORIGIN", originCase.origin)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != originCase.want {
			t.Fatalf("asserted origin %q returned %d", originCase.origin, w.Code)
		}
	}
	appOnly := map[string]any{"account": principal.User.Account, "appId": app.AppID, "exp": time.Now().Add(time.Hour).Unix()}
	if _, err := service.Identity.VerifyEmbedded(ctx, signedEmbeddedFixture(t, app.AppSecret, appOnly)); err != nil {
		t.Fatalf("app-id lookup: %v", err)
	}
	// The upstream public integration demo signs appId+account with jose, sets
	// iat, and omits both typ and exp. Keep it usable within a bounded age.
	publicDemo := map[string]any{"account": principal.User.Account, "appId": app.AppID, "iat": time.Now().Unix()}
	if _, err := service.Identity.VerifyEmbedded(ctx, signedEmbeddedFixtureHeader(t, app.AppSecret, publicDemo, `{"alg":"HS256"}`)); err != nil {
		t.Fatalf("published app-id integration token: %v", err)
	}
	publicDemo["iat"] = time.Now().Add(-9 * 24 * time.Hour).Unix()
	if _, err := service.Identity.VerifyEmbedded(ctx, signedEmbeddedFixtureHeader(t, app.AppSecret, publicDemo, `{"alg":"HS256"}`)); err == nil {
		t.Fatal("stale no-expiry token accepted")
	}
	badExpiry := map[string]any{"account": principal.User.Account, "embeddedId": app.ID, "exp": time.Now().Add(-time.Minute).Unix()}
	if _, err := service.Identity.VerifyEmbedded(ctx, signedEmbeddedFixture(t, app.AppSecret, badExpiry)); err == nil {
		t.Fatal("expired token accepted")
	}
	parts := strings.Split(jwt, ".")
	parts[0] = base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	if _, err := service.Identity.VerifyEmbedded(ctx, strings.Join(parts, ".")); err == nil {
		t.Fatal("unsigned algorithm accepted")
	}
	other := testUser(t, "2", "other")
	other.Workspace = "2"
	other.Memberships = []Membership{{Workspace: "2", Name: "Two"}}
	if err := service.Store.CreateUser(ctx, other); err != nil {
		t.Fatal(err)
	}
	foreign := map[string]any{"account": other.Account, "embeddedId": app.ID, "exp": time.Now().Add(time.Hour).Unix()}
	if _, err := service.Identity.VerifyEmbedded(ctx, signedEmbeddedFixture(t, app.AppSecret, foreign)); err == nil {
		t.Fatal("cross-workspace token accepted")
	}
	adminState(true)
	if _, err := service.Identity.VerifyEmbedded(ctx, jwt); err == nil {
		t.Fatal("admin via embedded credential accepted")
	}
	rotated, err := service.RotateAssistantSecret(ctx, principal, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	adminState(false)
	if _, err := service.Identity.VerifyEmbedded(ctx, jwt); err == nil {
		t.Fatal("old credential survived rotation")
	}
	if err := service.live(ctx, embedded); err == nil {
		t.Fatal("in-flight app identity survived rotation")
	}
	newToken := signedEmbeddedFixture(t, rotated.AppSecret, claims)
	if _, err := service.Identity.VerifyEmbedded(ctx, newToken); err != nil {
		t.Fatalf("rotated credential rejected: %v", err)
	}
	adminState(true)
	rotated.Enabled = false
	if _, err := service.SaveAssistant(ctx, principal, rotated); err != nil {
		t.Fatal(err)
	}
	adminState(false)
	if status := call(http.MethodGet, "/api/v1/user/info", newToken, false); status != 401 {
		t.Fatalf("disabled app token returned %d", status)
	}
}

func TestPageEmbedSeparatesApplicationsAndExplicitDatasourceScope(t *testing.T) {
	ctx := context.Background()
	service, principal, _, _, ordinaryToken := serviceFixture(t)
	binding, err := service.binding(ctx, principal, "10")
	if err != nil {
		t.Fatal(err)
	}
	binding.Datasource = "11"
	if err := service.Store.put(ctx, bindingKey(principal, "11"), binding, 0); err != nil {
		t.Fatal(err)
	}
	user, revision, err := service.Store.User(ctx, principal.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	user.Admin = true
	if err := service.Store.put(ctx, userKey(user.ID), user, revision); err != nil {
		t.Fatal(err)
	}
	principal.User = user
	first, err := service.SaveAssistant(ctx, principal, AssistantApplication{Name: "Scoped page", Type: 4, Domain: "https://app.example", DatasourceIDs: []ID{"10"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.SaveAssistant(ctx, principal, AssistantApplication{Name: "Other page", Type: 4, Domain: "https://app.example", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	user, revision, err = service.Store.User(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	user.Admin = false
	if err := service.Store.put(ctx, userKey(user.ID), user, revision); err != nil {
		t.Fatal(err)
	}
	firstJWT := signedEmbeddedFixture(t, first.AppSecret, map[string]any{"account": user.Account, "appId": first.AppID, "exp": time.Now().Add(time.Hour).Unix()})
	secondJWT := signedEmbeddedFixture(t, second.AppSecret, map[string]any{"account": user.Account, "appId": second.AppID, "exp": time.Now().Add(time.Hour).Unix()})
	firstPrincipal, err := service.Identity.VerifyEmbedded(ctx, firstJWT)
	if err != nil {
		t.Fatal(err)
	}
	secondPrincipal, err := service.Identity.VerifyEmbedded(ctx, secondJWT)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Start(ctx, firstPrincipal, "11", "", 2); err != ErrNotFound {
		t.Fatalf("embedded app used unlisted datasource: %v", err)
	}
	chat, err := service.Start(ctx, firstPrincipal, "10", "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.chat(ctx, secondPrincipal, chat.ID); err != ErrUnavailable {
		t.Fatalf("other embedded app read conversation: %v", err)
	}
	ordinary, err := service.Identity.Verify(ctx, ordinaryToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.chat(ctx, ordinary, chat.ID); err != ErrUnavailable {
		t.Fatalf("ordinary token read embedded conversation: %v", err)
	}
	otherChats, err := service.Chats(ctx, secondPrincipal)
	if err != nil || len(otherChats) != 0 {
		t.Fatalf("other app listed embedded conversation: %v, %v", otherChats, err)
	}
	router, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/datasource/list", nil)
	r.Header.Set("X-SQLBOT-ASSISTANT-TOKEN", "Embedded "+firstJWT)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 200 || bytes.Contains(w.Body.Bytes(), []byte(`"id":"11"`)) || !bytes.Contains(w.Body.Bytes(), []byte(`"id":"10"`)) {
		t.Fatalf("embedded datasource list escaped app scope: %d %s", w.Code, w.Body.String())
	}
	visitorA := strings.Repeat("a", 32)
	visitorB := strings.Repeat("b", 32)
	visitorToken := func(visitor string) string {
		return signedEmbeddedFixture(t, first.AppSecret, map[string]any{
			"account": user.Account, "appId": first.AppID, "visitor": visitor,
			"exp": time.Now().Add(time.Hour).Unix(),
		})
	}
	firstVisitor, err := service.Identity.VerifyEmbedded(ctx, visitorToken(visitorA))
	if err != nil || firstVisitor.VisitorScope != visitorA {
		t.Fatalf("first visitor identity: %v", err)
	}
	secondVisitor, err := service.Identity.VerifyEmbedded(ctx, visitorToken(visitorB))
	if err != nil || secondVisitor.VisitorScope != visitorB {
		t.Fatalf("second visitor identity: %v", err)
	}
	if _, _, err := service.chat(ctx, firstVisitor, chat.ID); err != ErrUnavailable {
		t.Fatalf("visitor could read unscoped chat: %v", err)
	}
	visitorChat, err := service.Start(ctx, firstVisitor, "10", "", 2)
	if err != nil {
		t.Fatalf("first visitor start: %v", err)
	}
	if _, _, err := service.chat(ctx, secondVisitor, visitorChat.ID); err != ErrUnavailable {
		t.Fatalf("second visitor read first visitor chat: %v", err)
	}
	if _, _, err := service.chat(ctx, firstPrincipal, visitorChat.ID); err != ErrUnavailable {
		t.Fatalf("unscoped app token read visitor chat: %v", err)
	}
	firstChats, err := service.Chats(ctx, firstVisitor)
	if err != nil || len(firstChats) != 1 || firstChats[0].ID != visitorChat.ID {
		t.Fatalf("first visitor chat list: %v, %v", firstChats, err)
	}
	secondChats, err := service.Chats(ctx, secondVisitor)
	if err != nil || len(secondChats) != 0 {
		t.Fatalf("second visitor chat list: %v, %v", secondChats, err)
	}
	visitorChat, chatRevision, err := service.chat(ctx, firstVisitor, visitorChat.ID)
	if err != nil {
		t.Fatal(err)
	}
	visitorChat.Records = append(visitorChat.Records, Record{
		ID: "123", Chat: visitorChat.ID, Datasource: "10", Question: "private visitor question", Created: time.Now(),
	})
	if err := service.Store.put(ctx, chatKey(firstVisitor, visitorChat.ID), visitorChat, chatRevision); err != nil {
		t.Fatal(err)
	}
	firstQuestions, err := service.RecentQuestions(ctx, firstVisitor, "10")
	if err != nil || len(firstQuestions) != 1 || firstQuestions[0] != "private visitor question" {
		t.Fatalf("first visitor suggestions: %v, %v", firstQuestions, err)
	}
	secondQuestions, err := service.RecentQuestions(ctx, secondVisitor, "10")
	if err != nil || len(secondQuestions) != 0 {
		t.Fatalf("second visitor suggestions: %v, %v", secondQuestions, err)
	}
	if _, err := service.Identity.VerifyEmbedded(ctx, visitorToken("public-visitor")); err == nil {
		t.Fatal("invalid visitor scope accepted")
	}
}
