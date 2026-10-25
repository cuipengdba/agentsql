package chatbi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDatasourceSchemaRoutesUseCurrentBindingAndFailClosed(t *testing.T) {
	service, p, gateway, _, token := serviceFixture(t)
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	request := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, path, nil)
		r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for path, expected := range map[string]string{
		"/api/v1/datasource/getTables/10":       `"tableName":"sales"`,
		"/api/v1/datasource/getFields/10/sales": `"fieldName":"amount"`,
	} {
		w := request(path)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), expected) {
			t.Fatalf("visible schema response for %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	if w := request("/api/v1/datasource/getFields/10/hidden"); w.Code == http.StatusOK {
		t.Fatal("unknown table exposed a successful response")
	}
	if w := request("/api/v1/datasource/getTables/11"); w.Code == http.StatusOK {
		t.Fatal("unbound datasource exposed metadata")
	}
	if gateway.schemaCalls != 3 {
		t.Fatalf("unexpected schema calls: %d", gateway.schemaCalls)
	}
	gateway.afterSchema = func() {
		gateway.afterSchema = nil
		if err := service.Store.delete(context.Background(), bindingKey(p, "10"), 1); err != nil {
			t.Fatal(err)
		}
	}
	if w := request("/api/v1/datasource/getTables/10"); w.Code == http.StatusOK || strings.Contains(w.Body.String(), "sales") {
		t.Fatal("revoked binding released metadata after a remote schema read")
	}
}

func TestDatasourceSchemaRejectsLogoutDuringRemoteRead(t *testing.T) {
	service, _, gateway, _, token := serviceFixture(t)
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	gateway.afterSchema = func() {
		gateway.afterSchema = nil
		if err := service.Identity.Logout(context.Background(), token); err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/datasource/getFields/10/sales", nil)
	r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized || strings.Contains(w.Body.String(), "amount") {
		t.Fatalf("logged-out token released fields: %d %s", w.Code, w.Body.String())
	}
}
