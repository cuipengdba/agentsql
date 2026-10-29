package chatbi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestOriginalSQLBotBrowserDatasourceDetailRoutes(t *testing.T) {
	service, principal, gateway, _, token := serviceFixture(t)
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	request := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	tables := request("/api/v1/datasource/tableList/10", "")
	var tableResponse struct {
		Data []struct {
			ID      int64  `json:"id"`
			Name    string `json:"table_name"`
			Checked bool   `json:"checked"`
			DSID    int64  `json:"ds_id"`
		} `json:"data"`
	}
	if tables.Code != http.StatusOK || json.Unmarshal(tables.Body.Bytes(), &tableResponse) != nil || len(tableResponse.Data) != 1 ||
		tableResponse.Data[0].ID < 1 || tableResponse.Data[0].ID > maxSQLBotBrowserID || tableResponse.Data[0].Name != "sales" ||
		!tableResponse.Data[0].Checked || tableResponse.Data[0].DSID != 10 {
		t.Fatalf("browser table list: %d %s", tables.Code, tables.Body.String())
	}
	tableID := strconv.FormatInt(tableResponse.Data[0].ID, 10)
	fields := request("/api/v1/datasource/fieldList/10/"+tableID, `{"fieldName":"am"}`)
	if fields.Code != http.StatusOK || !strings.Contains(fields.Body.String(), `"field_name":"amount"`) ||
		!strings.Contains(fields.Body.String(), `"table_id":`+tableID) || !strings.Contains(fields.Body.String(), `"field_type":"numeric"`) {
		t.Fatalf("browser field list: %d %s", fields.Code, fields.Body.String())
	}
	unknownID := tableResponse.Data[0].ID + 1
	if unknownID > maxSQLBotBrowserID {
		unknownID = 1
	}
	if w := request("/api/v1/datasource/fieldList/10/"+strconv.FormatInt(unknownID, 10), `{"fieldName":""}`); w.Code == http.StatusOK {
		t.Fatal("unknown table ID selected visible columns")
	}
	if w := request("/api/v1/datasource/fieldList/11/"+tableID, `{"fieldName":""}`); w.Code == http.StatusOK {
		t.Fatal("another datasource exposed table metadata")
	}
	gateway.afterSchema = func() {
		gateway.afterSchema = nil
		if err := service.Store.delete(context.Background(), bindingKey(principal, "10"), 1); err != nil {
			t.Fatal(err)
		}
	}
	if w := request("/api/v1/datasource/fieldList/10/"+tableID, `{"fieldName":""}`); w.Code == http.StatusOK || strings.Contains(w.Body.String(), "amount") {
		t.Fatal("revoked browser field list leaked metadata")
	}
}

func TestOriginalDatabaseDatasourceDetailUsesLiveSchemaAndRedactsGatewayCredentials(t *testing.T) {
	ctx := context.Background()
	service, principal, gateway, _, token := serviceFixture(t)
	user, revision, err := service.Store.User(ctx, principal.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	user.Admin = true
	if err := service.Store.put(ctx, userKey(user.ID), user, revision); err != nil {
		t.Fatal(err)
	}
	principal, err = service.Identity.Verify(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	binding, bindingRevision, _, err := service.bindingSnapshot(ctx, principal, "10")
	if err != nil {
		t.Fatal(err)
	}
	binding.Configuration = "sensitive-database-configuration"
	if err := service.Store.put(ctx, bindingKey(principal, "10"), binding, bindingRevision); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	request := func(credential string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/api/v1/datasource/get/10", nil)
		if credential != "" {
			r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+credential)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := request(""); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous datasource detail: %d", w.Code)
	}
	w := request(token)
	displayConfiguration, err := databaseDisplayConfiguration()
	if err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"type":"pg"`) ||
		!strings.Contains(w.Body.String(), `"num":"1/1"`) || !strings.Contains(w.Body.String(), `"configuration":"`+displayConfiguration+`"`) ||
		strings.Contains(w.Body.String(), binding.Configuration) || strings.Contains(w.Body.String(), binding.ConnectorToken) ||
		strings.Contains(w.Body.String(), binding.AgentKey) {
		t.Fatalf("database detail leaked or failed: %d %s", w.Code, w.Body.String())
	}
	other := testUser(t, "2", "other-admin")
	other.Admin = true
	if err := service.Store.CreateUser(ctx, other); err != nil {
		t.Fatal(err)
	}
	_, otherToken, err := service.Identity.Login(ctx, other.Account, "Synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	if w := request(otherToken); w.Code == http.StatusOK {
		t.Fatal("another administrator read a datasource they do not own")
	}
	gateway.afterSchema = func() {
		gateway.afterSchema = nil
		if err := service.Store.delete(ctx, bindingKey(principal, "10"), bindingRevision+1); err != nil {
			t.Fatal(err)
		}
	}
	if w := request(token); w.Code == http.StatusOK || strings.Contains(w.Body.String(), binding.Name) {
		t.Fatalf("revoked binding released database detail: %d %s", w.Code, w.Body.String())
	}
}

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
	var preview struct {
		Data struct {
			SQL string `json:"sql"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(preview.Data.SQL)
	if err != nil || string(decoded) != gateway.lastQuery {
		t.Fatalf("original browser expects Base64 SQL: %s, %v", preview.Data.SQL, err)
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

func TestDatasourcePreviewQualifiesMySQLTableWithAuthorizedSchema(t *testing.T) {
	service, principal, gateway, _, token := serviceFixture(t)
	binding, err := service.binding(context.Background(), principal, "10")
	if err != nil {
		t.Fatal(err)
	}
	binding.Dialect = "mysql"
	if err := service.Store.put(context.Background(), bindingKey(principal, "10"), binding, 1); err != nil {
		t.Fatal(err)
	}
	gateway.columns = []Column{{"agentsql_demo", "orders", "amount", "decimal"}}
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	request := func(table string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/datasource/previewData/10",
			strings.NewReader(`{"table":{"id":12,"table_name":"`+table+`"}}`))
		r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if got := request("orders"); got.Code != http.StatusOK ||
		gateway.lastQuery != "SELECT `amount` FROM `agentsql_demo`.`orders` LIMIT 100" {
		t.Fatalf("MySQL preview did not use authorized schema: %d %s sql=%s", got.Code, got.Body.String(), gateway.lastQuery)
	}
	before := gateway.queries
	if got := request("other"); got.Code == http.StatusOK || gateway.queries != before {
		t.Fatal("forged MySQL table reached the gateway")
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
