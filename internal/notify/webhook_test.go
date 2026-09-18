package notify

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/eventbus"
	"github.com/stretchr/testify/require"
)

func TestWebhookSuccessUpdatesStatus(t *testing.T) {
	requests := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		requests <- body
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	hub := newHub(t)
	manager := NewManager(hub, WithRetryPolicy(2, time.Millisecond, 2*time.Millisecond))
	require.NoError(t, manager.Start(context.Background(), webhookManagerConfig(server.URL, WebhookGeneric, true, 4)))
	defer manager.Close()

	hub.Publish(eventbus.Event{Audit: sensitiveAudit()})
	select {
	case body := <-requests:
		assertNoSensitiveText(t, string(body))
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for webhook")
	}
	require.Eventually(t, func() bool {
		return manager.Status()["hook"].Sent == 1
	}, time.Second, time.Millisecond)
	status := manager.Status()["hook"]
	require.Equal(t, uint64(1), status.Sent)
	require.Zero(t, status.Failed)
	require.Zero(t, status.Dropped)
	require.False(t, status.LastSuccessAt.IsZero())
}

func TestWebhookFailuresRetryAtMostTwice(t *testing.T) {
	t.Run("http 5xx", func(t *testing.T) {
		var attempts atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			attempts.Add(1)
			writer.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer server.Close()
		hub := newHub(t)
		manager := NewManager(hub, WithRetryPolicy(2, time.Millisecond, 2*time.Millisecond))
		require.NoError(t, manager.Start(context.Background(), webhookManagerConfig(server.URL, WebhookGeneric, true, 4)))
		defer manager.Close()
		hub.Publish(eventbus.Event{Audit: sensitiveAudit()})
		require.Eventually(t, func() bool { return manager.Status()["hook"].Failed == 1 }, time.Second, time.Millisecond)
		require.Equal(t, int32(3), attempts.Load(), "initial attempt plus at most two retries")
		require.Equal(t, "http_status", manager.Status()["hook"].LastError)
	})

	t.Run("connection refused", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		address := listener.Addr().String()
		require.NoError(t, listener.Close())
		hub := newHub(t)
		manager := NewManager(hub, WithHTTPTimeout(40*time.Millisecond), WithRetryPolicy(2, time.Millisecond, 2*time.Millisecond))
		config := webhookManagerConfig("http://"+address, WebhookGeneric, true, 4)
		require.NoError(t, manager.Start(context.Background(), config))
		defer manager.Close()
		hub.Publish(eventbus.Event{Audit: sensitiveAudit()})
		require.Eventually(t, func() bool { return manager.Status()["hook"].Failed == 1 }, time.Second, time.Millisecond)
		require.Equal(t, "connect_error", manager.Status()["hook"].LastError)
	})
}

func TestWebhookHangStopsAfterTimeoutAndDoesNotBlockPublish(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-request.Context().Done():
		}
	}))
	hub := newHub(t)
	manager := NewManager(hub, WithHTTPTimeout(30*time.Millisecond), WithRetryPolicy(2, time.Millisecond, 2*time.Millisecond))
	require.NoError(t, manager.Start(context.Background(), webhookManagerConfig(server.URL, WebhookGeneric, true, 2)))

	hub.Publish(eventbus.Event{Audit: sensitiveAudit()})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("webhook handler was not entered")
	}
	started := time.Now()
	for id := int64(1000); id < 3000; id++ {
		audit := sensitiveAudit()
		audit.ID = id
		hub.Publish(eventbus.Event{Audit: audit})
	}
	require.Less(t, time.Since(started), time.Second, "notification backpressure reached event publishers")
	require.Eventually(t, func() bool { return manager.Status()["hook"].Failed >= 1 }, 2*time.Second, time.Millisecond)
	require.NoError(t, manager.Close())
	close(release)
	server.Close()
}

func TestQueueFullDropsWithoutBlockingOrPanicking(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-release:
			writer.WriteHeader(http.StatusNoContent)
		case <-request.Context().Done():
		}
	}))
	defer server.Close()
	hub := newHub(t)
	manager := NewManager(hub, WithHTTPTimeout(time.Second), WithRetryPolicy(0, time.Millisecond, time.Millisecond))
	require.NoError(t, manager.Start(context.Background(), webhookManagerConfig(server.URL, WebhookGeneric, true, 1)))
	defer manager.Close()

	hub.Publish(eventbus.Event{Audit: sensitiveAudit()})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("worker did not begin delivery")
	}
	for id := int64(43); id < 50; id++ {
		audit := sensitiveAudit()
		audit.ID = id
		require.NotPanics(t, func() { hub.Publish(eventbus.Event{Audit: audit}) })
	}
	require.Eventually(t, func() bool { return manager.Status()["hook"].Dropped > 0 }, time.Second, time.Millisecond)
	close(release)
}

func TestGenericHMACAndDingTalkSignature(t *testing.T) {
	fixed := time.Date(2026, 9, 19, 3, 4, 5, 0, time.UTC)

	t.Run("generic", func(t *testing.T) {
		type captured struct {
			body      []byte
			timestamp string
			signature string
		}
		requests := make(chan captured, 1)
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			body, _ := io.ReadAll(request.Body)
			requests <- captured{body: body, timestamp: request.Header.Get("X-AgentSQL-Timestamp"), signature: request.Header.Get("X-AgentSQL-Signature")}
			writer.WriteHeader(http.StatusNoContent)
		}))
		defer server.Close()
		hub := newHub(t)
		manager := NewManager(hub, WithRetryPolicy(0, time.Millisecond, time.Millisecond))
		manager.options.now = func() time.Time { return fixed }
		config := webhookManagerConfig(server.URL, WebhookGeneric, true, 2)
		config.Channels[0].Webhook.Secret = "generic-secret"
		require.NoError(t, manager.Start(context.Background(), config))
		defer manager.Close()
		hub.Publish(eventbus.Event{Audit: sensitiveAudit()})
		select {
		case request := <-requests:
			require.Equal(t, "1789787045", request.timestamp)
			require.Equal(t, genericSignature(request.timestamp, request.body, "generic-secret"), request.signature)
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for signed generic webhook")
		}
	})

	t.Run("dingtalk", func(t *testing.T) {
		queries := make(chan url.Values, 1)
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			queries <- request.URL.Query()
			writer.WriteHeader(http.StatusNoContent)
		}))
		defer server.Close()
		hub := newHub(t)
		manager := NewManager(hub, WithRetryPolicy(0, time.Millisecond, time.Millisecond))
		manager.options.now = func() time.Time { return fixed }
		config := webhookManagerConfig(server.URL+"?access_token=token", WebhookDingTalk, true, 2)
		config.Channels[0].Webhook.Secret = "ding-secret"
		require.NoError(t, manager.Start(context.Background(), config))
		defer manager.Close()
		hub.Publish(eventbus.Event{Audit: sensitiveAudit()})
		select {
		case query := <-queries:
			expectedURL, err := dingTalkSignedURL(server.URL+"?access_token=token", "ding-secret", fixed)
			require.NoError(t, err)
			expected, err := url.Parse(expectedURL)
			require.NoError(t, err)
			require.Equal(t, expected.Query().Get("timestamp"), query.Get("timestamp"))
			require.Equal(t, expected.Query().Get("sign"), query.Get("sign"))
			require.Equal(t, "token", query.Get("access_token"))
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for signed DingTalk webhook")
		}
	})
}

func webhookManagerConfig(rawURL string, template WebhookTemplate, allowPrivate bool, queueSize int) Config {
	return Config{Enabled: true, QueueSize: queueSize, Channels: []ChannelConfig{{
		ID: "hook", Enabled: true, Kind: ChannelWebhook, AllowPrivateEndpoints: allowPrivate,
		Webhook: &WebhookConfig{Template: template, URL: rawURL},
	}}}
}

func newHub(t *testing.T) *eventbus.Hub {
	t.Helper()
	hub, err := eventbus.New(eventbus.Options{HistorySize: 8, SubscriberBuffer: 64})
	require.NoError(t, err)
	t.Cleanup(hub.Close)
	return hub
}
