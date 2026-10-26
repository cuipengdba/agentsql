package chatbi

import (
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
	for _, path := range []string{"/api/v1/system/license/version", "/api/v1/system/parameter/chat"} {
		if status, _ := request(path, ""); status != http.StatusUnauthorized {
			t.Fatalf("anonymous browser bootstrap %s returned %d", path, status)
		}
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
}
