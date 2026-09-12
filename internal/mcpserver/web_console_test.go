package mcpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestWebConsoleOptionPreservesSpecificRoutePriority(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	admin := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})
	console := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/html")
		_, _ = writer.Write([]byte("console"))
	})
	handler, _, err := newHTTPHandlerWithRegistry(
		fixture.runtime,
		httpTestConfig(100),
		zerolog.Nop(),
		WithAdminAPI(admin),
		WithWebConsole(console),
	)
	require.NoError(t, err)

	rootRecorder := httptest.NewRecorder()
	handler.ServeHTTP(rootRecorder, httptest.NewRequest(http.MethodGet, "/audit", nil))
	require.Equal(t, http.StatusOK, rootRecorder.Code)
	require.Equal(t, "console", rootRecorder.Body.String())

	adminRecorder := httptest.NewRecorder()
	handler.ServeHTTP(adminRecorder, httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil))
	require.Equal(t, http.StatusNoContent, adminRecorder.Code)

	mcpRecorder := httptest.NewRecorder()
	handler.ServeHTTP(mcpRecorder, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	require.Equal(t, http.StatusUnauthorized, mcpRecorder.Code)
}
