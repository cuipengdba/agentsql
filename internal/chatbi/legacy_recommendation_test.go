package chatbi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSQLBotRecommendationSaveUsesCurrentAdminAndAuditContract(t *testing.T) {
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
	call := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		method := http.MethodGet
		if body != "" {
			method = http.MethodPost
		}
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	checkDatasourceMode := func(want int) {
		t.Helper()
		response := call("/api/v1/datasource/list", "")
		var listing struct {
			Data []struct {
				ID   ID  `json:"id"`
				Mode int `json:"recommended_config"`
			} `json:"data"`
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &listing) != nil || strings.Contains(response.Body.String(), "Revenue by region") {
			t.Fatalf("unsafe datasource listing: %d %s", response.Code, response.Body.String())
		}
		for _, item := range listing.Data {
			if item.ID == "10" {
				if item.Mode != want {
					t.Fatalf("original datasource card mode = %d, want %d", item.Mode, want)
				}
				return
			}
		}
		t.Fatal("fixture datasource missing from list")
	}
	checkDatasourceMode(1)
	const savePath = "/api/v1/recommended_problem/save_recommended_problem"
	created := call(savePath, `{"datasource_id":10,"recommended_config":2,"problemInfo":[{"id":0,"datasource_id":10,"question":"Revenue by region","remark":"Synthetic"}]}`)
	if created.Code != http.StatusOK || !strings.Contains(created.Body.String(), `"data":null`) {
		t.Fatalf("original save envelope: %d %s", created.Code, created.Body.String())
	}
	checkDatasourceMode(2)
	var list struct {
		Data []struct {
			ID       ID     `json:"id"`
			Question string `json:"question"`
		} `json:"data"`
	}
	read := call("/api/v1/recommended_problem/get_datasource_recommended/10", "")
	if read.Code != http.StatusOK || json.Unmarshal(read.Body.Bytes(), &list) != nil ||
		len(list.Data) != 1 || list.Data[0].Question != "Revenue by region" || !list.Data[0].ID.Valid() {
		t.Fatalf("compatibility read after original save: %d %s", read.Code, read.Body.String())
	}
	for _, body := range []string{
		`{"datasource_id":10,"recommended_config":2,"problemInfo":[{"id":0,"datasource_id":11,"question":"wrong datasource"}]}`,
		`{"datasource_id":10,"recommended_config":2,"problemInfo":[{"id":999,"datasource_id":10,"question":"forged ID"}]}`,
		`{"datasource_id":10,"recommended_config":0,"problemInfo":[{"question":"unexpected"}]}`,
	} {
		result := call(savePath, body)
		if result.Code != http.StatusUnprocessableEntity {
			t.Fatalf("invalid legacy recommendation accepted: %d %s", result.Code, result.Body.String())
		}
	}
	if _, err := service.Store.db.ExecContext(ctx, `CREATE TRIGGER reject_legacy_recommendation_audit BEFORE INSERT ON chatbi_objects WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(ABORT,'synthetic audit fault'); END`); err != nil {
		t.Fatal(err)
	}
	failed := call(savePath, `{"datasource_id":10,"recommended_config":0,"problemInfo":[]}`)
	if failed.Code != http.StatusServiceUnavailable {
		t.Fatalf("audit failure did not close write: %d", failed.Code)
	}
	config, savedRevision, err := service.RecommendationSettings(ctx, principal, "10")
	if err != nil || savedRevision != 1 || len(config.Questions) != 1 {
		t.Fatal("audit failure changed the original recommendation state")
	}
	checkDatasourceMode(2)
	user, revision, err = service.Store.User(ctx, principal.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	user.Admin = false
	if err := service.Store.put(ctx, userKey(user.ID), user, revision); err != nil {
		t.Fatal(err)
	}
	denied := call(savePath, `{"datasource_id":10,"recommended_config":0,"problemInfo":[]}`)
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("revoked administrator saved recommendations: %d", denied.Code)
	}
}
