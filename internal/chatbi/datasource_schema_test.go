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
	request := func(method, path string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for path, expected := range map[string]string{
		"/api/v1/datasource/getTables/10":       `"tableName":"sales"`,
		"/api/v1/datasource/getFields/10/sales": `"fieldName":"amount"`,
	} {
		w := request(http.MethodPost, path)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), expected) {
			t.Fatalf("visible schema response for %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	if w := request(http.MethodGet, "/api/v1/datasource/check/10"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"data":true`) {
		t.Fatal("bound datasource health did not use the live gateway")
	}
	if w := request(http.MethodPost, "/api/v1/datasource/getFields/10/hidden"); w.Code == http.StatusOK {
		t.Fatal("unknown table exposed a successful response")
	}
	if w := request(http.MethodPost, "/api/v1/datasource/getTables/11"); w.Code == http.StatusOK {
		t.Fatal("unbound datasource exposed metadata")
	}
	if gateway.schemaCalls != 4 {
		t.Fatalf("unexpected schema calls: %d", gateway.schemaCalls)
	}
	gateway.afterSchema = func() {
		gateway.afterSchema = nil
		if err := service.Store.delete(context.Background(), bindingKey(p, "10"), 1); err != nil {
			t.Fatal(err)
		}
	}
	if w := request(http.MethodPost, "/api/v1/datasource/getTables/10"); w.Code == http.StatusOK || strings.Contains(w.Body.String(), "sales") {
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

func TestDatasourcePreviewUsesCurrentSchemaAndControlledQuery(t *testing.T) {
	service, _, gateway, _, token := serviceFixture(t)
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	request := func(datasource, table string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/api/v1/datasource/previewData/"+datasource, strings.NewReader(`{"table":{"id":12,"table_name":"`+table+`"},"fields":[{"field_name":"ignored"}]}`))
		r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	w := request("10", "sales")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"amount":"123"`) || gateway.lastQuery != `SELECT "amount" FROM "public"."sales" LIMIT 100` {
		t.Fatalf("preview failed current controlled query: %d %s sql=%s", w.Code, w.Body.String(), gateway.lastQuery)
	}
	before := gateway.queries
	if w := request("10", "hidden"); w.Code == http.StatusOK || gateway.queries != before {
		t.Fatal("unknown table reached controlled query")
	}
	if w := request("11", "sales"); w.Code == http.StatusOK || gateway.queries != before {
		t.Fatal("unbound datasource reached controlled query")
	}
	gateway.audit = 0
	if w := request("10", "sales"); w.Code == http.StatusOK || strings.Contains(w.Body.String(), `"amount":"123"`) {
		t.Fatal("un-audited preview released rows")
	}
}

func TestDatasourcePreviewRejectsRevocationAfterQuery(t *testing.T) {
	for _, revoke := range []string{"binding", "token"} {
		t.Run(revoke, func(t *testing.T) {
			service, p, gateway, _, token := serviceFixture(t)
			handler, err := NewHandler(service, "test")
			if err != nil {
				t.Fatal(err)
			}
			gateway.afterQuery = func() {
				if revoke == "binding" {
					if err := service.Store.delete(context.Background(), bindingKey(p, "10"), 1); err != nil {
						t.Fatal(err)
					}
				} else if err := service.Identity.Logout(context.Background(), token); err != nil {
					t.Fatal(err)
				}
			}
			r := httptest.NewRequest(http.MethodPost, "/api/v1/datasource/previewData/10", strings.NewReader(`{"table":{"id":12,"table_name":"sales"}}`))
			r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code == http.StatusOK || strings.Contains(w.Body.String(), `"amount":"123"`) {
				t.Fatalf("revoked %s released preview rows: %d %s", revoke, w.Code, w.Body.String())
			}
		})
	}
}
