package chatbi

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOriginalAssistantImagesAreScopedAtomicAndRevocable(t *testing.T) {
	ctx := context.Background()
	service, principal, _, _, token := serviceFixture(t)
	user, revision, err := service.Store.User(ctx, principal.User.ID)
	require.NoError(t, err)
	user.Admin = true
	require.NoError(t, service.Store.put(ctx, userKey(user.ID), user, revision))
	principal.User = user
	app, err := service.SaveAssistant(ctx, principal, AssistantApplication{
		Name: "Original basic app", Type: 0, Domain: "https://app.example", Enabled: true,
		Configuration: `{"oid":1,"public_list":[10]}`,
	})
	require.NoError(t, err)
	handler, err := NewHandler(service, "test")
	require.NoError(t, err)
	var pngBody bytes.Buffer
	imageBody := image.NewRGBA(image.Rect(0, 0, 2, 2))
	imageBody.Set(0, 0, color.RGBA{R: 255, A: 255})
	require.NoError(t, png.Encode(&pngBody, imageBody))
	post := func(payload, filename string, content []byte) *httptest.ResponseRecorder {
		t.Helper()
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		if filename != "" {
			part, err := writer.CreateFormFile("files", filename)
			require.NoError(t, err)
			_, err = part.Write(content)
			require.NoError(t, err)
		}
		require.NoError(t, writer.WriteField("data", payload))
		require.NoError(t, writer.Close())
		r := httptest.NewRequest(http.MethodPatch, "/api/v1/system/assistant/ui", &body)
		r.Header.Set("Content-Type", writer.FormDataContentType())
		r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	picture := func(id string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/system/assistant/picture/"+id, nil))
		return w
	}
	readLogo := func() string {
		t.Helper()
		current, err := service.Assistant(ctx, principal, app.ID)
		require.NoError(t, err)
		var config struct {
			Logo   string `json:"logo"`
			Public []ID   `json:"public_list"`
		}
		require.NoError(t, json.Unmarshal([]byte(current.Configuration), &config))
		require.Equal(t, []ID{"10"}, config.Public)
		return config.Logo
	}
	request := `{"id":` + string(app.ID) + `,"logo":"","welcome":"Image test"}`
	require.Equal(t, http.StatusUnprocessableEntity, post(request, "logo.png,logo", []byte("not an image")).Code)
	require.Equal(t, http.StatusOK, post(request, "logo.png,logo", pngBody.Bytes()).Code)
	first := readLogo()
	require.True(t, validAssistantImageID(first))
	shown := picture(first)
	require.Equal(t, http.StatusOK, shown.Code)
	require.Equal(t, "image/png", shown.Header().Get("Content-Type"))
	require.Equal(t, "nosniff", shown.Header().Get("X-Content-Type-Options"))
	require.Equal(t, pngBody.Bytes(), shown.Body.Bytes())
	require.Equal(t, http.StatusUnprocessableEntity, post(`{"id":`+string(app.ID)+`,"logo":"forged.png"}`, "", nil).Code)
	require.Equal(t, first, readLogo())
	require.Equal(t, http.StatusOK, post(`{"id":`+string(app.ID)+`,"logo":"`+first+`"}`, "logo.png,logo", pngBody.Bytes()).Code)
	second := readLogo()
	require.NotEqual(t, first, second)
	require.Equal(t, http.StatusNotFound, picture(first).Code)
	require.Equal(t, http.StatusOK, picture(second).Code)
	_, err = service.Store.db.ExecContext(ctx, `CREATE TRIGGER reject_assistant_image_audit BEFORE INSERT ON chatbi_objects
		WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(FAIL, 'audit offline'); END`)
	require.NoError(t, err)
	require.NotEqual(t, http.StatusOK, post(`{"id":`+string(app.ID)+`,"logo":"`+second+`"}`, "logo.png,logo", pngBody.Bytes()).Code)
	require.Equal(t, second, readLogo())
	require.Equal(t, http.StatusOK, picture(second).Code)
	var imageCount int
	require.NoError(t, service.Store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chatbi_objects WHERE kind='assistant-image' AND owner=?`, string(app.ID)).Scan(&imageCount))
	require.Equal(t, 1, imageCount)
	_, err = service.Store.db.ExecContext(ctx, `DROP TRIGGER reject_assistant_image_audit`)
	require.NoError(t, err)
	current, err := service.Assistant(ctx, principal, app.ID)
	require.NoError(t, err)
	current.Enabled = false
	current, err = service.SaveAssistant(ctx, principal, current)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, picture(second).Code)
	current.Enabled = true
	_, err = service.SaveAssistant(ctx, principal, current)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, picture(second).Code)
	require.Equal(t, http.StatusOK, post(`{"id":`+string(app.ID)+`,"logo":""}`, "", nil).Code)
	require.Empty(t, readLogo())
	require.Equal(t, http.StatusNotFound, picture(second).Code)
	require.NoError(t, service.Store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chatbi_objects WHERE kind='assistant-image' AND owner=?`, string(app.ID)).Scan(&imageCount))
	require.Zero(t, imageCount)
	require.Equal(t, http.StatusOK, post(request, "logo.png,logo", pngBody.Bytes()).Code)
	last := readLogo()
	current, err = service.Assistant(ctx, principal, app.ID)
	require.NoError(t, err)
	require.NoError(t, service.DeleteAssistant(ctx, principal, app.ID, current.Revision))
	require.Equal(t, http.StatusNotFound, picture(last).Code)
	require.NoError(t, service.Store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chatbi_objects WHERE kind='assistant-image' AND owner=?`, string(app.ID)).Scan(&imageCount))
	require.Zero(t, imageCount)
}
