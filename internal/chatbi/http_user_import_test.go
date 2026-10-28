package chatbi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOriginalUserImportTemplateAndBoundedBatch(t *testing.T) {
	service, handler, adminToken, memberToken := adminUsersFixture(t)
	handler.(*Handler).initialUserPassword = "Synthetic-import-password-4"
	template := accountRequest(t, handler, http.MethodGet, "/api/v1/user/template", "", adminToken)
	if template.Code != http.StatusOK || !strings.Contains(template.Header().Get("Content-Type"), "spreadsheetml.sheet") {
		t.Fatalf("original user template unavailable: %d %s", template.Code, template.Body.String())
	}
	if rows, err := readSemanticWorkbook(template.Body.Bytes(), 8); err != nil || len(rows) != 1 || rows[0][0] != "example_member" {
		t.Fatalf("template is not a usable workbook: %v %+v", err, rows)
	}
	rows := [][]string{userImportHeaders("zh-CN"),
		{"imported_member", "导入成员", "member@example.invalid", "One", "普通成员", "已启用", "本地创建", ""},
		{"imported_admin", "导入管理员", "", "One", "管理员", "已禁用", "本地创建", ""},
		{"imported_disabled", "停用成员", "", "One", "普通成员", "已禁用", "本地创建", ""},
		{"imported_member", "重复成员", "", "One", "普通成员", "已启用", "本地创建", ""},
		{"external_member", "外部成员", "", "One", "普通成员", "已启用", "本地创建", "foreign-id"}}
	workbook, err := semanticWorkbook(rows)
	if err != nil {
		t.Fatal(err)
	}
	upload := func(token string) *httptest.ResponseRecorder {
		t.Helper()
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		part, err := form.CreateFormFile("file", "users.xlsx")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(workbook); err != nil {
			t.Fatal(err)
		}
		if err := form.Close(); err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/api/v1/user/batchImport", &body)
		request.Header.Set("Content-Type", form.FormDataContentType())
		request.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	if denied := upload(memberToken); denied.Code != http.StatusUnauthorized {
		t.Fatalf("ordinary member imported users: %d", denied.Code)
	}
	handler.(*Handler).initialUserPassword = ""
	if unconfigured := upload(adminToken); unconfigured.Code != http.StatusConflict {
		t.Fatalf("batch import used an implicit password: %d", unconfigured.Code)
	}
	handler.(*Handler).initialUserPassword = "Synthetic-import-password-4"
	created := upload(adminToken)
	var report struct {
		Data struct {
			SuccessCount int    `json:"successCount"`
			ErrorCount   int    `json:"errorCount"`
			DataKey      string `json:"dataKey"`
		} `json:"data"`
	}
	if created.Code != http.StatusOK || json.Unmarshal(created.Body.Bytes(), &report) != nil ||
		report.Data.SuccessCount != 2 || report.Data.ErrorCount != 3 || !ID(report.Data.DataKey).Valid() {
		t.Fatalf("unexpected user import report: %d %s", created.Code, created.Body.String())
	}
	var id ID
	if _, err := service.Store.get(context.Background(), service.Store.accountKey("imported_member"), &id); err != nil {
		t.Fatalf("imported account missing: %v", err)
	}
	user, _, err := service.Store.User(context.Background(), id)
	if err != nil || user.Name != "导入成员" || user.Admin || user.PasswordFormat != "bcrypt" {
		t.Fatalf("imported account invalid: %+v %v", user, err)
	}
	if _, _, err := service.Identity.Login(context.Background(), "imported_member", "Synthetic-import-password-4"); err != nil {
		t.Fatalf("imported member cannot use configured initial password: %v", err)
	}
	if _, err := service.Store.get(context.Background(), service.Store.accountKey("imported_admin"), &id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bulk import created an irreversible administrator: %v", err)
	}
	if _, err := service.Store.get(context.Background(), service.Store.accountKey("imported_disabled"), &id); err != nil {
		t.Fatalf("imported disabled member missing: %v", err)
	}
	user, _, err = service.Store.User(context.Background(), id)
	if err != nil || user.Admin || user.Status != 0 || len(user.Memberships) != 1 {
		t.Fatalf("disabled member status lost: %+v %v", user, err)
	}
	if _, _, err := service.Identity.Login(context.Background(), "imported_disabled", "Synthetic-import-password-4"); err == nil {
		t.Fatal("disabled imported member logged in")
	}
	if missing := accountRequest(t, handler, http.MethodGet, "/api/v1/user/errorRecord/1234567", "", adminToken); missing.Code != http.StatusNotFound {
		t.Fatalf("unowned error report readable: %d", missing.Code)
	}
	path := "/api/v1/user/errorRecord/" + report.Data.DataKey
	if denied := accountRequest(t, handler, http.MethodGet, path, "", memberToken); denied.Code != http.StatusUnauthorized {
		t.Fatalf("ordinary member downloaded user import report: %d", denied.Code)
	}
	errors := accountRequest(t, handler, http.MethodGet, path, "", adminToken)
	if errors.Code != http.StatusOK || !strings.Contains(errors.Header().Get("Content-Type"), "spreadsheetml.sheet") {
		t.Fatalf("user import error workbook unavailable: %d %s", errors.Code, errors.Body.String())
	}
	if failed, err := readSemanticWorkbook(errors.Body.Bytes(), 9); err != nil || len(failed) != 3 || failed[0][0] != "imported_admin" || failed[1][0] != "imported_member" || failed[2][0] != "external_member" {
		t.Fatalf("user import error workbook invalid: %v %+v", err, failed)
	}
}
