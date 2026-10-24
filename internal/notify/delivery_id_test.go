package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDeliveryIDIsStableAndScoped(t *testing.T) {
	first, err := DeliveryID("tenant-a", "channel-a", 7)
	require.NoError(t, err)
	repeat, err := DeliveryID("tenant-a", "channel-a", 7)
	require.NoError(t, err)
	require.Equal(t, first, repeat)
	require.Len(t, first, 64)
	for _, other := range [][3]string{{"tenant-b", "channel-a", "7"}, {"tenant-a", "channel-b", "7"}, {"tenant-a", "channel-a", "8"}} {
		sequence := int64(7)
		if other[2] == "8" {
			sequence = 8
		}
		value, otherErr := DeliveryID(other[0], other[1], sequence)
		require.NoError(t, otherErr)
		require.NotEqual(t, first, value)
	}
	_, err = DeliveryID("", "channel-a", 7)
	require.Error(t, err)
	_, err = DeliveryID("tenant-a", "channel-a", 0)
	require.Error(t, err)
}

func TestWebhookDeliveryIDOverridesConfiguredHeader(t *testing.T) {
	var received string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		received = request.Header.Get("X-AgentSQL-Delivery-ID")
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	config := webhookManagerConfig(server.URL, WebhookGeneric, true, 4).Channels[0]
	config.Webhook.Headers = map[string]string{"X-AgentSQL-Delivery-ID": "operator-value"}
	sender, err := newWebhookSender(context.Background(), config, defaultManagerOptions())
	require.NoError(t, err)
	defer sender.Close()
	id, err := DeliveryID("tenant-a", "channel-a", 7)
	require.NoError(t, err)
	require.NoError(t, sender.Send(context.Background(), Payload{DeliveryID: id}))
	require.Equal(t, id, received)
}
