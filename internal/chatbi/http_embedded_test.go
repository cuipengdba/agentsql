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
