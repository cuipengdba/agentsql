package chatbi

import (
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAssistantOnlyHandlerSeparatesPublicIframeFromManagement(t *testing.T) {
	service, _, _, _, _ := serviceFixture(t)
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	service.Identity.privateKey = privateKey
	base, err := NewHandler(service, "test")
	require.NoError(t, err)
	isolated := AssistantOnlyHandler(base)
	call := func(method, path string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		isolated.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		return w
	}
	root := call(http.MethodGet, "/")
	require.Equal(t, http.StatusOK, root.Code)
	require.Contains(t, root.Body.String(), "/ui/assistant.js")
	require.NotContains(t, root.Body.String(), "login-form")
	require.Contains(t, root.Header().Get("Content-Security-Policy"), "frame-ancestors https: http:")
	require.Equal(t, "no-store", root.Header().Get("Cache-Control"))
	for _, asset := range []string{"/ui/assistant.js", "/ui/assistant.css", "/ui/mark.svg"} {
		require.Equal(t, http.StatusOK, call(http.MethodGet, asset).Code, asset)
	}
	for _, path := range []string{"/ui/app.js", "/ui/index.html", "/api/v1/agentsql/assistants", "/api/v1/system/assistant", "/api/v1/user/pager/1/10", "/api/v1/system/aimodel", "/api/v1/chat/../system/assistant"} {
		require.Equal(t, http.StatusNotFound, call(http.MethodGet, path).Code, path)
	}
	require.Equal(t, http.StatusNotFound, call(http.MethodPost, "/api/v1/login/access-token").Code)
	publicKey := call(http.MethodGet, "/api/v1/system/config/key")
	require.Equal(t, http.StatusOK, publicKey.Code)
	require.Equal(t, "*", publicKey.Header().Get("Access-Control-Allow-Origin"))
	require.Equal(t, http.StatusUnauthorized, call(http.MethodGet, "/api/v1/system/assistant/ds").Code)
	post := httptest.NewRequest(http.MethodPost, "/api/v1/chat/question", strings.NewReader(`{}`))
	post.Header.Set("X-SQLBOT-TOKEN", "Bearer untrusted")
	w := httptest.NewRecorder()
	isolated.ServeHTTP(w, post)
	require.Equal(t, http.StatusUnauthorized, w.Code)
	wrongMode := httptest.NewRequest(http.MethodGet, "/api/v1/chat/list", nil)
	wrongMode.Header.Set("X-SQLBOT-ASSISTANT-TOKEN", "Embedded untrusted")
	w = httptest.NewRecorder()
	isolated.ServeHTTP(w, wrongMode)
	require.Equal(t, http.StatusUnauthorized, w.Code)

	adminRoot := httptest.NewRecorder()
	base.ServeHTTP(adminRoot, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Contains(t, adminRoot.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'")
}
