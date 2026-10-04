package mcpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestStreamableHTTPPersistentEventReplay(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	cfg := statefulHTTPTestConfig(1_000)
	handler, err := NewHTTPHandler(fixture.runtime, cfg, zerolog.Nop())
	require.NoError(t, err)
	initialized := serveMCPWithSession(t, handler, fixture.handlers.apiKey, http.MethodPost, initializeRequest, "")
	sessionID := initialized.Header().Get(mcpSessionIDHeader)
	require.NotEmpty(t, sessionID)

	effective := cfg.EffectiveMCP().HTTP
	eventStore := newPersistentEventStore(
		fixture.runtime.Store.MCPStreamEvents(),
		effective.EventStoreMaxBytes,
		time.Duration(effective.EventStoreTTLMS)*time.Millisecond,
	)
	require.NoError(t, eventStore.Open(context.Background(), sessionID, "replay"))
	for _, data := range [][]byte{[]byte(`{"seq":0}`), []byte(`{"seq":1}`), []byte(`{"seq":2}`)} {
		require.NoError(t, eventStore.Append(context.Background(), sessionID, "replay", data))
	}

	request := newMCPRequest(fixture.handlers.apiKey, http.MethodGet, "", sessionID)
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Last-Event-ID", "replay_0")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, "text/event-stream", response.Header().Get("Content-Type"))
	body := response.Body.String()
	require.NotContains(t, body, `{"seq":0}`)
	requireSSEEventOrder(t, body,
		"id: replay_1", `data: {"seq":1}`,
		"id: replay_2", `data: {"seq":2}`,
	)
}

func TestStreamableHTTPPersistentEventReplayFailsClosedAfterPurge(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	cfg := statefulHTTPTestConfig(1_000)
	cfg.MCP.HTTP.EventStoreMaxBytes = 1
	handler, err := NewHTTPHandler(fixture.runtime, cfg, zerolog.Nop())
	require.NoError(t, err)
	initialized := serveMCPWithSession(t, handler, fixture.handlers.apiKey, http.MethodPost, initializeRequest, "")
	sessionID := initialized.Header().Get(mcpSessionIDHeader)
	require.NotEmpty(t, sessionID)

	effective := cfg.EffectiveMCP().HTTP
	eventStore := newPersistentEventStore(
		fixture.runtime.Store.MCPStreamEvents(),
		effective.EventStoreMaxBytes,
		time.Duration(effective.EventStoreTTLMS)*time.Millisecond,
	)
	require.NoError(t, eventStore.Open(context.Background(), sessionID, "purged"))
	require.NoError(t, eventStore.Append(context.Background(), sessionID, "purged", []byte("a")))
	require.NoError(t, eventStore.Append(context.Background(), sessionID, "purged", []byte("too-large")))

	request := newMCPRequest(fixture.handlers.apiKey, http.MethodGet, "", sessionID)
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Last-Event-ID", "purged_0")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	require.Equal(t, "failed to replay events\n", response.Body.String())
	require.NotContains(t, response.Body.String(), "too-large")
}

func TestStreamableHTTPRestartSignalsExpiredSessionAndReinitializes(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	cfg := statefulHTTPTestConfig(1_000)
	cfg.MCP.HTTP.EventStoreTTLMS = 1
	first, err := NewHTTPHandler(fixture.runtime, cfg, zerolog.Nop())
	require.NoError(t, err)
	initialized := serveMCPWithSession(t, first, fixture.handlers.apiKey, http.MethodPost, initializeRequest, "")
	oldSessionID := initialized.Header().Get(mcpSessionIDHeader)
	require.NotEmpty(t, oldSessionID)

	eventStore := newPersistentEventStore(fixture.runtime.Store.MCPStreamEvents(), 1<<20, time.Millisecond)
	require.NoError(t, eventStore.Open(context.Background(), oldSessionID, "old"))
	require.NoError(t, eventStore.Append(context.Background(), oldSessionID, "old", []byte("old-session-event")))
	time.Sleep(5 * time.Millisecond)

	restarted, err := NewHTTPHandler(fixture.runtime, cfg, zerolog.Nop())
	require.NoError(t, err)
	expired := serveMCPWithSession(t, restarted, fixture.handlers.apiKey, http.MethodPost, listToolsRequest, oldSessionID)
	require.Equal(t, http.StatusNotFound, expired.Code, expired.Body.String())
	require.Equal(t, MCPSessionExpiredValue, expired.Header().Get(MCPSessionExpiredHeader))
	require.Contains(t, expired.Body.String(), "session not found")

	reinitialized := serveMCPWithSession(t, restarted, fixture.handlers.apiKey, http.MethodPost, initializeRequest, "")
	require.Equal(t, http.StatusOK, reinitialized.Code, reinitialized.Body.String())
	newSessionID := reinitialized.Header().Get(mcpSessionIDHeader)
	require.NotEmpty(t, newSessionID)
	require.NotEqual(t, oldSessionID, newSessionID)
	listed := serveMCPWithSession(t, restarted, fixture.handlers.apiKey, http.MethodPost, listToolsRequest, newSessionID)
	require.Equal(t, http.StatusOK, listed.Code, listed.Body.String())
	require.Len(t, decodeToolNames(t, listed.Body.Bytes()), 7)

	_, _, err = fixture.runtime.Store.MCPStreamEvents().StreamEventsAfter(context.Background(), oldSessionID, "old", -1)
	require.ErrorIs(t, err, store.ErrMCPStreamNotFound, "new activity must lazily reclaim expired crash residue")
	request := newMCPRequest(fixture.handlers.apiKey, http.MethodGet, "", newSessionID)
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Last-Event-ID", "old_0")
	response := httptest.NewRecorder()
	restarted.ServeHTTP(response, request)
	require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	require.NotContains(t, response.Body.String(), "old-session-event")
}

func requireSSEEventOrder(t *testing.T, body string, fragments ...string) {
	t.Helper()
	position := 0
	for _, fragment := range fragments {
		next := strings.Index(body[position:], fragment)
		require.NotEqual(t, -1, next, "missing SSE fragment %q in %q", fragment, body)
		position += next + len(fragment)
	}
}
