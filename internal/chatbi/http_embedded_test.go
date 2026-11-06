package chatbi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOriginalPageEmbedManagementIsScopedAndAudited(t *testing.T) {
	ctx := context.Background()
	service, principal, _, _, token := serviceFixture(t)
	router, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body string) (int, json.RawMessage) {
		request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		request.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		var envelope struct {
			Data json.RawMessage `json:"data"`
		}
		_ = json.Unmarshal(response.Body.Bytes(), &envelope)
		return response.Code, envelope.Data
	}
	base := "/api/v1/system/embedded"
	if status, _ := call(http.MethodPost, base, `{"name":"Page","type":4,"domain":"https://app.example"}`); status != 401 {
		t.Fatalf("non-admin create returned %d", status)
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
	if status, _ := call(http.MethodPost, base, `{"name":"Bad","type":4,"domain":"http://outside.example"}`); status != 422 {
		t.Fatalf("insecure domain returned %d", status)
	}
	status, raw := call(http.MethodPost, base, `{"id":null,"name":"Page","type":4,"domain":"https://app.example","app_id":"forged","app_secret":"forged"}`)
	if status != 200 {
		t.Fatalf("create returned %d", status)
	}
	var created struct {
		ID        ID     `json:"id"`
		AppID     string `json:"app_id"`
		AppSecret string `json:"app_secret"`
	}
	if json.Unmarshal(raw, &created) != nil || !created.ID.Valid() || len(created.AppID) < 20 || len(created.AppSecret) < 40 || created.AppSecret == "forged" {
		t.Fatal("server did not generate protected page embedding credentials")
	}
	if status, _ := call(http.MethodGet, base+"/0/10", ""); status != 422 {
		t.Fatalf("invalid pagination returned %d", status)
	}
	status, raw = call(http.MethodGet, base+"/1/10?keyword=page", "")
	var listed struct {
		Items []json.RawMessage `json:"items"`
		Total int               `json:"total"`
	}
	if status != 200 || json.Unmarshal(raw, &listed) != nil || listed.Total != 1 || len(listed.Items) != 1 {
		t.Fatalf("list returned %d with %d items", status, listed.Total)
	}
	status, raw = call(http.MethodPatch, base+"/secret/"+string(created.ID), "")
	var rotated struct {
		AppID     string `json:"app_id"`
		AppSecret string `json:"app_secret"`
	}
	if status != 200 || json.Unmarshal(raw, &rotated) != nil || rotated.AppID != created.AppID || rotated.AppSecret == created.AppSecret || len(rotated.AppSecret) < 40 {
		t.Fatal("secret rotation failed")
	}
	status, raw = call(http.MethodPut, base, `{"id":`+string(created.ID)+`,"name":"Page 2","type":4,"domain":"https://other.example","app_secret":"forged"}`)
	var updated struct {
		AppSecret string `json:"app_secret"`
		Domain    string `json:"domain"`
	}
	if status != 200 || json.Unmarshal(raw, &updated) != nil || updated.AppSecret != rotated.AppSecret || updated.Domain != "https://other.example" {
		t.Fatal("update changed secret or failed to save origin")
	}
	if status, _ := call(http.MethodGet, base+"/"+string(created.ID), ""); status != 200 {
		t.Fatalf("read returned %d", status)
	}
	if status, _ := call(http.MethodDelete, base, `["`+string(created.ID)+`","999999999"]`); status != 404 {
		t.Fatalf("mixed batch returned %d", status)
	}
	if status, _ := call(http.MethodGet, base+"/"+string(created.ID), ""); status != 200 {
		t.Fatalf("failed batch removed item: %d", status)
	}
	if status, _ := call(http.MethodDelete, base, `["`+string(created.ID)+`"]`); status != 200 {
		t.Fatalf("delete returned %d", status)
	}
	if status, _ := call(http.MethodGet, base+"/"+string(created.ID), ""); status != 404 {
		t.Fatalf("deleted item returned %d", status)
	}
	logs, err := service.Store.list(ctx, "management-audit", "system", "system")
	if err != nil || len(logs) != 4 {
		t.Fatalf("expected audited create, rotate, update, delete; got %d: %v", len(logs), err)
	}
}

func TestPublicEmbedHandshakeChecksOriginAndNeverRevealsSecret(t *testing.T) {
	ctx := context.Background()
	service, principal, _, _, _ := serviceFixture(t)
	user, revision, err := service.Store.User(ctx, principal.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	user.Admin = true
	if err := service.Store.put(ctx, userKey(user.ID), user, revision); err != nil {
		t.Fatal(err)
	}
	principal.User = user
	app, err := service.SaveAssistant(ctx, principal, AssistantApplication{Name: "Page", Type: 4, Domain: "https://app.example", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, origin, appID string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1/system/assistant/app/"+appID, nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	if got := call(http.MethodGet, "https://app.example.evil", app.AppID); got.Code != 403 {
		t.Fatalf("forged subdomain returned %d", got.Code)
	}
	if got := call(http.MethodGet, "", app.AppID); got.Code != 403 {
		t.Fatalf("missing origin returned %d", got.Code)
	}
	if got := call(http.MethodGet, "https://app.example", "unknown-app-id"); got.Code != 404 {
		t.Fatalf("unknown app returned %d", got.Code)
	}
	got := call(http.MethodGet, "https://app.example", app.AppID)
	if got.Code != 200 || bytes.Contains(got.Body.Bytes(), []byte(app.AppSecret)) || got.Header().Get("Access-Control-Allow-Origin") != "https://app.example" {
		t.Fatal("public handshake failed or disclosed signing material")
	}
	preflight := call(http.MethodOptions, "https://app.example", app.AppID)
	if preflight.Code != 204 || preflight.Header().Get("Access-Control-Allow-Origin") != "https://app.example" {
		t.Fatal("SDK preflight rejected")
	}
	validate := func(method, origin, referer, id string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1/system/assistant/validate/"+id, nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if referer != "" {
			r.Header.Set("Referer", referer)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	for _, origin := range []string{"", "https://app.example.evil", "http://app.example"} {
		if got := validate(http.MethodGet, origin, "", string(app.ID)); got.Code != 403 {
			t.Fatalf("invalid validation origin %q returned %d", origin, got.Code)
		}
	}
	if got := validate(http.MethodGet, "https://app.example", "", "999999999"); got.Code != 404 {
		t.Fatalf("unknown validation app returned %d", got.Code)
	}
	validated := validate(http.MethodGet, "https://app.example", "", string(app.ID))
	if validated.Code != 200 || validated.Header().Get("Access-Control-Allow-Origin") != "https://app.example" ||
		validated.Header().Get("Cache-Control") != "no-store" ||
		bytes.Contains(validated.Body.Bytes(), []byte(app.AppSecret)) {
		t.Fatal("public validation failed or disclosed signing material")
	}
	var result struct {
		Valid  bool   `json:"valid"`
		Origin string `json:"origin"`
	}
	if json.Unmarshal(validated.Body.Bytes(), &result) != nil || !result.Valid || result.Origin != "https://app.example" {
		t.Fatal("public validation response is not the original SDK shape")
	}
	if got := validate(http.MethodGet, "", "https://app.example/embed/page", string(app.ID)); got.Code != 200 {
		t.Fatalf("allowed Referer returned %d", got.Code)
	}
	if got := validate(http.MethodOptions, "https://app.example", "", string(app.ID)); got.Code != 204 {
		t.Fatalf("validation preflight returned %d", got.Code)
	}
	app.Enabled = false
	if _, err := service.SaveAssistant(ctx, principal, app); err != nil {
		t.Fatal(err)
	}
	if got := validate(http.MethodGet, "https://app.example", "", string(app.ID)); got.Code != 404 {
		t.Fatalf("disabled validation app returned %d", got.Code)
	}
}
