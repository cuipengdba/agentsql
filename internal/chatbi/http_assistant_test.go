package chatbi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLegacyAssistantManagementContract(t *testing.T) {
	ctx := context.Background()
	service, principal, _, _, token := serviceFixture(t)
	user, revision, err := service.Store.User(ctx, principal.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	user.Admin = true
	if err := service.Store.put(ctx, userKey(user.ID), user, revision); err != nil {
		t.Fatal(err)
	}
	router, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body string) (int, map[string]json.RawMessage) {
		request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		request.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		var envelope map[string]json.RawMessage
		_ = json.Unmarshal(response.Body.Bytes(), &envelope)
		return response.Code, envelope
	}
	path := "/api/v1/system/assistant"
	if status, _ := call(http.MethodGet, "/api/v1/datasource/ws/2", ""); status != 404 {
		t.Fatalf("foreign workspace datasource list: %d", status)
	}
	if status, body := call(http.MethodGet, "/api/v1/datasource/ws/1", ""); status != 200 || bytes.Contains(body["data"], []byte("fixture-connector")) || bytes.Contains(body["data"], []byte("fixture-agent")) {
		t.Fatalf("workspace datasource list: %d, %s", status, body["data"])
	}
	if status, body := call(http.MethodGet, "/api/v1/system/aimodel/list/by_ws", ""); status != 200 || bytes.Contains(body["data"], []byte("api_key")) || bytes.Contains(body["data"], []byte("api_domain")) {
		t.Fatalf("workspace model list: %d, %s", status, body["data"])
	}
	if status, _ := call(http.MethodPost, path, `{"id":null,"name":"bad","type":0,"domain":"https://app.example","oid":2}`); status != 422 {
		t.Fatalf("foreign workspace create: %d", status)
	}
	if status, _ := call(http.MethodPost, path, `{"name":"bad","type":0,"domain":"https://app.example","configuration":"{\"oid\":\"2\",\"public_list\":[\"10\"]}"}`); status != 422 {
		t.Fatalf("foreign configuration scope: %d", status)
	}
	status, envelope := call(http.MethodPost, path, `{"id":null,"name":"Finance","type":1,"domain":"https://app.example","configuration":"{\"oid\":\"1\",\"public_list\":[\"10\"],\"private_list\":[]}","enable_custom_model":true,"custom_model":"20"}`)
	if status != 200 {
		t.Fatalf("create: %d, %s", status, envelope)
	}
	var app struct {
		ID   json.Number `json:"id"`
		Name string      `json:"name"`
	}
	if err := json.Unmarshal(envelope["data"], &app); err != nil || app.ID == "" || app.Name != "Finance" {
		t.Fatalf("created model: %+v, %v", app, err)
	}
	stored, err := service.Assistant(ctx, principal, ID(app.ID.String()))
	if err != nil || len(stored.DatasourceIDs) != 1 || stored.DatasourceIDs[0] != "10" || !stored.UseCustomModel || stored.CustomModel == nil || *stored.CustomModel != "20" {
		t.Fatalf("original datasource scope not enforced: %+v, %v", stored.DatasourceIDs, err)
	}
	status, envelope = call(http.MethodGet, path+"/advanced_application", "")
	if status != 200 {
		t.Fatalf("advanced list: %d", status)
	}
	var items []json.RawMessage
	if json.Unmarshal(envelope["data"], &items) != nil || len(items) != 1 {
		t.Fatalf("advanced list body: %s", envelope["data"])
	}
	status, _ = call(http.MethodPut, path, `{"id":`+app.ID.String()+`,"name":"Finance v2","type":1,"domain":"https://app.example","configuration":"{}"}`)
	if status != 200 {
		t.Fatalf("update: %d", status)
	}
	status, _ = call(http.MethodDelete, path+"/"+app.ID.String(), "")
	if status != 200 {
		t.Fatalf("delete: %d", status)
	}
	status, envelope = call(http.MethodGet, path, "")
	if status != 200 || json.Unmarshal(envelope["data"], &items) != nil || len(items) != 0 {
		t.Fatalf("list after delete: %d, %s", status, envelope["data"])
	}
	status, envelope = call(http.MethodPost, path, `{"name":"Basic","type":0,"domain":"http://localhost:3000","configuration":"{\"oid\":\"1\",\"public_list\":[\"10\"],\"private_list\":[]}","enable_custom_model":false,"custom_model":""}`)
	if status != 200 {
		t.Fatalf("original browser empty custom model: %d, %s", status, envelope)
	}
}
