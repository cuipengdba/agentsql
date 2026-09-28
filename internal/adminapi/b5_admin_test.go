package adminapi

import (
	"context"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/audit"
	"github.com/cuipengdba/agentsql/internal/b5"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestB5AdminFeatureOffResponsesAreStableAndEmpty(t *testing.T) {
	fixture := newAdminFixture(t)
	tests := []struct {
		path, contains string
	}{
		{"/api/v1/b5/status", `"enabled":false`},
		{"/api/v1/b5/sessions?page=1&page_size=10", `"list":[]`},
		{"/api/v1/b5/transactions?page=1&page_size=10", `"list":[]`},
		{"/api/v1/b5/quarantine?page=1&page_size=10", `"list":[]`},
		{"/api/v1/b5/inventory", `"children":[]`},
		{"/api/v1/b5/metrics", `"phase_durations":[]`},
		{"/api/v1/b5/reconciliation?page=1&page_size=10", `"list":[]`},
		{"/api/v1/b5/sessions/session-missing", `"data":null`},
		{"/api/v1/b5/transactions/tx-missing", `"data":null`},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			status, body := fixture.request(http.MethodGet, test.path, fixture.adminToken, "")
			require.Equal(t, http.StatusOK, status, body)
			require.Contains(t, body, test.contains)
		})
	}

	for _, test := range []struct{ path, body string }{
		{"/api/v1/b5/reconciliation", `{}`},
		{"/api/v1/b5/quarantine/lease-1/confirm-discard", `{}`},
	} {
		status, body := fixture.request(http.MethodPost, test.path, fixture.adminToken, test.body)
		require.Equal(t, http.StatusOK, status, body)
		require.Contains(t, body, `"available":false`)
	}
}

func TestB5AdminEnabledReadAndOperationSurface(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	backend := &fakeB5Admin{
		status:         B5StatusView{State: "ACTIVE", Reason: "B5_READY", Ready: true},
		sessions:       B5SessionPage{Total: 1, List: []B5SessionView{{ID: "session-1", Status: "ACTIVE", OwnerInstanceID: "node-a", OwnerEpoch: 7, IdleExpiresAt: now.Add(time.Minute), AbsoluteExpiresAt: now.Add(time.Hour)}}},
		transactions:   B5TransactionPage{Total: 1, List: []B5TransactionView{{ID: "tx-1", SessionID: "session-1", DatasourceID: "ds-1", Status: "ACTIVE", Phase: "ACTIVE", PlanDigest: "abcd", WallDeadline: now.Add(time.Minute), Events: []B5EventView{}}}},
		quarantine:     B5QuarantinePage{Total: 1, List: []B5QuarantineView{{LeaseID: "lease-1", Reason: "DISCARD_UNCONFIRMED", ChargedSlots: 1}}},
		metrics:        B5MetricsView{Sessions: 1, Transactions: 1, Unknown: 1, DiscardUnconfirmed: 1, QuarantineCount: 1, QuarantineChargedSlots: 1, HardBudget: 100, QuarantineBudgetRatio: .01},
		reconciliation: B5ReconciliationPage{Total: 1, List: []B5ReconciliationView{{EventUUID: "event-1", TransactionID: "tx-1", Classification: "COMMITTED_AUDIT_PENDING"}}},
	}
	fixture := newB5AdminFixture(t, backend)

	tests := []struct{ path, contains string }{
		{"/api/v1/b5/status", `"state":"ACTIVE"`},
		{"/api/v1/b5/sessions?page=2&page_size=5&status=ACTIVE&owner=node-a", `"id":"session-1"`},
		{"/api/v1/b5/transactions?page=1&page_size=10&phase=ACTIVE&datasource_id=ds-1", `"id":"tx-1"`},
		{"/api/v1/b5/quarantine?page=1&page_size=10", `"lease_id":"lease-1"`},
		{"/api/v1/b5/metrics", `"quarantine_budget_ratio":0.01`},
		{"/api/v1/b5/reconciliation?page=1&page_size=10", `"classification":"COMMITTED_AUDIT_PENDING"`},
	}
	for _, test := range tests {
		status, body := fixture.request(http.MethodGet, test.path, fixture.adminToken, "")
		require.Equal(t, http.StatusOK, status, body)
		require.Contains(t, body, test.contains)
	}
	require.Equal(t, B5ListFilter{Page: 2, PageSize: 5, Status: "ACTIVE", Owner: "node-a"}, backend.lastSessionFilter)
	require.Equal(t, B5ListFilter{Page: 1, PageSize: 10, Phase: "ACTIVE", DatasourceID: "ds-1"}, backend.lastTransactionFilter)

	status, body := fixture.request(http.MethodPost, "/api/v1/b5/reconciliation", fixture.adminToken, `{"transaction_id":"tx-1","confirm":true}`)
	require.Equal(t, http.StatusOK, status, body)
	status, body = fixture.request(http.MethodPost, "/api/v1/b5/quarantine/lease-1/confirm-discard", fixture.adminToken, `{"confirm":true}`)
	require.Equal(t, http.StatusOK, status, body)
	require.Equal(t, 1, backend.reconcileCalls)
	require.Equal(t, 1, backend.discardCalls)

	logs, err := fixture.store.AuditLogs().Page(context.Background(), 1, 20)
	require.NoError(t, err)
	actions := map[string]bool{}
	for _, log := range logs.List {
		if log.Action != nil {
			actions[*log.Action] = true
		}
	}
	require.True(t, actions[audit.ActionB5Reconcile])
	require.True(t, actions[audit.ActionB5Discard])
}

func TestB5AdminOperationsRequireExplicitConfirmation(t *testing.T) {
	backend := &fakeB5Admin{}
	fixture := newB5AdminFixture(t, backend)
	for _, path := range []string{"/api/v1/b5/reconciliation", "/api/v1/b5/quarantine/lease-1/confirm-discard"} {
		status, _ := fixture.request(http.MethodPost, path, fixture.adminToken, `{"confirm":false}`)
		require.Equal(t, http.StatusUnprocessableEntity, status)
	}
	require.Zero(t, backend.reconcileCalls)
	require.Zero(t, backend.discardCalls)
}

func TestStoreB5AdminReadsPersistedSessionsTransactionsAndDigestChain(t *testing.T) {
	fixture := newAdminFixture(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	_, err := fixture.store.B5Sessions().Create(context.Background(), store.B5Session{
		SessionID: "session-store", AgentID: fixture.agent.ID, TenantID: "tenant-a", PrincipalID: "principal-a",
		OwnerInstanceID: "node-a", OwnerEpoch: 3, ContinuationSchemaID: b5.ContinuationProofSchemaID,
		ContinuationSchemaVersion: b5.ContinuationProofVersion, ContinuationKeyCiphertext: "secret-ciphertext",
		ContinuationHMACDigest: make([]byte, 32), StickyRoute: "node-a", Status: b5.SessionActive,
		IdleExpiresAt: now.Add(time.Minute), AbsoluteExpiresAt: now.Add(time.Hour),
	})
	require.NoError(t, err)
	approval := "approval-1"
	_, err = fixture.store.B5Transactions().Create(context.Background(), store.B5Transaction{
		TransactionID: "tx-store", SessionID: "session-store", DatasourceID: fixture.datasource.ID,
		Status: b5.TransactionActive, Phase: b5.PhaseActive, PlanDigest: bytesOfAdmin(32, 0x11), ApprovalID: &approval,
		OwnerEpoch: 3, IdleDeadline: now.Add(time.Minute), WallDeadline: now.Add(time.Hour),
		ConnectionGeneration: 4, LeaseGeneration: 5, TransactionSeq: 1, PreviousTxEventDigest: bytesOfAdmin(32, 0x22),
	})
	require.NoError(t, err)
	require.NoError(t, fixture.store.B5TxEvents().Append(context.Background(), store.B5TxEvent{
		TransactionID: "tx-store", TransactionSeq: 1, EventUUID: bytesOfAdmin(16, 0x33), EventType: "tx_begin",
		EventSchemaID: "agentsql.b5.event.v4", EventSchemaVersion: 4, PreviousTxEventDigest: bytesOfAdmin(32, 0x22),
		EventDigest: bytesOfAdmin(32, 0x44), CanonicalEvent: []byte("private-canonical-event"),
	}))

	backend, err := NewStoreB5Admin(fixture.store, nil)
	require.NoError(t, err)
	sessions, err := backend.ListSessions(context.Background(), B5ListFilter{Page: 1, PageSize: 10, Status: "ACTIVE", Owner: "node-a", Query: "store"})
	require.NoError(t, err)
	require.Equal(t, int64(1), sessions.Total)
	require.Equal(t, "session-store", sessions.List[0].ID)
	transaction, err := backend.GetTransaction(context.Background(), "tx-store")
	require.NoError(t, err)
	require.Equal(t, "tx-store", transaction.ID)
	require.Equal(t, "1111111111111111111111111111111111111111111111111111111111111111", transaction.PlanDigest)
	require.Len(t, transaction.Events, 1)
	require.Equal(t, "tx_begin", transaction.Events[0].Type)
	encoded := mustJSON(t, transaction)
	require.NotContains(t, encoded, "secret-ciphertext")
	require.NotContains(t, encoded, "private-canonical-event")
	require.NotContains(t, encoded, "backend_secret_digest")
}

func TestStoreB5AdminUsesProductionReadinessProvider(t *testing.T) {
	fixture := newAdminFixture(t)
	backend, err := NewStoreB5AdminWithStatus(fixture.store, func(context.Context) (B5StatusView, error) {
		return B5StatusView{Enabled: true, State: "READY", Reason: "B5_READY", Ready: true}, nil
	})
	require.NoError(t, err)
	status, err := backend.Status(context.Background())
	require.NoError(t, err)
	require.Equal(t, B5StatusView{Enabled: true, State: "READY", Reason: "B5_READY", Ready: true}, status)
}

func newB5AdminFixture(t *testing.T, backend B5AdminBackend) *adminFixture {
	t.Helper()
	fixture := newAdminFixture(t)
	handler, err := NewHandler(Deps{Runtime: fixture.runtime, Config: adminTestConfig(filepath.Join(t.TempDir(), "unused.db")), AdminUsername: "admin", AdminPassword: "password", TokenKey: DeriveTokenKey([]byte(adminTestSecret)), DatasourcePinger: fixture.pinger, B5Admin: backend}, zerolog.New(fixture.logs))
	require.NoError(t, err)
	fixture.handler = handler
	return fixture
}

type fakeB5Admin struct {
	mu                    sync.Mutex
	status                B5StatusView
	sessions              B5SessionPage
	transactions          B5TransactionPage
	quarantine            B5QuarantinePage
	metrics               B5MetricsView
	reconciliation        B5ReconciliationPage
	lastSessionFilter     B5ListFilter
	lastTransactionFilter B5ListFilter
	reconcileCalls        int
	discardCalls          int
}

func (f *fakeB5Admin) Status(context.Context) (B5StatusView, error) { return f.status, nil }
func (f *fakeB5Admin) ListSessions(_ context.Context, filter B5ListFilter) (B5SessionPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastSessionFilter = filter
	return f.sessions, nil
}
func (f *fakeB5Admin) GetSession(context.Context, string) (*B5SessionView, error) {
	if len(f.sessions.List) == 0 {
		return nil, nil
	}
	return &f.sessions.List[0], nil
}
func (f *fakeB5Admin) ListTransactions(_ context.Context, filter B5ListFilter) (B5TransactionPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastTransactionFilter = filter
	return f.transactions, nil
}
func (f *fakeB5Admin) GetTransaction(context.Context, string) (*B5TransactionView, error) {
	if len(f.transactions.List) == 0 {
		return nil, nil
	}
	return &f.transactions.List[0], nil
}
func (f *fakeB5Admin) ListQuarantine(context.Context, B5ListFilter) (B5QuarantinePage, error) {
	return f.quarantine, nil
}
func (f *fakeB5Admin) Inventory(context.Context, string) (B5InventoryView, error) {
	return B5InventoryView{Children: []B5InventoryChildView{}}, nil
}
func (f *fakeB5Admin) Metrics(context.Context, string) (B5MetricsView, error) { return f.metrics, nil }
func (f *fakeB5Admin) ListReconciliation(context.Context, B5ListFilter) (B5ReconciliationPage, error) {
	return f.reconciliation, nil
}
func (f *fakeB5Admin) ConfirmDiscard(context.Context, string) (B5QuarantineView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.discardCalls++
	return B5QuarantineView{LeaseID: "lease-1"}, nil
}
func (f *fakeB5Admin) Reconcile(context.Context, B5ReconcileInput) ([]B5ReconciliationView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reconcileCalls++
	return f.reconciliation.List, nil
}

var _ B5AdminBackend = (*fakeB5Admin)(nil)

func bytesOfAdmin(size int, value byte) []byte {
	result := make([]byte, size)
	for index := range result {
		result[index] = value
	}
	return result
}
