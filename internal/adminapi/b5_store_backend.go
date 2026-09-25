package adminapi

import (
	"context"
	"encoding/hex"
	"errors"

	"github.com/cuipengdba/agentsql/internal/store"
)

var ErrB5OperationsUnavailable = errors.New("b5 operations runtime is unavailable")

// B5RuntimePlane supplies the live S3/S7 observations and controlled actions
// which cannot be inferred safely from session metadata. Implementations own
// backend inventory and WAL scanners; StoreB5Admin never receives a business
// connection or guesses backend absence from local socket state.
type B5RuntimePlane interface {
	Status(context.Context) (B5StatusView, error)
	ListQuarantine(context.Context, B5ListFilter) (B5QuarantinePage, error)
	Inventory(context.Context, string) (B5InventoryView, error)
	Metrics(context.Context, string) (B5MetricsView, error)
	ListReconciliation(context.Context, B5ListFilter) (B5ReconciliationPage, error)
	ConfirmDiscard(context.Context, string) (B5QuarantineView, error)
	Reconcile(context.Context, B5ReconcileInput) ([]B5ReconciliationView, error)
}

// StoreB5Admin is the production S9 read model. Constructing it does not
// activate B5; the caller must still explicitly place it in Deps.B5Admin.
type StoreB5Admin struct {
	store *store.Store
	live  B5RuntimePlane
}

func NewStoreB5Admin(metadata *store.Store, live B5RuntimePlane) (*StoreB5Admin, error) {
	if metadata == nil {
		return nil, errors.New("b5 admin metadata store is required")
	}
	return &StoreB5Admin{store: metadata, live: live}, nil
}

func (admin *StoreB5Admin) Status(ctx context.Context) (B5StatusView, error) {
	if admin.live == nil {
		return B5StatusView{Enabled: true, State: "DEGRADED", Reason: "B5_OPERATIONS_RUNTIME_UNAVAILABLE", Ready: false}, nil
	}
	return admin.live.Status(ctx)
}

func (admin *StoreB5Admin) ListSessions(ctx context.Context, filter B5ListFilter) (B5SessionPage, error) {
	page, err := admin.store.B5Sessions().ListPage(ctx, store.B5SessionFilter{Status: filter.Status, Owner: filter.Owner, Query: filter.Query}, filter.Page, filter.PageSize)
	if err != nil {
		return B5SessionPage{}, err
	}
	views := make([]B5SessionView, 0, len(page.List))
	for _, value := range page.List {
		views = append(views, b5SessionView(value))
	}
	return B5SessionPage{Total: page.Total, List: views}, nil
}

func (admin *StoreB5Admin) GetSession(ctx context.Context, id string) (*B5SessionView, error) {
	value, err := admin.store.B5Sessions().Get(ctx, id)
	if err != nil {
		return nil, err
	}
	view := b5SessionView(value)
	return &view, nil
}

func b5SessionView(value store.B5Session) B5SessionView {
	return B5SessionView{ID: value.SessionID, AgentID: value.AgentID, TenantID: value.TenantID, PrincipalID: value.PrincipalID, Status: string(value.Status), OwnerInstanceID: value.OwnerInstanceID, OwnerEpoch: value.OwnerEpoch, StickyRoute: value.StickyRoute, IdleExpiresAt: value.IdleExpiresAt, AbsoluteExpiresAt: value.AbsoluteExpiresAt, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}
}

func (admin *StoreB5Admin) ListTransactions(ctx context.Context, filter B5ListFilter) (B5TransactionPage, error) {
	page, err := admin.store.B5Transactions().ListPage(ctx, store.B5TransactionFilter{Status: filter.Status, Phase: filter.Phase, DatasourceID: filter.DatasourceID, Query: filter.Query}, filter.Page, filter.PageSize)
	if err != nil {
		return B5TransactionPage{}, err
	}
	views := make([]B5TransactionView, 0, len(page.List))
	for _, value := range page.List {
		views = append(views, b5TransactionView(value, []B5EventView{}))
	}
	return B5TransactionPage{Total: page.Total, List: views}, nil
}

func (admin *StoreB5Admin) GetTransaction(ctx context.Context, id string) (*B5TransactionView, error) {
	value, err := admin.store.B5Transactions().Get(ctx, id)
	if err != nil {
		return nil, err
	}
	events, err := admin.store.B5TxEvents().ListByTransaction(ctx, id, 10000)
	if err != nil {
		return nil, err
	}
	views := make([]B5EventView, 0, len(events))
	for _, event := range events {
		views = append(views, B5EventView{Sequence: event.TransactionSeq, Type: event.EventType, SchemaID: event.EventSchemaID, SchemaVersion: event.EventSchemaVersion, PreviousDigest: hex.EncodeToString(event.PreviousTxEventDigest), Digest: hex.EncodeToString(event.EventDigest), CreatedAt: event.CreatedAt})
	}
	view := b5TransactionView(value, views)
	return &view, nil
}

func b5TransactionView(value store.B5Transaction, events []B5EventView) B5TransactionView {
	return B5TransactionView{ID: value.TransactionID, SessionID: value.SessionID, DatasourceID: value.DatasourceID, Status: string(value.Status), Phase: string(value.Phase), PlanDigest: hex.EncodeToString(value.PlanDigest), ApprovalID: value.ApprovalID, OwnerEpoch: value.OwnerEpoch, IdleDeadline: value.IdleDeadline, WallDeadline: value.WallDeadline, StatementDeadline: value.StatementDeadline, BackendPID: value.BackendPID, BackendStartedAt: value.BackendStartedAt, ConnectionGeneration: value.ConnectionGeneration, LeaseGeneration: value.LeaseGeneration, StatementCount: value.StatementCount, Sequence: value.TransactionSeq, PreviousEventDigest: hex.EncodeToString(value.PreviousTxEventDigest), CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt, Events: events}
}

func (admin *StoreB5Admin) Metrics(ctx context.Context, datasource string) (B5MetricsView, error) {
	value := B5MetricsView{SessionStates: []B5StateCount{}, TransactionStates: []B5StateCount{}, PhaseDurations: []B5DurationMetric{}}
	sessions, err := admin.store.B5Sessions().ListPage(ctx, store.B5SessionFilter{}, 1, 1)
	if err != nil {
		return value, err
	}
	transactions, err := admin.store.B5Transactions().ListPage(ctx, store.B5TransactionFilter{DatasourceID: datasource}, 1, 1)
	if err != nil {
		return value, err
	}
	value.Sessions, value.Transactions = sessions.Total, transactions.Total
	if admin.live == nil {
		return value, nil
	}
	live, err := admin.live.Metrics(ctx, datasource)
	if err != nil {
		return value, err
	}
	live.Sessions, live.Transactions = value.Sessions, value.Transactions
	return live, nil
}

func (admin *StoreB5Admin) ListQuarantine(ctx context.Context, filter B5ListFilter) (B5QuarantinePage, error) {
	if admin.live == nil {
		return B5QuarantinePage{List: []B5QuarantineView{}}, nil
	}
	return admin.live.ListQuarantine(ctx, filter)
}
func (admin *StoreB5Admin) Inventory(ctx context.Context, datasource string) (B5InventoryView, error) {
	if admin.live == nil {
		return B5InventoryView{Children: []B5InventoryChildView{}}, nil
	}
	return admin.live.Inventory(ctx, datasource)
}
func (admin *StoreB5Admin) ListReconciliation(ctx context.Context, filter B5ListFilter) (B5ReconciliationPage, error) {
	if admin.live == nil {
		return B5ReconciliationPage{List: []B5ReconciliationView{}}, nil
	}
	return admin.live.ListReconciliation(ctx, filter)
}
func (admin *StoreB5Admin) ConfirmDiscard(ctx context.Context, id string) (B5QuarantineView, error) {
	if admin.live == nil {
		return B5QuarantineView{}, ErrB5OperationsUnavailable
	}
	return admin.live.ConfirmDiscard(ctx, id)
}
func (admin *StoreB5Admin) Reconcile(ctx context.Context, input B5ReconcileInput) ([]B5ReconciliationView, error) {
	if admin.live == nil {
		return nil, ErrB5OperationsUnavailable
	}
	return admin.live.Reconcile(ctx, input)
}

var _ B5AdminBackend = (*StoreB5Admin)(nil)
