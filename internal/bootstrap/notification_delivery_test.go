package bootstrap

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/audit"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/notify"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/stretchr/testify/require"
)

func TestReliableNotificationRetriesAndResumesAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delivery.db")
	var fail atomic.Bool
	fail.Store(true)
	received := make(chan notify.Payload, 4)
	receiver := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if fail.Load() {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		var payload notify.Payload
		if json.NewDecoder(request.Body).Decode(&payload) != nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		received <- payload
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer receiver.Close()
	channel := notify.ChannelConfig{
		ID: "durable", Enabled: true, Reliable: true, Kind: notify.ChannelWebhook,
		Decisions: []string{"deny"}, AllowPrivateEndpoints: true,
		Webhook: &notify.WebhookConfig{Template: notify.WebhookGeneric, URL: receiver.URL},
	}
	seed, err := store.OpenWithSecret(context.Background(), path, bootstrapTestSecret)
	require.NoError(t, err)
	digest, err := notify.ChannelDigest(channel)
	require.NoError(t, err)
	require.NoError(t, seed.NotificationDelivery().Ensure(context.Background(), channel.ID, digest, 0))
	require.NoError(t, seed.Notifications().Replace(context.Background(), notify.Config{
		Enabled: true, QueueSize: 4, Channels: []notify.ChannelConfig{channel},
	}))
	recorded, err := audit.NewRecorder(seed.AuditLogs()).Record(context.Background(), model.AuditLog{
		TS: time.Now().UTC(), Decision: string(model.DecisionDeny),
	})
	require.NoError(t, err)
	require.NotZero(t, recorded.ID)
	require.NoError(t, seed.Close())

	runtime, err := Assemble(context.Background(), bootstrapTestConfig(path), bootstrapTestSecret)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		status, statusErr := runtime.Store.NotificationDelivery().Status(context.Background(), channel.ID)
		return statusErr == nil && status.Failures >= 1 && status.LastSequence == 0
	}, 5*time.Second, 25*time.Millisecond)
	require.NoError(t, runtime.Close())

	fail.Store(false)
	runtime, err = Assemble(context.Background(), bootstrapTestConfig(path), bootstrapTestSecret)
	require.NoError(t, err)
	defer func() { require.NoError(t, runtime.Close()) }()
	var payload notify.Payload
	select {
	case payload = <-received:
	case <-time.After(7 * time.Second):
		t.Fatal("reliable webhook did not resume after restart")
	}
	wantID, err := notify.DeliveryID(store.DefaultTenantID, channel.ID, 1)
	require.NoError(t, err)
	require.Equal(t, wantID, payload.DeliveryID)
	require.Equal(t, "deny", payload.Decision)
	require.Eventually(t, func() bool {
		status, statusErr := runtime.Store.NotificationDelivery().Status(context.Background(), channel.ID)
		return statusErr == nil && status.LastSequence == 1 && status.Failures == 0
	}, 3*time.Second, 25*time.Millisecond)
}
