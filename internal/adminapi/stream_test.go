package adminapi

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/bootstrap"
	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/eventbus"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestStreamRequiresHeaderBearerAndFlusher(t *testing.T) {
	handler, token, _, cleanup := newStreamTestHandler(t, 1, time.Hour)
	defer cleanup()
	for _, target := range []string{"/api/v1/stream", "/api/v1/stream?token=" + token} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		require.Equal(t, http.StatusUnauthorized, recorder.Code)
		require.Contains(t, recorder.Body.String(), `"code":401`)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/stream", nil)
	request.Header.Set("Authorization", "Bearer wrong")
	handler.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusUnauthorized, recorder.Code)

	base := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/api/v1/stream", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	handler.ServeHTTP(&writerWithoutFlusher{ResponseWriter: base}, request)
	require.Equal(t, http.StatusInternalServerError, base.Code)
	require.Contains(t, base.Body.String(), `"code":500`)
}

func TestStreamHelloHistoryLiveHeadersAndSafeProjection(t *testing.T) {
	handler, token, runtime, cleanup := newStreamTestHandler(t, 2, time.Hour)
	defer cleanup()
	sentinelSQL := "PASSWORD_SENTINEL"
	sentinelError := "postgres://SECRET_SENTINEL@db/stack"
	sentinelIP := "CLIENT_SECRET_SENTINEL"
	for id := int64(1); id <= 2; id++ {
		runtime.Events.Publish(eventbus.Event{Audit: model.AuditLog{
			ID: id, TS: time.Unix(id, 0).UTC(), Decision: "deny", SQLRaw: &sentinelSQL,
			ErrorMsg: &sentinelError, ClientIP: &sentinelIP,
		}})
	}

	server := httptest.NewServer(handler)
	defer server.Close()
	request, err := http.NewRequest(http.MethodGet, server.URL+"/api/v1/stream", nil)
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := server.Client().Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, "text/event-stream", response.Header.Get("Content-Type"))
	require.Equal(t, "no-cache, no-store, no-transform", response.Header.Get("Cache-Control"))
	require.Equal(t, "keep-alive", response.Header.Get("Connection"))
	require.Equal(t, "no", response.Header.Get("X-Accel-Buffering"))

	reader := bufio.NewReader(response.Body)
	hello := readSSEFrame(t, reader)
	require.True(t, strings.HasPrefix(hello, "event: hello\n"), hello)
	require.Contains(t, hello, `"demo":false`)
	first := readSSEFrame(t, reader)
	second := readSSEFrame(t, reader)
	require.Contains(t, first, "id: 1\n")
	require.Contains(t, second, "id: 2\n")
	combined := hello + first + second
	require.NotContains(t, combined, sentinelSQL)
	require.NotContains(t, combined, sentinelError)
	require.NotContains(t, combined, sentinelIP)
	require.NotContains(t, combined, "sql_raw")
	require.NotContains(t, combined, "error_msg")

	runtime.Events.Publish(eventbus.Event{Audit: model.AuditLog{ID: 3, TS: time.Now(), Decision: "allow"}})
	live := readSSEFrame(t, reader)
	require.Contains(t, live, "event: audit\n")
	require.Contains(t, live, "id: 3\n")
}

func TestStreamHeartbeatConnectionLimitAndSlotReuse(t *testing.T) {
	handler, token, runtime, cleanup := newStreamTestHandler(t, 1, 10*time.Millisecond)
	defer cleanup()
	server := httptest.NewServer(handler)
	defer server.Close()
	open := func() *http.Response {
		request, err := http.NewRequest(http.MethodGet, server.URL+"/api/v1/stream", nil)
		require.NoError(t, err)
		request.Header.Set("Authorization", "Bearer "+token)
		response, err := server.Client().Do(request)
		require.NoError(t, err)
		return response
	}
	first := open()
	reader := bufio.NewReader(first.Body)
	require.Contains(t, readSSEFrame(t, reader), "event: hello")
	require.Equal(t, ": ping\n\n", readSSEFrame(t, reader))

	second := open()
	body, err := io.ReadAll(second.Body)
	require.NoError(t, err)
	require.NoError(t, second.Body.Close())
	require.Equal(t, http.StatusServiceUnavailable, second.StatusCode)
	require.Contains(t, string(body), `"code":503`)

	require.NoError(t, first.Body.Close())
	require.Eventually(t, func() bool { return runtime.Events.SubscriberCount() == 0 }, time.Second, time.Millisecond)
	third := open()
	require.Equal(t, http.StatusOK, third.StatusCode)
	require.NoError(t, third.Body.Close())
}

func TestNewHandlerFailsFastWhenEnabledStreamHasNoHub(t *testing.T) {
	opened, err := store.OpenWithSecret(context.Background(), filepath.Join(t.TempDir(), "admin.db"), []byte(adminTestSecret))
	require.NoError(t, err)
	defer opened.Close()
	cfg := adminTestConfig(filepath.Join(t.TempDir(), "unused.db"))
	cfg.Server.ConsoleEnabled = true
	cfg.Server.EventStream = true
	_, err = NewHandler(Deps{Runtime: &bootstrap.Runtime{Store: opened}, Config: cfg, AdminPassword: "password", TokenKey: []byte("key")}, zerolog.Nop())
	require.Error(t, err)
}

func TestDisabledEventStreamDoesNotRegisterRouteOrRequireHub(t *testing.T) {
	opened, err := store.OpenWithSecret(context.Background(), filepath.Join(t.TempDir(), "admin.db"), []byte(adminTestSecret))
	require.NoError(t, err)
	defer opened.Close()
	cfg := adminTestConfig(filepath.Join(t.TempDir(), "unused.db"))
	cfg.Server.EventStream = false
	key := DeriveTokenKey([]byte(adminTestSecret))
	handler, err := NewHandler(Deps{Runtime: &bootstrap.Runtime{Store: opened}, Config: cfg, AdminPassword: "password", TokenKey: key}, zerolog.Nop())
	require.NoError(t, err)
	token, _, err := issueAdminToken(key, time.Now(), "disabled-stream")
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/stream", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusNotFound, response.Code)
}

func TestStreamFailuresAndCancellationReleaseSubscriptionAndSlot(t *testing.T) {
	tests := []struct {
		name       string
		writeErr   error
		flushErr   error
		panicWrite bool
		panicFlush bool
	}{
		{name: "write error", writeErr: io.ErrClosedPipe},
		{name: "flush error", flushErr: io.ErrClosedPipe},
		{name: "write panic", panicWrite: true},
		{name: "flush panic", panicFlush: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			hub, err := eventbus.New(eventbus.Options{})
			require.NoError(t, err)
			defer hub.Close()
			handler := &Handler{
				deps:   Deps{Runtime: &bootstrap.Runtime{Events: hub}},
				logger: zerolog.Nop(), streamSlots: make(chan struct{}, 1), heartbeat: time.Hour,
			}
			writer := newControlledStreamWriter()
			writer.writeErr = test.writeErr
			writer.flushErr = test.flushErr
			writer.panicWrite = test.panicWrite
			writer.panicFlush = test.panicFlush
			handler.recover(http.HandlerFunc(handler.stream)).ServeHTTP(
				writer,
				httptest.NewRequest(http.MethodGet, "/api/v1/stream", nil),
			)
			require.Zero(t, hub.SubscriberCount())
			require.Len(t, handler.streamSlots, 0)
		})
	}

	hub, err := eventbus.New(eventbus.Options{})
	require.NoError(t, err)
	defer hub.Close()
	handler := &Handler{
		deps:   Deps{Runtime: &bootstrap.Runtime{Events: hub}},
		logger: zerolog.Nop(), streamSlots: make(chan struct{}, 1), heartbeat: time.Hour,
	}
	writer := newControlledStreamWriter()
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodGet, "/api/v1/stream", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		handler.recover(http.HandlerFunc(handler.stream)).ServeHTTP(writer, request)
		close(done)
	}()
	select {
	case <-writer.flushed:
	case <-time.After(time.Second):
		t.Fatal("hello was not flushed")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stream did not exit after cancellation")
	}
	require.Zero(t, hub.SubscriberCount())
	require.Len(t, handler.streamSlots, 0)
}

type writerWithoutFlusher struct{ http.ResponseWriter }

type controlledStreamWriter struct {
	mu         sync.Mutex
	header     http.Header
	writeErr   error
	flushErr   error
	panicWrite bool
	panicFlush bool
	flushed    chan struct{}
	flushOnce  sync.Once
}

func newControlledStreamWriter() *controlledStreamWriter {
	return &controlledStreamWriter{header: make(http.Header), flushed: make(chan struct{})}
}

func (writer *controlledStreamWriter) Header() http.Header { return writer.header }
func (writer *controlledStreamWriter) WriteHeader(int)     {}
func (writer *controlledStreamWriter) Write(body []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.panicWrite {
		panic("stream write panic")
	}
	if writer.writeErr != nil {
		return 0, writer.writeErr
	}
	return len(body), nil
}
func (writer *controlledStreamWriter) Flush() { _ = writer.FlushError() }
func (writer *controlledStreamWriter) FlushError() error {
	if writer.panicFlush {
		panic("stream flush panic")
	}
	writer.flushOnce.Do(func() { close(writer.flushed) })
	return writer.flushErr
}

func newStreamTestHandler(t *testing.T, maxConnections int, heartbeat time.Duration) (http.Handler, string, *bootstrap.Runtime, func()) {
	t.Helper()
	opened, err := store.OpenWithSecret(context.Background(), filepath.Join(t.TempDir(), "stream.db"), []byte(adminTestSecret))
	require.NoError(t, err)
	hub, err := eventbus.New(eventbus.Options{})
	require.NoError(t, err)
	runtime := &bootstrap.Runtime{Store: opened, Events: hub}
	cfg := config.Config{
		Server:   config.ServerConfig{HTTPListen: "127.0.0.1:7780", ConsoleEnabled: true, EventStream: true, EventStreamMaxConnections: maxConnections},
		Store:    config.StoreConfig{SQLitePath: filepath.Join(t.TempDir(), "unused.db")},
		Defaults: config.DefaultsConfig{StatementTimeoutMS: 5000, RowLimit: 1000, MaxConnsPerDatasource: 5, QPSPerAgent: 20},
		Theme:    config.ThemeConfig{Default: "dark"},
	}
	key := DeriveTokenKey([]byte(adminTestSecret))
	handler, err := NewHandler(Deps{Runtime: runtime, Config: cfg, AdminPassword: "password", TokenKey: key, EventStreamHeartbeatInterval: heartbeat}, zerolog.Nop())
	require.NoError(t, err)
	token, _, err := issueAdminToken(key, time.Now(), "stream-test")
	require.NoError(t, err)
	return handler, token, runtime, func() { hub.Close(); require.NoError(t, opened.Close()) }
}

func readSSEFrame(t *testing.T, reader *bufio.Reader) string {
	t.Helper()
	var frame strings.Builder
	for {
		line, err := reader.ReadString('\n')
		require.NoError(t, err)
		frame.WriteString(line)
		if line == "\n" {
			return frame.String()
		}
	}
}
