package notify

import (
	"bufio"
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/eventbus"
	"github.com/stretchr/testify/require"
)

func TestSyslogUDPEmitsRFC5424WithDecisionSeverity(t *testing.T) {
	packetConnection, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	defer packetConnection.Close()
	host, portText, err := net.SplitHostPort(packetConnection.LocalAddr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(portText)
	require.NoError(t, err)

	hub := newHub(t)
	manager := NewManager(hub, WithRetryPolicy(0, time.Millisecond, time.Millisecond))
	config := Config{Enabled: true, QueueSize: 4, Channels: []ChannelConfig{{
		ID: "syslog", Enabled: true, Kind: ChannelSyslog, AllowPrivateEndpoints: true,
		Syslog: &SyslogConfig{Host: host, Port: port, Transport: "udp", Facility: 16},
	}}}
	require.NoError(t, manager.Start(context.Background(), config))
	defer manager.Close()
	hub.Publish(eventbus.Event{Audit: sensitiveAudit()})

	require.NoError(t, packetConnection.SetReadDeadline(time.Now().Add(time.Second)))
	buffer := make([]byte, 8192)
	count, _, err := packetConnection.ReadFrom(buffer)
	require.NoError(t, err)
	message := string(buffer[:count])
	require.True(t, strings.HasPrefix(message, "<131>1 "), "facility local0 + err severity")
	require.Contains(t, message, " - AgentSQL - audit [agentsql@32473 product=\"AgentSQL\" audit_id=\"42\"] ")
	require.Contains(t, message, `"decision":"deny"`)
	require.Contains(t, message, `"rule_ids":["R001","R002"]`)
	assertNoSensitiveText(t, message)
	require.Eventually(t, func() bool { return manager.Status()["syslog"].Sent == 1 }, time.Second, time.Millisecond)
}

func TestSyslogTCPAndSeverityMapping(t *testing.T) {
	for decision, expected := range map[string]int{"deny": 3, "error": 3, "warn": 4, "approve": 5, "allow": 6, "other": 6} {
		require.Equal(t, expected, severity(decision), decision)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	host, portText, err := net.SplitHostPort(listener.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(portText)
	require.NoError(t, err)
	messages := make(chan string, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		line, _ := bufio.NewReader(connection).ReadString('\n')
		messages <- line
	}()

	hub := newHub(t)
	manager := NewManager(hub, WithRetryPolicy(0, time.Millisecond, time.Millisecond))
	config := Config{Enabled: true, QueueSize: 4, Channels: []ChannelConfig{{
		ID: "syslog-tcp", Enabled: true, Kind: ChannelSyslog, AllowPrivateEndpoints: true,
		Decisions: []string{"warn"},
		Syslog:    &SyslogConfig{Host: host, Port: port, Transport: "tcp", Facility: 1},
	}}}
	require.NoError(t, manager.Start(context.Background(), config))
	defer manager.Close()
	audit := sensitiveAudit()
	audit.Decision = "warn"
	hub.Publish(eventbus.Event{Audit: audit})
	select {
	case message := <-messages:
		require.True(t, strings.HasPrefix(message, "<12>1 "), "facility user + warning severity")
		require.True(t, strings.HasSuffix(message, "\n"))
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for TCP syslog")
	}
}
