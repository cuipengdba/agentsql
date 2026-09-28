package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/audit"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
)

// B5AdminBackend is the narrow S9 boundary. Read methods cannot obtain a raw
// business executor. Mutation methods are intentionally separate and are
// called only after the HTTP layer has durably recorded an administrator
// intent. A nil backend keeps the explicit rollback/feature-off surface.
type B5AdminBackend interface {
	Status(context.Context) (B5StatusView, error)
	ListSessions(context.Context, B5ListFilter) (B5SessionPage, error)
	GetSession(context.Context, string) (*B5SessionView, error)
	ListTransactions(context.Context, B5ListFilter) (B5TransactionPage, error)
	GetTransaction(context.Context, string) (*B5TransactionView, error)
	ListQuarantine(context.Context, B5ListFilter) (B5QuarantinePage, error)
	Inventory(context.Context, string) (B5InventoryView, error)
	Metrics(context.Context, string) (B5MetricsView, error)
	ListReconciliation(context.Context, B5ListFilter) (B5ReconciliationPage, error)
	ConfirmDiscard(context.Context, string) (B5QuarantineView, error)
	Reconcile(context.Context, B5ReconcileInput) ([]B5ReconciliationView, error)
}

type B5ListFilter struct {
	Page, PageSize int
	Status         string
	Phase          string
	DatasourceID   string
	Owner          string
	Query          string
}

type B5StatusView struct {
	Enabled bool   `json:"enabled"`
	State   string `json:"state"`
	Reason  string `json:"reason"`
	Ready   bool   `json:"ready"`
}

type B5SessionView struct {
	ID                string    `json:"id"`
	AgentID           string    `json:"agent_id"`
	TenantID          string    `json:"tenant_id"`
	PrincipalID       string    `json:"principal_id"`
	Status            string    `json:"status"`
	OwnerInstanceID   string    `json:"owner_instance_id"`
	OwnerEpoch        uint64    `json:"owner_epoch"`
	StickyRoute       string    `json:"sticky_route"`
	IdleExpiresAt     time.Time `json:"idle_expires_at"`
	AbsoluteExpiresAt time.Time `json:"absolute_expires_at"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type B5SessionPage struct {
	Total int64           `json:"total"`
	List  []B5SessionView `json:"list"`
}

type B5EventView struct {
	Sequence       uint64    `json:"sequence"`
	Type           string    `json:"type"`
	SchemaID       string    `json:"schema_id"`
	SchemaVersion  uint16    `json:"schema_version"`
	PreviousDigest string    `json:"previous_digest"`
	Digest         string    `json:"digest"`
	CreatedAt      time.Time `json:"created_at"`
}

type B5TransactionView struct {
	ID                   string        `json:"id"`
	SessionID            string        `json:"session_id"`
	DatasourceID         string        `json:"datasource_id"`
	Status               string        `json:"status"`
	Phase                string        `json:"phase"`
	PlanDigest           string        `json:"plan_digest"`
	ApprovalID           *string       `json:"approval_id,omitempty"`
	OwnerEpoch           uint64        `json:"owner_epoch"`
	IdleDeadline         time.Time     `json:"idle_deadline"`
	WallDeadline         time.Time     `json:"wall_deadline"`
	StatementDeadline    *time.Time    `json:"statement_deadline,omitempty"`
	BackendPID           *int          `json:"backend_pid,omitempty"`
	BackendStartedAt     *time.Time    `json:"backend_started_at,omitempty"`
	ConnectionGeneration uint64        `json:"connection_generation"`
	LeaseGeneration      uint64        `json:"lease_generation"`
	StatementCount       int           `json:"statement_count"`
	Sequence             uint64        `json:"sequence"`
	PreviousEventDigest  string        `json:"previous_event_digest"`
	CreatedAt            time.Time     `json:"created_at"`
	UpdatedAt            time.Time     `json:"updated_at"`
	Events               []B5EventView `json:"events"`
}

type B5TransactionPage struct {
	Total int64               `json:"total"`
	List  []B5TransactionView `json:"list"`
}

type B5BackendIdentityView struct {
	ServerID       string     `json:"server_id"`
	Database       string     `json:"database"`
	PID            int32      `json:"pid"`
	BackendStarted *time.Time `json:"backend_started_at,omitempty"`
	DialPermitID   string     `json:"dial_permit_id"`
}

type B5QuarantineView struct {
	LeaseID        string                `json:"lease_id"`
	ClaimID        string                `json:"claim_id"`
	DatasourceID   string                `json:"datasource_id"`
	Reason         string                `json:"reason"`
	AgeSeconds     int64                 `json:"age_seconds"`
	ChargedSlots   int                   `json:"charged_slots"`
	Generation     uint64                `json:"generation"`
	InventoryEpoch uint64                `json:"inventory_epoch"`
	Backend        B5BackendIdentityView `json:"backend"`
}

type B5QuarantinePage struct {
	Total int64              `json:"total"`
	List  []B5QuarantineView `json:"list"`
}

type B5InventoryChildView struct {
	LeaseID       string                `json:"lease_id"`
	ClaimID       string                `json:"claim_id"`
	DatasourceID  string                `json:"datasource_id"`
	State         string                `json:"state"`
	Charged       bool                  `json:"charged"`
	DialAttempted bool                  `json:"dial_attempted"`
	Backend       B5BackendIdentityView `json:"backend"`
}

type B5InventoryView struct {
	DialPermits int                    `json:"dial_permits"`
	Children    []B5InventoryChildView `json:"children"`
}

type B5DurationMetric struct {
	Phase string  `json:"phase"`
	P50MS float64 `json:"p50_ms"`
	P95MS float64 `json:"p95_ms"`
	P99MS float64 `json:"p99_ms"`
}

type B5StateCount struct {
	State string `json:"state"`
	Count int64  `json:"count"`
}

type B5MetricsView struct {
	Sessions               int64              `json:"sessions"`
	Transactions           int64              `json:"transactions"`
	Unknown                int64              `json:"unknown"`
	DiscardUnconfirmed     int64              `json:"discard_unconfirmed"`
	QuarantineCount        int64              `json:"quarantine_count"`
	QuarantineChargedSlots int64              `json:"quarantine_charged_slots"`
	HardBudget             int64              `json:"hard_budget"`
	QuarantineBudgetRatio  float64            `json:"quarantine_budget_ratio"`
	SessionStates          []B5StateCount     `json:"session_states"`
	TransactionStates      []B5StateCount     `json:"transaction_states"`
	PhaseDurations         []B5DurationMetric `json:"phase_durations"`
}

type B5ReconciliationView struct {
	EventUUID          string    `json:"event_uuid"`
	TransactionID      string    `json:"transaction_id"`
	Sequence           uint64    `json:"sequence"`
	Classification     string    `json:"classification"`
	ReportedDurability string    `json:"reported_durability"`
	AppendConfirmation string    `json:"append_confirmation"`
	Reconciliation     string    `json:"reconciliation"`
	Replayed           bool      `json:"replayed"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type B5ReconciliationPage struct {
	Total int64                  `json:"total"`
	List  []B5ReconciliationView `json:"list"`
}

type B5ReconcileInput struct {
	TransactionID string `json:"transaction_id,omitempty"`
	DatasourceID  string `json:"datasource_id,omitempty"`
	Confirm       bool   `json:"confirm"`
}

type b5ConfirmInput struct {
	Confirm bool `json:"confirm"`
}

func b5OffStatus() B5StatusView {
	return B5StatusView{Enabled: false, State: "FEATURE_OFF", Reason: "B5_SESSIONS_FEATURE_OFF", Ready: true}
}

func (handler *Handler) b5Enabled() bool { return handler.deps.B5Admin != nil }

func (handler *Handler) b5Status(writer http.ResponseWriter, request *http.Request) {
	if !handler.b5Enabled() {
		handler.ok(writer, b5OffStatus())
		return
	}
	value, err := handler.deps.B5Admin.Status(request.Context())
	if err != nil {
		handler.internal(writer, err)
		return
	}
	value.Enabled = true
	handler.ok(writer, value)
}

func b5Filter(request *http.Request) (B5ListFilter, error) {
	page, size, err := parsePage(request)
	if err != nil {
		return B5ListFilter{}, err
	}
	query := request.URL.Query()
	filter := B5ListFilter{Page: page, PageSize: size, Status: strings.TrimSpace(query.Get("status")), Phase: strings.TrimSpace(query.Get("phase")), DatasourceID: strings.TrimSpace(query.Get("datasource_id")), Owner: strings.TrimSpace(query.Get("owner")), Query: strings.TrimSpace(query.Get("q"))}
	for _, value := range []string{filter.Status, filter.Phase, filter.DatasourceID, filter.Owner, filter.Query} {
		if len(value) > 256 {
			return B5ListFilter{}, errors.New("filter value is too long")
		}
	}
	return filter, nil
}

func emptyB5Page(page, size int) pageResponse {
	return pageResponse{Total: 0, Page: page, PageSize: size, List: []any{}}
}

func (handler *Handler) b5SessionsList(writer http.ResponseWriter, request *http.Request) {
	filter, err := b5Filter(request)
	if err != nil {
		handler.fail(writer, http.StatusBadRequest, err.Error())
		return
	}
	if !handler.b5Enabled() {
		handler.ok(writer, emptyB5Page(filter.Page, filter.PageSize))
		return
	}
	result, err := handler.deps.B5Admin.ListSessions(request.Context(), filter)
	if err != nil {
		handler.internal(writer, err)
		return
	}
	if result.List == nil {
		result.List = []B5SessionView{}
	}
	handler.ok(writer, pageResponse{Total: result.Total, Page: filter.Page, PageSize: filter.PageSize, List: result.List})
}

func (handler *Handler) b5SessionsGet(writer http.ResponseWriter, request *http.Request) {
	if !handler.b5Enabled() {
		handler.ok(writer, nil)
		return
	}
	value, err := handler.deps.B5Admin.GetSession(request.Context(), request.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		handler.notFound(writer)
		return
	}
	if err != nil {
		handler.internal(writer, err)
		return
	}
	handler.ok(writer, value)
}

func (handler *Handler) b5TransactionsList(writer http.ResponseWriter, request *http.Request) {
	filter, err := b5Filter(request)
	if err != nil {
		handler.fail(writer, http.StatusBadRequest, err.Error())
		return
	}
	if !handler.b5Enabled() {
		handler.ok(writer, emptyB5Page(filter.Page, filter.PageSize))
		return
	}
	result, err := handler.deps.B5Admin.ListTransactions(request.Context(), filter)
	if err != nil {
		handler.internal(writer, err)
		return
	}
	if result.List == nil {
		result.List = []B5TransactionView{}
	}
	handler.ok(writer, pageResponse{Total: result.Total, Page: filter.Page, PageSize: filter.PageSize, List: result.List})
}

func (handler *Handler) b5TransactionsGet(writer http.ResponseWriter, request *http.Request) {
	if !handler.b5Enabled() {
		handler.ok(writer, nil)
		return
	}
	value, err := handler.deps.B5Admin.GetTransaction(request.Context(), request.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		handler.notFound(writer)
		return
	}
	if err != nil {
		handler.internal(writer, err)
		return
	}
	handler.ok(writer, value)
}

func (handler *Handler) b5QuarantineList(writer http.ResponseWriter, request *http.Request) {
	filter, err := b5Filter(request)
	if err != nil {
		handler.fail(writer, http.StatusBadRequest, err.Error())
		return
	}
	if !handler.b5Enabled() {
		handler.ok(writer, emptyB5Page(filter.Page, filter.PageSize))
		return
	}
	result, err := handler.deps.B5Admin.ListQuarantine(request.Context(), filter)
	if err != nil {
		handler.internal(writer, err)
		return
	}
	if result.List == nil {
		result.List = []B5QuarantineView{}
	}
	handler.ok(writer, pageResponse{Total: result.Total, Page: filter.Page, PageSize: filter.PageSize, List: result.List})
}

func (handler *Handler) b5Inventory(writer http.ResponseWriter, request *http.Request) {
	if !handler.b5Enabled() {
		handler.ok(writer, B5InventoryView{Children: []B5InventoryChildView{}})
		return
	}
	value, err := handler.deps.B5Admin.Inventory(request.Context(), strings.TrimSpace(request.URL.Query().Get("datasource_id")))
	if err != nil {
		handler.internal(writer, err)
		return
	}
	if value.Children == nil {
		value.Children = []B5InventoryChildView{}
	}
	handler.ok(writer, value)
}

func (handler *Handler) b5Metrics(writer http.ResponseWriter, request *http.Request) {
	if !handler.b5Enabled() {
		handler.ok(writer, B5MetricsView{SessionStates: []B5StateCount{}, TransactionStates: []B5StateCount{}, PhaseDurations: []B5DurationMetric{}})
		return
	}
	value, err := handler.deps.B5Admin.Metrics(request.Context(), strings.TrimSpace(request.URL.Query().Get("datasource_id")))
	if err != nil {
		handler.internal(writer, err)
		return
	}
	if value.SessionStates == nil {
		value.SessionStates = []B5StateCount{}
	}
	if value.TransactionStates == nil {
		value.TransactionStates = []B5StateCount{}
	}
	if value.PhaseDurations == nil {
		value.PhaseDurations = []B5DurationMetric{}
	}
	handler.ok(writer, value)
}

func (handler *Handler) b5ReconciliationList(writer http.ResponseWriter, request *http.Request) {
	filter, err := b5Filter(request)
	if err != nil {
		handler.fail(writer, http.StatusBadRequest, err.Error())
		return
	}
	if !handler.b5Enabled() {
		handler.ok(writer, emptyB5Page(filter.Page, filter.PageSize))
		return
	}
	result, err := handler.deps.B5Admin.ListReconciliation(request.Context(), filter)
	if err != nil {
		handler.internal(writer, err)
		return
	}
	if result.List == nil {
		result.List = []B5ReconciliationView{}
	}
	handler.ok(writer, pageResponse{Total: result.Total, Page: filter.Page, PageSize: filter.PageSize, List: result.List})
}

func (handler *Handler) b5ConfirmDiscard(writer http.ResponseWriter, request *http.Request) {
	if !handler.b5Enabled() {
		handler.ok(writer, struct {
			Available bool `json:"available"`
		}{false})
		return
	}
	var input b5ConfirmInput
	if err := decodeJSON(writer, request, &input); err != nil || !input.Confirm {
		handler.fail(writer, http.StatusUnprocessableEntity, "confirm must be true")
		return
	}
	id := request.PathValue("id")
	if err := handler.recordB5Operation(request, audit.ActionB5Discard, map[string]string{"lease_id": id}); err != nil {
		handler.internal(writer, err)
		return
	}
	value, err := handler.deps.B5Admin.ConfirmDiscard(request.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		handler.notFound(writer)
		return
	}
	if err != nil {
		handler.internal(writer, err)
		return
	}
	handler.ok(writer, value)
}

func (handler *Handler) b5Reconcile(writer http.ResponseWriter, request *http.Request) {
	if !handler.b5Enabled() {
		handler.ok(writer, struct {
			Available bool `json:"available"`
		}{false})
		return
	}
	var input B5ReconcileInput
	if err := decodeJSON(writer, request, &input); err != nil || !input.Confirm {
		handler.fail(writer, http.StatusUnprocessableEntity, "confirm must be true")
		return
	}
	if err := handler.recordB5Operation(request, audit.ActionB5Reconcile, map[string]string{"transaction_id": input.TransactionID, "datasource_id": input.DatasourceID}); err != nil {
		handler.internal(writer, err)
		return
	}
	values, err := handler.deps.B5Admin.Reconcile(request.Context(), input)
	if err != nil {
		handler.internal(writer, err)
		return
	}
	if values == nil {
		values = []B5ReconciliationView{}
	}
	handler.ok(writer, values)
}

func (handler *Handler) recordB5Operation(request *http.Request, action string, details map[string]string) error {
	if handler.deps.Runtime == nil || handler.deps.Runtime.ManagementAudit == nil {
		return errors.New("management audit unavailable")
	}
	encoded, err := json.Marshal(details)
	if err != nil {
		return err
	}
	actorType, actorID, decision, text := "admin", handler.adminUser, "allow", string(encoded)
	log := model.AuditLog{Decision: decision, Action: &action, ActorType: &actorType, ActorID: &actorID, ClientIP: auditPeerIP(request.RemoteAddr), DetailsJSON: &text}
	_, err = handler.deps.Runtime.ManagementAudit.Record(request.Context(), log)
	return err
}
