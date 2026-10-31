package chatbi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestSQLBotBrowserBootstrapContract(t *testing.T) {
	service, _, _, _, token := serviceFixture(t)
	handler, err := NewHandler(service, "v4.2.0")
	if err != nil {
		t.Fatal(err)
	}
	request := func(path, bearer string) (int, map[string]json.RawMessage) {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if bearer != "" {
			r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		var body map[string]json.RawMessage
		if w.Code == http.StatusOK {
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
		}
		return w.Code, body
	}
	status, body := request("/api/v1/system/license", "")
	if status != http.StatusOK || string(body["data"]) != `{"license":null,"message":"No license record found.","status":"no_record"}` {
		t.Fatalf("public SQLBot license bootstrap: %d %s", status, body["data"])
	}
	status, body = request("/api/v1/system/parameter/login", "")
	var loginParameters []sqlbotChatParameter
	if status != http.StatusOK || json.Unmarshal(body["data"], &loginParameters) != nil || len(loginParameters) != 2 {
		t.Fatalf("public SQLBot login settings: %d %s", status, body["data"])
	}
	loginSettings := map[string]string{}
	for _, parameter := range loginParameters {
		loginSettings[parameter.Key] = parameter.Value
	}
	if loginSettings["login.initial_pwd_hidden"] != "false" || loginSettings["login.initial_pwd_disabled"] != "false" {
		t.Fatalf("unsafe SQLBot login settings: %v", loginSettings)
	}
	for _, path := range []string{"/api/v1/system/license/version", "/api/v1/system/parameter/chat", "/api/v1/system/aimodel/default"} {
		if status, _ := request(path, ""); status != http.StatusUnauthorized {
			t.Fatalf("anonymous browser bootstrap %s returned %d", path, status)
		}
	}
	if status, _ := request("/api/v1/user/defaultPwd", ""); status != http.StatusUnauthorized {
		t.Fatalf("anonymous default password probe returned %d", status)
	}
	if status, _ := request("/api/v1/user/defaultPwd", token); status != http.StatusUnauthorized {
		t.Fatalf("member default password probe returned %d", status)
	}
	_, adminHandler, adminToken, _ := adminUsersFixture(t)
	adminRequest := httptest.NewRequest(http.MethodGet, "/api/v1/user/defaultPwd", nil)
	adminRequest.Header.Set("X-SQLBOT-TOKEN", "Bearer "+adminToken)
	adminResponse := httptest.NewRecorder()
	adminHandler.ServeHTTP(adminResponse, adminRequest)
	if adminResponse.Code != http.StatusOK || !bytes.Contains(adminResponse.Body.Bytes(), []byte(`"data":""`)) {
		t.Fatalf("shared default password must be absent: %d %s", adminResponse.Code, adminResponse.Body.String())
	}
	status, body = request("/api/v1/system/license/version", token)
	if status != http.StatusOK || string(body["data"]) != `"1.10.2"` {
		t.Fatalf("SQLBot protocol version: %d %s", status, body["data"])
	}
	status, body = request("/api/v1/system/parameter/chat", token)
	var parameters []sqlbotChatParameter
	if status != http.StatusOK || json.Unmarshal(body["data"], &parameters) != nil || len(parameters) != 6 {
		t.Fatalf("SQLBot chat bootstrap: %d %s", status, body["data"])
	}
	want := map[string]string{
		"chat.hide_thinking_block": "false", "chat.expand_thinking_block": "false",
		"chat.limit_rows": "true", "chat.context_record_count": "3",
		"chat.show_sql": "true", "chat.show_log": "true",
	}
	got := map[string]string{}
	for _, parameter := range parameters {
		if parameter.ID == "" || parameter.Type != "str" || parameter.SortNo != 0 {
			t.Fatalf("incomplete parameter shape: %+v", parameter)
		}
		got[parameter.Key] = parameter.Value
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SQLBot chat parameters = %v, want %v", got, want)
	}
	status, body = request("/api/v1/system/aimodel/default", token)
	if status != http.StatusOK || string(body["data"]) != "null" {
		t.Fatalf("SQLBot default model probe: %d %s", status, body["data"])
	}
	key := objectKey{"model", "1", "workspace", "default"}
	var configured ModelConfig
	revision, err := service.Store.get(context.Background(), key, &configured)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Store.delete(context.Background(), key, revision); err != nil {
		t.Fatal(err)
	}
	if status, _ := request("/api/v1/system/aimodel/default", token); status != http.StatusNotFound {
		t.Fatalf("missing default model returned %d", status)
	}
}

func TestSQLBotBrowserInitialRecordIsResponseOnly(t *testing.T) {
	service, principal, _, _, token := serviceFixture(t)
	handler, err := NewHandler(service, "v4.2.0")
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path string, body []byte) Chat {
		t.Helper()
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		var response struct {
			Data Chat `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response.Data
	}
	created := request(http.MethodPost, "/api/v1/chat/start", []byte(`{"datasource":"10"}`))
	if len(created.Records) != 1 || !created.Records[0].FirstChat || !created.Records[0].Finish || created.Records[0].SQL != "" || created.Records[0].Data != nil {
		t.Fatalf("initial browser response record = %+v", created.Records)
	}
	stored, _, err := service.chat(context.Background(), principal, created.ID)
	if err != nil || len(stored.Records) != 0 {
		t.Fatalf("browser placeholder was persisted: %v %+v", err, stored.Records)
	}
	listRequest := httptest.NewRequest(http.MethodGet, "/api/v1/chat/list", nil)
	listRequest.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
	listResponse := httptest.NewRecorder()
	handler.ServeHTTP(listResponse, listRequest)
	if listResponse.Code != http.StatusOK || !bytes.Contains(listResponse.Body.Bytes(), []byte(`"records":[]`)) {
		t.Fatalf("browser chat list requires an empty records array: %d %s", listResponse.Code, listResponse.Body.String())
	}
	loaded := request(http.MethodGet, "/api/v1/chat/"+string(created.ID)+"/with_data", nil)
	if len(loaded.Records) != 1 || !loaded.Records[0].FirstChat || loaded.Records[0].ID != created.Records[0].ID {
		t.Fatalf("historical browser response record = %+v", loaded.Records)
	}
}
