package chatbi

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOriginalEmbeddedUISettingsPreserveScopeAndRollbackWithAudit(t *testing.T) {
	ctx := context.Background()
	service, principal, _, _, token := serviceFixture(t)
	user, revision, err := service.Store.User(ctx, principal.User.ID)
	require.NoError(t, err)
	user.Admin = true
	require.NoError(t, service.Store.put(ctx, userKey(user.ID), user, revision))
	principal.User = user
	app, err := service.SaveAssistant(ctx, principal, AssistantApplication{
		Name: "Page", Type: 4, Domain: "https://app.example", Enabled: true,
		Configuration: `{"oid":1,"public_list":[10]}`,
	})
	require.NoError(t, err)
	handler, err := NewHandler(service, "test")
	require.NoError(t, err)
	post := func(payload string, file bool, authenticated bool) *httptest.ResponseRecorder {
		t.Helper()
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		require.NoError(t, writer.WriteField("data", payload))
		if file {
			part, err := writer.CreateFormFile("files", "logo.png,logo")
			require.NoError(t, err)
			_, err = part.Write([]byte("not an image"))
			require.NoError(t, err)
		}
		require.NoError(t, writer.Close())
		r := httptest.NewRequest(http.MethodPatch, "/api/v1/system/assistant/ui", &body)
		r.Header.Set("Content-Type", writer.FormDataContentType())
		if authenticated {
			r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	payload := `{"id":` + string(app.ID) + `,"theme":"#3370ff","header_font_color":"#FFFFFF",` +
		`"x_type":"right","x_val":30,"y_type":"bottom","y_val":33,` +
		`"float_icon_drag":true,"welcome":"Hello","welcome_desc":"Ask your data",` +
		`"configuration":{"oid":999},"app_secret":"forged"}`
	require.Equal(t, http.StatusUnauthorized, post(payload, false, false).Code)
	require.Equal(t, http.StatusOK, post(payload, false, true).Code)
	stored, err := service.Assistant(ctx, principal, app.ID)
	require.NoError(t, err)
	var config map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(stored.Configuration), &config))
	require.JSONEq(t, `1`, string(config["oid"]))
	require.JSONEq(t, `[10]`, string(config["public_list"]))
	require.JSONEq(t, `"#3370ff"`, string(config["theme"]))
	require.JSONEq(t, `"Hello"`, string(config["welcome"]))
	require.Equal(t, app.AppSecret, stored.AppSecret)
	for _, appType := range []int{0, 1} {
		legacy, err := service.SaveAssistant(ctx, principal, AssistantApplication{
			Name: "Original assistant", Type: appType, Domain: "https://app.example", Enabled: true,
			Configuration: `{"oid":1,"public_list":[10]}`,
		})
		require.NoError(t, err)
		response := post(`{"id":`+string(legacy.ID)+`,"welcome":"Original UI",`+
			`"x_type":"right","x_val":45,"y_type":"bottom","y_val":55}`, false, true)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		require.Contains(t, response.Body.String(), `"type":`+strconv.Itoa(appType))
		saved, err := service.Assistant(ctx, principal, legacy.ID)
		require.NoError(t, err)
		require.Equal(t, appType, saved.Type)
		var legacyConfig map[string]json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(saved.Configuration), &legacyConfig))
		require.JSONEq(t, `[10]`, string(legacyConfig["public_list"]))
		require.JSONEq(t, `"Original UI"`, string(legacyConfig["welcome"]))
	}
	require.Equal(t, http.StatusUnprocessableEntity, post(`{"id":`+string(app.ID)+`,"theme":"javascript:alert(1)"}`, false, true).Code)
	require.Equal(t, http.StatusUnprocessableEntity, post(`{"id":`+string(app.ID)+`,"welcome":"<script>"}`, false, true).Code)
	require.Equal(t, http.StatusUnprocessableEntity, post(payload, true, true).Code)
	_, err = service.Store.db.ExecContext(ctx, `CREATE TRIGGER reject_embedded_ui_audit BEFORE INSERT ON chatbi_objects
		WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(FAIL, 'audit offline'); END`)
	require.NoError(t, err)
	require.NotEqual(t, http.StatusOK, post(`{"id":`+string(app.ID)+`,"theme":"#111111"}`, false, true).Code)
	after, err := service.Assistant(ctx, principal, app.ID)
	require.NoError(t, err)
	require.Equal(t, stored.Configuration, after.Configuration)
}
