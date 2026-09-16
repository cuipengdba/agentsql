package adminapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestAdminAccessLogRecordsFinalStatus(t *testing.T) {
	tests := []struct {
		name   string
		status int
		write  bool
	}{
		{name: "implicit 200", status: http.StatusOK, write: true},
		{name: "401", status: http.StatusUnauthorized},
		{name: "404", status: http.StatusNotFound},
		{name: "405", status: http.StatusMethodNotAllowed},
		{name: "business error", status: http.StatusUnprocessableEntity},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var logs bytes.Buffer
			handler := &Handler{logger: zerolog.New(&logs), adminUser: "admin"}
			next := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				if test.write {
					_, err := writer.Write([]byte("ok"))
					require.NoError(t, err)
					return
				}
				writer.WriteHeader(test.status)
			})
			recorder := httptest.NewRecorder()
			handler.recover(next).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/test", nil))

			require.Equal(t, test.status, recorder.Code)
			entry := lastAdminLogEntry(t, logs.String())
			require.Equal(t, "GET", entry["method"])
			require.Equal(t, "/api/v1/test", entry["path"])
			require.Equal(t, "admin", entry["admin_user"])
			require.Equal(t, float64(test.status), entry["status"])
			require.Contains(t, entry, "latency_ms")
		})
	}
}

func TestAdminRecoverBeforeAndAfterCommittedHeader(t *testing.T) {
	tests := []struct {
		name       string
		next       http.Handler
		wantStatus int
		wantBody   string
	}{
		{
			name: "panic before header",
			next: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				panic("before")
			}),
			wantStatus: http.StatusInternalServerError,
			wantBody:   "internal error",
		},
		{
			name: "panic after header",
			next: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(http.StatusAccepted)
				_, _ = writer.Write([]byte("partial"))
				panic("after")
			}),
			wantStatus: http.StatusAccepted,
			wantBody:   "partial",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var logs bytes.Buffer
			handler := &Handler{logger: zerolog.New(&logs), adminUser: "admin"}
			recorder := httptest.NewRecorder()
			handler.recover(test.next).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/panic", nil))

			require.Equal(t, test.wantStatus, recorder.Code)
			require.Contains(t, recorder.Body.String(), test.wantBody)
			if test.wantStatus == http.StatusAccepted {
				require.NotContains(t, recorder.Body.String(), "internal error")
			}
			entry := lastAdminLogEntry(t, logs.String())
			require.Equal(t, float64(test.wantStatus), entry["status"])
		})
	}
}

func TestStatusRecorderUnwrapsForResponseController(t *testing.T) {
	underlying := httptest.NewRecorder()
	wrapper := &statusRecorder{ResponseWriter: underlying, status: http.StatusOK}
	require.Same(t, underlying, wrapper.Unwrap())
	require.NoError(t, http.NewResponseController(wrapper).Flush())
}

func lastAdminLogEntry(t *testing.T, encoded string) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(encoded), "\n")
	require.NotEmpty(t, lines)
	var entry map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &entry))
	require.Equal(t, "admin API request", entry["message"])
	return entry
}
