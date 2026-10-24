package bootstrap

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/audit"
	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/notify"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestReliableNotificationThreeGatewaysPostgres18(t *testing.T) {
	if testing.Short() {
		t.Skip("three-gateway PostgreSQL notification delivery requires Docker")
	}
	ctx := bootstrapDockerTestContext(t)
	const username, password, databaseName = "agentsql", "synthetic-reliable-password", "agentsql"
	container, err := postgrescontainer.Run(ctx, "postgres:18",
		postgrescontainer.WithDatabase(databaseName),
		postgrescontainer.WithUsername(username),
		postgrescontainer.WithPassword(password),
		postgrescontainer.BasicWaitStrategies(),
	)
	if err != nil {
		if container != nil {
			testcontainers.CleanupContainer(t, container)
		}
		require.NoError(t, err)
	}
	testcontainers.CleanupContainer(t, container)
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)
	dsn := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", username, password, host, port.Port(), databaseName)
	var mu sync.Mutex
	var received []string
	receiver := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload notify.Payload
		if json.NewDecoder(request.Body).Decode(&payload) != nil || payload.DeliveryID == "" {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		received = append(received, payload.DeliveryID)
		mu.Unlock()
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer receiver.Close()
	cfg := bootstrapTestConfig("")
	cfg.Store.Metadata = &config.MetadataStoreConfig{Driver: "postgres", DSN: dsn, MaxOpenConns: 5, MaxIdleConns: 2}
	first, err := Assemble(ctx, cfg, bootstrapTestSecret)
	require.NoError(t, err)
	defer first.Close()
	channel := notify.ChannelConfig{ID: "shared-hook", Enabled: true, Reliable: true, Kind: notify.ChannelWebhook,
		Decisions: []string{"deny"}, AllowPrivateEndpoints: true,
		Webhook: &notify.WebhookConfig{Template: notify.WebhookGeneric, URL: receiver.URL}}
	digest, err := notify.ChannelDigest(channel)
	require.NoError(t, err)
	_, err = first.Store.Notifications().ReplaceWithCursors(ctx,
		notify.Config{Enabled: true, QueueSize: 4, Channels: []notify.ChannelConfig{channel}},
		[]store.NotificationCursorSeed{{ChannelID: channel.ID, ConfigDigest: digest}},
	)
	require.NoError(t, err)
	second, err := Assemble(ctx, cfg, bootstrapTestSecret)
	require.NoError(t, err)
	defer second.Close()
	third, err := Assemble(ctx, cfg, bootstrapTestSecret)
	require.NoError(t, err)
	defer third.Close()
	record := func() {
		_, recordErr := audit.NewRecorder(first.Store.AuditLogs()).Record(ctx,
			model.AuditLog{TS: time.Now().UTC(), Decision: string(model.DecisionDeny)})
		require.NoError(t, recordErr)
	}
	record()
	require.Eventually(t, func() bool {
		status, statusErr := first.Store.NotificationDelivery().Status(ctx, channel.ID)
		mu.Lock()
		count := len(received)
		mu.Unlock()
		return statusErr == nil && status.LastSequence == 1 && count == 1
	}, 15*time.Second, 50*time.Millisecond)
	// Remove one gateway and confirm the remaining independent workers advance
	// the same durable cursor without a second delivery of the first event.
	require.NoError(t, first.Close())
	_, err = audit.NewRecorder(second.Store.AuditLogs()).Record(ctx,
		model.AuditLog{TS: time.Now().UTC(), Decision: string(model.DecisionDeny)})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		status, statusErr := second.Store.NotificationDelivery().Status(ctx, channel.ID)
		mu.Lock()
		count := len(received)
		mu.Unlock()
		return statusErr == nil && status.LastSequence == 2 && count == 2
	}, 15*time.Second, 50*time.Millisecond)
	wantFirst, err := notify.DeliveryID(store.DefaultTenantID, channel.ID, 1)
	require.NoError(t, err)
	wantSecond, err := notify.DeliveryID(store.DefaultTenantID, channel.ID, 2)
	require.NoError(t, err)
	mu.Lock()
	require.Equal(t, []string{wantFirst, wantSecond}, received)
	mu.Unlock()
	// All three runtimes used the same shared PostgreSQL metadata database.
	connection, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	require.NoError(t, connection.PingContext(ctx))
	require.NoError(t, connection.Close())
}
