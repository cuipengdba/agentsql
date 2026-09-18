package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/eventbus"
	"github.com/stretchr/testify/require"
)

func TestReloadDoesNotReplayHistoryOrPreviouslySentEvents(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	hub := newHub(t)
	hub.Publish(eventbus.Event{Audit: sensitiveAudit()})
	manager := NewManager(hub, WithRetryPolicy(0, time.Millisecond, time.Millisecond))
	config := webhookManagerConfig(server.URL, WebhookGeneric, true, 4)
	require.NoError(t, manager.Start(context.Background(), config))
	defer manager.Close()
	time.Sleep(20 * time.Millisecond)
	require.Zero(t, requests.Load(), "Start must not replay retained history")

	audit := sensitiveAudit()
	audit.ID = 43
	hub.Publish(eventbus.Event{Audit: audit})
	require.Eventually(t, func() bool { return requests.Load() == 1 }, time.Second, time.Millisecond)
	require.NoError(t, manager.Reload(context.Background(), config))
	time.Sleep(20 * time.Millisecond)
	require.Equal(t, int32(1), requests.Load(), "Reload must not replay retained history")
	audit.ID = 44
	hub.Publish(eventbus.Event{Audit: audit})
	require.Eventually(t, func() bool { return requests.Load() == 2 }, time.Second, time.Millisecond)
}

func TestDisabledAndEmptyConfigRemainInert(t *testing.T) {
	hub := newHub(t)
	manager := NewManager(hub)
	require.NoError(t, manager.Start(context.Background(), Config{}))
	require.Zero(t, hub.SubscriberCount())
	require.NoError(t, manager.Reload(context.Background(), Config{Enabled: true}))
	require.Zero(t, hub.SubscriberCount())
	hub.Publish(eventbus.Event{Audit: sensitiveAudit()})
	require.Empty(t, manager.Status())
}

func TestCloseIsIdempotentAndConcurrentSafe(t *testing.T) {
	hub := newHub(t)
	manager := NewManager(hub)
	require.NoError(t, manager.Start(context.Background(), Config{}))
	var waitGroup sync.WaitGroup
	for index := 0; index < 16; index++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			require.NoError(t, manager.Close())
		}()
	}
	waitGroup.Wait()
	require.ErrorIs(t, manager.Reload(context.Background(), Config{}), ErrManagerClosed)
	require.ErrorIs(t, manager.Start(context.Background(), Config{}), ErrManagerClosed)
}
