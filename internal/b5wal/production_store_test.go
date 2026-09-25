package b5wal_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/b5"
	"github.com/cuipengdba/agentsql/internal/b5wal"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
)

var receiptSecret = []byte("0123456789abcdef0123456789abcdef")

func TestDurableReceiptSQLiteCrashWindowsAndRestartJoin(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "receipts.db")
	opened := openReceiptStore(t, ctx, store.MetadataOptions{Driver: store.DialectSQLite, SQLitePath: path, AutoMigrate: true})
	createReceiptParent(t, ctx, opened, "sqlite-session")
	adapter, err := b5wal.NewPGReceiptStore(opened.B5ResultReceipts())
	require.NoError(t, err)
	receipt := durableLostReceipt("sqlite-session", 1)

	// Crash after PREPARED, before SEND_STARTED.
	_, inserted, err := adapter.PutWriteOnce(ctx, receipt)
	require.NoError(t, err)
	require.True(t, inserted)
	require.NoError(t, opened.Close())

	opened = openReceiptStore(t, ctx, store.MetadataOptions{Driver: store.DialectSQLite, SQLitePath: path, AutoMigrate: false})
	adapter, err = b5wal.NewPGReceiptStore(opened.B5ResultReceipts())
	require.NoError(t, err)
	sends := 0
	err = b5wal.PersistThenSend(ctx, adapter, receipt, func(got b5wal.ResultReceipt) error {
		sends++
		require.Equal(t, b5wal.ResponseSendStarted, got.Delivery)
		return errors.New("crash after send, before SEND_COMPLETED")
	})
	require.Error(t, err)
	require.Equal(t, 1, sends)
}

func TestDurableReceiptSQLiteSendFailureAndMonotonicRecovery(t *testing.T) {
	ctx := context.Background()
	opened := openReceiptStore(t, ctx, store.MetadataOptions{Driver: store.DialectSQLite, SQLitePath: filepath.Join(t.TempDir(), "receipts.db"), AutoMigrate: true})
	createReceiptParent(t, ctx, opened, "sqlite-session-2")
	adapter, err := b5wal.NewPGReceiptStore(opened.B5ResultReceipts())
	require.NoError(t, err)
	receipt := durableLostReceipt("sqlite-session-2", 2)

	sendErr := errors.New("connection disappeared")
	err = b5wal.PersistThenSend(ctx, adapter, receipt, func(b5wal.ResultReceipt) error { return sendErr })
	require.ErrorIs(t, err, sendErr)
	stored, exists, err := adapter.Get(ctx, receipt.Key)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, b5wal.ResponseSendStarted, stored.Delivery)

	require.NoError(t, b5wal.PersistThenSend(ctx, adapter, receipt, func(got b5wal.ResultReceipt) error {
		require.Equal(t, b5wal.DurabilityLost, got.ReportedDurabilityAtResponse)
		return nil
	}))
	view, err := b5wal.RestartJoin(ctx, adapter, receipt.Key, true, true)
	require.NoError(t, err)
	require.Equal(t, b5wal.DurabilityLost, view.Receipt.ReportedDurabilityAtResponse)
	require.Equal(t, b5wal.AppendRecovered, view.Receipt.AppendConfirmation)
	require.Equal(t, b5wal.ReconciliationPrimaryDurable, view.Receipt.Reconciliation)
	missing := receipt.Key
	missing.EventUUID[0]++
	unknown, err := b5wal.RestartJoin(ctx, adapter, missing, true, true)
	require.NoError(t, err)
	require.Equal(t, b5wal.HistoricalResponseUnknown, unknown.HistoricalResponse)
}

func TestDurableReceiptPostgres14And18(t *testing.T) {
	if testing.Short() {
		t.Skip("postgres:14/18 durable receipt integration test")
	}
	for _, image := range []string{"postgres:14", "postgres:18"} {
		image := image
		t.Run(image, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			dsn := startReceiptPostgres(t, ctx, image)
			options := store.MetadataOptions{Driver: store.DialectPostgres, PostgresDSN: dsn, MaxOpenConns: 4, AutoMigrate: true}
			opened := openReceiptStore(t, ctx, options)
			createReceiptParent(t, ctx, opened, "pg-session")
			adapter, err := b5wal.NewPGReceiptStore(opened.B5ResultReceipts())
			require.NoError(t, err)
			receipt := durableLostReceipt("pg-session", 3)
			sent := 0
			require.NoError(t, b5wal.PersistThenSend(ctx, adapter, receipt, func(b5wal.ResultReceipt) error { sent++; return nil }))
			require.Equal(t, 1, sent)
			crashWindows := prepareReceiptCrashWindows(t, ctx, adapter, "pg-session")

			createRecoveryParent(t, ctx, opened, "pg-session")
			var recoveryUUID [16]byte
			recoveryUUID[0] = 33
			payload := []byte(`{"outcome":"committed"}`)
			payloadDigest := sha256.Sum256(payload)
			identity := b5wal.AppendIdentity{EventUUID: recoveryUUID, EventDigest: payloadDigest}
			identity.SegmentID[0], identity.Nonce[0], identity.ExtentDigest[0] = 1, 2, 3
			recoveryReceipt := b5wal.ResultReceipt{
				Key:                 b5wal.ReceiptKey{SessionID: "pg-session", RequestID: "recovery-request", EventUUID: recoveryUUID, AttemptGeneration: 1},
				BusinessEventDigest: payloadDigest, WALAppendReceiptDigest: b5wal.WALAppendReceiptDigest(identity),
				ReportedDurabilityAtResponse: b5wal.DurabilityLost, AppendConfirmation: b5wal.AppendTimeout,
				Reconciliation: b5wal.ReconciliationNone, Delivery: b5wal.ResponsePrepared,
			}
			require.NoError(t, b5wal.PersistThenSend(ctx, adapter, recoveryReceipt, func(b5wal.ResultReceipt) error { return nil }))
			require.NoError(t, opened.Close())

			options.AutoMigrate = false
			opened = openReceiptStore(t, ctx, options)
			adapter, err = b5wal.NewPGReceiptStore(opened.B5ResultReceipts())
			require.NoError(t, err)
			view, err := b5wal.RestartJoin(ctx, adapter, receipt.Key, true, true)
			require.NoError(t, err)
			require.Equal(t, b5wal.DurabilityLost, view.Receipt.ReportedDurabilityAtResponse)
			require.Equal(t, b5wal.AppendRecovered, view.Receipt.AppendConfirmation)
			require.Equal(t, b5wal.ReconciliationPrimaryDurable, view.Receipt.Reconciliation)
			for _, window := range crashWindows {
				stored, exists, err := adapter.Get(ctx, window.receipt.Key)
				require.NoError(t, err, window.name)
				require.Equal(t, window.exists, exists, window.name)
				if exists {
					require.Equal(t, window.delivery, stored.Delivery, window.name)
					require.Equal(t, b5wal.DurabilityLost, stored.ReportedDurabilityAtResponse, window.name)
				}
			}
			primary, err := b5wal.NewPGPrimaryStore(opened.B5TxEvents())
			require.NoError(t, err)
			reconciler := b5wal.Reconciler{Primary: primary, Receipts: adapter, Finder: adapter}
			recovered := b5wal.RecoveredRecord{
				Identity: identity,
				Event: b5wal.PrimaryEvent{
					ReplayEvent: b5wal.ReplayEvent{EventUUID: recoveryUUID, PayloadDigest: payloadDigest, TransactionID: "recovery-tx", TransactionSeq: 1},
					Kind:        b5wal.RecordTerminalOutcome, Outcome: b5wal.OutcomeCommitted, SchemaID: b5.EventSchemaID, SchemaVersion: b5.EventSchemaVersion, CanonicalEvent: payload,
				},
			}
			ledger, err := reconciler.Reconcile(ctx, []b5wal.RecoveredRecord{recovered})
			require.NoError(t, err)
			require.Len(t, ledger, 1)
			require.Equal(t, b5wal.RecoveryCommittedAuditPending, ledger[0].Classification)
			require.Equal(t, b5wal.DurabilityLost, ledger[0].ReportedDurability)
			ledger, err = reconciler.Reconcile(ctx, []b5wal.RecoveredRecord{recovered})
			require.NoError(t, err, "restart reconciliation is idempotent against primary snapshot")
			require.Len(t, ledger, 1)
			conflict := recovered.Event
			conflict.PayloadDigest = sha256.Sum256([]byte("different"))
			_, err = primary.Put(ctx, conflict)
			require.ErrorIs(t, err, b5wal.ErrEventUUIDConflict)
		})
	}
}

type receiptCrashWindow struct {
	name     string
	receipt  b5wal.ResultReceipt
	exists   bool
	delivery b5wal.ResponseDeliveryState
}

func prepareReceiptCrashWindows(t *testing.T, ctx context.Context, adapter *b5wal.PGReceiptStore, sessionID string) []receiptCrashWindow {
	t.Helper()
	before := durableLostReceipt(sessionID, 40)
	prepared := durableLostReceipt(sessionID, 41)
	_, _, err := adapter.PutWriteOnce(ctx, prepared)
	require.NoError(t, err)
	started := durableLostReceipt(sessionID, 42)
	_, _, err = adapter.PutWriteOnce(ctx, started)
	require.NoError(t, err)
	deliveryStarted := b5wal.ResponseSendStarted
	_, err = adapter.Advance(ctx, started.Key, b5wal.ReceiptAdvance{Delivery: &deliveryStarted})
	require.NoError(t, err)
	afterSend := durableLostReceipt(sessionID, 43)
	sendErr := errors.New("crash after terminal send")
	err = b5wal.PersistThenSend(ctx, adapter, afterSend, func(b5wal.ResultReceipt) error { return sendErr })
	require.ErrorIs(t, err, sendErr)
	completed := durableLostReceipt(sessionID, 44)
	require.NoError(t, b5wal.PersistThenSend(ctx, adapter, completed, func(b5wal.ResultReceipt) error { return nil }))
	return []receiptCrashWindow{
		{name: "before metadata", receipt: before, exists: false},
		{name: "after prepared", receipt: prepared, exists: true, delivery: b5wal.ResponsePrepared},
		{name: "after send started", receipt: started, exists: true, delivery: b5wal.ResponseSendStarted},
		{name: "after send before completion", receipt: afterSend, exists: true, delivery: b5wal.ResponseSendStarted},
		{name: "after completion", receipt: completed, exists: true, delivery: b5wal.ResponseSendCompleted},
	}
}

func createRecoveryParent(t *testing.T, ctx context.Context, opened *store.Store, sessionID string) {
	t.Helper()
	_, err := opened.Datasources().Create(ctx, model.Datasource{
		ID: "recovery-ds", Name: "recovery", DBType: "postgres", Host: "localhost", Port: 5432,
		Database: "db", Username: "user", ConnLimit: 2, StmtTimeoutMS: 1000, RowLimit: 100,
	}, "password")
	require.NoError(t, err)
	now := time.Now().UTC()
	_, err = opened.B5Transactions().Create(ctx, store.B5Transaction{
		TransactionID: "recovery-tx", SessionID: sessionID, DatasourceID: "recovery-ds",
		Status: b5.TransactionPending, Phase: b5.PhaseReady, PlanDigest: bytes(32, 9), OwnerEpoch: 1,
		IdleDeadline: now.Add(time.Minute), WallDeadline: now.Add(2 * time.Minute),
	})
	require.NoError(t, err)
}

func openReceiptStore(t *testing.T, ctx context.Context, options store.MetadataOptions) *store.Store {
	t.Helper()
	opened, err := store.OpenMetadata(ctx, options, receiptSecret)
	require.NoError(t, err)
	t.Cleanup(func() { _ = opened.Close() })
	return opened
}

func createReceiptParent(t *testing.T, ctx context.Context, opened *store.Store, sessionID string) {
	t.Helper()
	_, hash, err := store.GenerateAPIKey()
	require.NoError(t, err)
	_, err = opened.Agents().Create(ctx, model.Agent{ID: "receipt-agent", Name: "receipt", Status: "active", APIKeyHash: hash, Level: "readonly"})
	require.NoError(t, err)
	now := time.Now().UTC()
	_, err = opened.B5Sessions().Create(ctx, store.B5Session{
		SessionID: sessionID, AgentID: "receipt-agent", TenantID: "tenant", PrincipalID: "principal",
		OwnerInstanceID: "owner", OwnerEpoch: 1, ContinuationSchemaID: "agentsql.b5.continuation.v2", ContinuationSchemaVersion: 2,
		ContinuationKeyCiphertext: "kms", ContinuationHMACDigest: bytes(32, 1), StickyRoute: "owner", Status: b5.SessionReady,
		IdleExpiresAt: now.Add(time.Hour), AbsoluteExpiresAt: now.Add(2 * time.Hour),
	})
	require.NoError(t, err)
}

func durableLostReceipt(session string, marker byte) b5wal.ResultReceipt {
	var uuid [16]byte
	uuid[0] = marker
	var eventDigest, appendDigest [32]byte
	eventDigest[0], appendDigest[0] = marker, marker+1
	return b5wal.ResultReceipt{
		Key:                 b5wal.ReceiptKey{SessionID: session, RequestID: fmt.Sprintf("request-%d", marker), EventUUID: uuid, AttemptGeneration: 1},
		BusinessEventDigest: eventDigest, WALAppendReceiptDigest: appendDigest,
		ReportedDurabilityAtResponse: b5wal.DurabilityLost, AppendConfirmation: b5wal.AppendTimeout,
		Reconciliation: b5wal.ReconciliationNone, Delivery: b5wal.ResponsePrepared,
	}
}

func bytes(length int, value byte) []byte {
	result := make([]byte, length)
	for index := range result {
		result[index] = value
	}
	return result
}

func startReceiptPostgres(t *testing.T, ctx context.Context, image string) string {
	t.Helper()
	container, err := postgrescontainer.Run(ctx, image,
		postgrescontainer.WithDatabase("agentsql_b5wal"), postgrescontainer.WithUsername("agentsql"),
		postgrescontainer.WithPassword("receipt-password"), postgrescontainer.BasicWaitStrategies())
	require.NoError(t, err)
	testcontainers.CleanupContainer(t, container)
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)
	return fmt.Sprintf("postgres://agentsql:receipt-password@%s:%s/agentsql_b5wal?sslmode=disable", host, port.Port())
}
