package notify

import (
	"context"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/eventbus"
	"github.com/stretchr/testify/require"
)

func TestSSRFPolicyBlocksPrivateMetadataAndReservedAddresses(t *testing.T) {
	for _, address := range []string{
		"127.0.0.1", "169.254.169.254", "10.1.2.3", "192.168.5.4", "172.16.0.1",
		"100.64.0.1", "::1", "fc00::1", "fe80::1", "0.0.0.0", "224.0.0.1",
		"192.0.2.1", "2001:db8::1", "2002:7f00:1::1", "64:ff9b::7f00:1",
	} {
		t.Run(address, func(t *testing.T) {
			require.True(t, blockedIP(netip.MustParseAddr(address), false))
		})
	}
	require.False(t, blockedIP(netip.MustParseAddr("127.0.0.1"), true))
	require.True(t, blockedIP(netip.MustParseAddr("0.0.0.0"), true))
	require.True(t, blockedIP(netip.MustParseAddr("224.0.0.1"), true))
}

func TestSSRFDefaultRejectsLocalWebhookAndExplicitOptInAllowsIt(t *testing.T) {
	server := httptest.NewServer(nil)
	defer server.Close()
	hub, err := eventbus.New(eventbus.Options{})
	require.NoError(t, err)
	defer hub.Close()

	blocked := NewManager(hub)
	err = blocked.Start(context.Background(), webhookManagerConfig(strings.Replace(server.URL, "http://", "https://", 1), WebhookGeneric, false, 4))
	require.Error(t, err)
	require.ErrorIs(t, err, errEndpointBlocked)

	allowed := NewManager(hub)
	require.NoError(t, allowed.Start(context.Background(), webhookManagerConfig(server.URL, WebhookGeneric, true, 4)))
	require.NoError(t, allowed.Close())
}
