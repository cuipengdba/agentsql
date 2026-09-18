package adminapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/eventbus"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/notify"
	"github.com/stretchr/testify/require"
)

func TestNotificationsGETRedactsSecretsAndRequiresBearer(t *testing.T) {
	fixture := newAdminFixture(t)
	config := notificationTestConfig("https://hooks.example.test/services/url-token")
	config.Channels[0].Webhook.BearerToken = "bearer-secret"
	config.Channels[0].Webhook.Secret = "signing-secret"
	config.Channels[0].Webhook.Headers = map[string]string{
		"Authorization": "header-secret",
		"X-Trace":       "also-sensitive",
	}
	require.NoError(t, fixture.store.Notifications().Replace(context.Background(), config))

	status, _ := fixture.request(http.MethodGet, "/api/v1/integrations/notifications", "", "")
	require.Equal(t, http.StatusUnauthorized, status)
	status, body := fixture.request(http.MethodGet, "/api/v1/integrations/notifications", fixture.adminToken, "")
	require.Equal(t, http.StatusOK, status, body)
	for _, secret := range []string{"url-token", "bearer-secret", "signing-secret", "header-secret", "also-sensitive"} {
		require.NotContains(t, body, secret)
	}
	var envelope struct {
		Data notificationConfigDTO `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &envelope))
	webhook := envelope.Data.Channels[0].Webhook
	require.Equal(t, notificationSecretMask, webhook.URL)
	require.True(t, webhook.URLConfigured)
	require.Equal(t, notificationSecretMask, webhook.BearerToken)
	require.True(t, webhook.BearerTokenConfigured)
	require.Equal(t, notificationSecretMask, webhook.Secret)
	require.True(t, webhook.SecretConfigured)
	require.Equal(t, notificationSecretMask, webhook.Headers["Authorization"])
	require.True(t, webhook.HeadersConfigured)
}

func TestNotificationsPUTPreservesBlankSecretsAndRejectsUnsafeTargets(t *testing.T) {
	fixture := newAdminFixture(t)
	receiver := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(receiver.Close)

	first := notificationConfigToDTO(notificationTestConfig(receiver.URL + "/private-token"))
	first.Channels[0].Webhook.URL = receiver.URL + "/private-token"
	first.Channels[0].Webhook.BearerToken = "bearer-secret"
	first.Channels[0].Webhook.Secret = "signing-secret"
	first.Channels[0].Webhook.Headers = map[string]string{"Authorization": "header-secret"}
	status, body := fixture.request(http.MethodPut, "/api/v1/integrations/notifications", fixture.adminToken, mustJSON(t, first))
	require.Equal(t, http.StatusOK, status, body)
	require.NotContains(t, body, "private-token")

	second := first
	second.Channels[0].Webhook.URL = ""
	second.Channels[0].Webhook.BearerToken = notificationSecretMask
	second.Channels[0].Webhook.Secret = ""
	second.Channels[0].Webhook.Headers = map[string]string{"Authorization": ""}
	status, body = fixture.request(http.MethodPut, "/api/v1/integrations/notifications", fixture.adminToken, mustJSON(t, second))
	require.Equal(t, http.StatusOK, status, body)
	stored, err := fixture.store.Notifications().Get(context.Background())
	require.NoError(t, err)
	webhook := stored.Channels[0].Webhook
	require.Equal(t, receiver.URL+"/private-token", webhook.URL)
	require.Equal(t, "bearer-secret", webhook.BearerToken)
	require.Equal(t, "signing-secret", webhook.Secret)
	require.Equal(t, "header-secret", webhook.Headers["Authorization"])

	invalid := first
	invalid.Channels[0].Webhook.URL = "ftp://example.com/hook"
	status, _ = fixture.request(http.MethodPut, "/api/v1/integrations/notifications", fixture.adminToken, mustJSON(t, invalid))
	require.Equal(t, http.StatusUnprocessableEntity, status)
	blocked := first
	blocked.Channels[0].AllowPrivateEndpoints = false
	blocked.Channels[0].Webhook.URL = "https://127.0.0.1/hook"
	status, _ = fixture.request(http.MethodPut, "/api/v1/integrations/notifications", fixture.adminToken, mustJSON(t, blocked))
	require.Equal(t, http.StatusUnprocessableEntity, status)
	after, err := fixture.store.Notifications().Get(context.Background())
	require.NoError(t, err)
	require.Equal(t, stored, after)
}

func TestNotificationsTestDeliversWithoutPersistingOrAuditing(t *testing.T) {
	fixture := newAdminFixture(t)
	var deliveries atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		deliveries.Add(1)
		require.Equal(t, "Bearer stored-bearer", request.Header.Get("Authorization"))
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("downstream-secret-response"))
	}))
	t.Cleanup(receiver.Close)

	saved := notificationTestConfig(receiver.URL + "/stored-url-token")
	saved.Channels[0].Webhook.BearerToken = "stored-bearer"
	require.NoError(t, fixture.store.Notifications().Replace(context.Background(), saved))
	beforeAudit, err := fixture.store.AuditLogs().Page(context.Background(), 1, 1)
	require.NoError(t, err)

	channel := notificationConfigToDTO(saved).Channels[0]
	channel.Webhook.URL = ""
	channel.Webhook.BearerToken = ""
	status, body := fixture.request(http.MethodPost, "/api/v1/integrations/notifications/test", fixture.adminToken,
		mustJSON(t, notificationTestInput{Channel: channel}))
	require.Equal(t, http.StatusOK, status, body)
	require.Contains(t, body, `"success":true`)
	require.Contains(t, body, `"category":"sent"`)
	require.NotContains(t, body, "stored-url-token")
	require.NotContains(t, body, "stored-bearer")
	require.NotContains(t, body, "downstream-secret-response")
	require.Equal(t, int32(1), deliveries.Load())

	unreachable := channel
	unreachable.ID = "unreachable"
	unreachable.Webhook.URL = "http://127.0.0.1:1/hook"
	unreachable.Webhook.BearerToken = ""
	status, body = fixture.request(http.MethodPost, "/api/v1/integrations/notifications/test", fixture.adminToken,
		mustJSON(t, notificationTestInput{Channel: unreachable}))
	require.Equal(t, http.StatusOK, status, body)
	require.Contains(t, body, `"success":false`)
	require.Contains(t, body, `"category":"connect_error"`)
	require.NotContains(t, body, "127.0.0.1")

	afterConfig, err := fixture.store.Notifications().Get(context.Background())
	require.NoError(t, err)
	require.Equal(t, saved, afterConfig)
	afterAudit, err := fixture.store.AuditLogs().Page(context.Background(), 1, 1)
	require.NoError(t, err)
	require.Equal(t, beforeAudit.Total, afterAudit.Total)
}

func TestNotificationsHealthTracksLiveDeliveryWithoutSecrets(t *testing.T) {
	fixture := newAdminFixture(t)
	received := make(chan struct{}, 1)
	receiver := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		select {
		case received <- struct{}{}:
		default:
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(receiver.Close)
	config := notificationTestConfig(receiver.URL + "/health-secret-token")
	input := notificationConfigToDTO(config)
	input.Channels[0].Webhook.URL = receiver.URL + "/health-secret-token"
	status, body := fixture.request(http.MethodPut, "/api/v1/integrations/notifications", fixture.adminToken,
		mustJSON(t, input))
	require.Equal(t, http.StatusOK, status, body)

	fixture.runtime.Events.Publish(eventbus.Event{Audit: model.AuditLog{ID: 42, TS: time.Now(), Decision: "deny"}})
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("notification webhook was not called")
	}
	require.Eventually(t, func() bool {
		status, body = fixture.request(http.MethodGet, "/api/v1/integrations/notifications/health", fixture.adminToken, "")
		return status == http.StatusOK && strings.Contains(body, `"sent":1`)
	}, time.Second, 10*time.Millisecond)
	require.NotContains(t, body, "health-secret-token")
	require.Contains(t, body, `"id":"primary"`)
	require.Contains(t, body, `"failed":0`)
	require.Contains(t, body, `"dropped":0`)
}

func TestNotificationsPUTReloadStopsOldChannelAndDoesNotReplayHistory(t *testing.T) {
	fixture := newAdminFixture(t)
	var oldCount, newCount atomic.Int32
	oldReceiver := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		oldCount.Add(1)
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer oldReceiver.Close()
	newReceiver := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		newCount.Add(1)
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer newReceiver.Close()

	oldInput := notificationConfigToDTO(notificationTestConfig(oldReceiver.URL))
	oldInput.Channels[0].Webhook.URL = oldReceiver.URL
	status, body := fixture.request(http.MethodPut, "/api/v1/integrations/notifications", fixture.adminToken, mustJSON(t, oldInput))
	require.Equal(t, http.StatusOK, status, body)
	fixture.runtime.Events.Publish(eventbus.Event{Audit: model.AuditLog{ID: 1, TS: time.Now(), Decision: "deny"}})
	require.Eventually(t, func() bool { return oldCount.Load() == 1 }, time.Second, 10*time.Millisecond)

	newInput := oldInput
	newInput.Channels[0].Webhook.URL = newReceiver.URL
	status, body = fixture.request(http.MethodPut, "/api/v1/integrations/notifications", fixture.adminToken, mustJSON(t, newInput))
	require.Equal(t, http.StatusOK, status, body)
	time.Sleep(30 * time.Millisecond)
	require.Zero(t, newCount.Load(), "reload must not replay retained event history")
	fixture.runtime.Events.Publish(eventbus.Event{Audit: model.AuditLog{ID: 2, TS: time.Now(), Decision: "deny"}})
	require.Eventually(t, func() bool { return newCount.Load() == 1 }, time.Second, 10*time.Millisecond)
	time.Sleep(30 * time.Millisecond)
	require.Equal(t, int32(1), oldCount.Load(), "old generation must stop after reload")
	require.Equal(t, uint64(2), fixture.runtime.Notifications.Status()["primary"].Sent)
}

func TestNotificationsPUTRestoresPersistenceWhenReloadFails(t *testing.T) {
	fixture := newAdminFixture(t)
	receiver := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer receiver.Close()
	previous, err := fixture.store.Notifications().Get(context.Background())
	require.NoError(t, err)
	require.NoError(t, fixture.runtime.Notifications.Close())
	input := notificationConfigToDTO(notificationTestConfig(receiver.URL + "/reload-secret"))
	input.Channels[0].Webhook.URL = receiver.URL + "/reload-secret"
	status, body := fixture.request(http.MethodPut, "/api/v1/integrations/notifications", fixture.adminToken, mustJSON(t, input))
	require.Equal(t, http.StatusInternalServerError, status, body)
	require.Contains(t, body, "previous configuration restored")
	require.NotContains(t, body, "reload-secret")
	after, err := fixture.store.Notifications().Get(context.Background())
	require.NoError(t, err)
	require.Equal(t, previous, after)
}

func notificationTestConfig(url string) notify.Config {
	return notify.Config{
		Enabled: true, QueueSize: 8,
		Channels: []notify.ChannelConfig{{
			ID: "primary", Enabled: true, Kind: notify.ChannelWebhook,
			Decisions: []string{"deny"}, AllowPrivateEndpoints: true,
			Webhook: &notify.WebhookConfig{Template: notify.WebhookGeneric, URL: url},
		}},
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return string(encoded)
}
