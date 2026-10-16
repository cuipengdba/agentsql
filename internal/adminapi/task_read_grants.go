package adminapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/rbac"
	"github.com/cuipengdba/agentsql/internal/store"
)

type taskReadGrantView struct {
	ID           string     `json:"id"`
	TenantID     string     `json:"tenant_id"`
	IssuerUserID string     `json:"issuer_user_id"`
	AgentID      string     `json:"agent_id"`
	DatasourceID string     `json:"datasource_id"`
	MaxUses      *int64     `json:"max_uses,omitempty"`
	UsedCount    int64      `json:"used_count"`
	ExpiresAt    time.Time  `json:"expires_at"`
	RevokedAt    *time.Time `json:"revoked_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	APIKey       string     `json:"api_key,omitempty"`
}

func taskReadGrantToView(grant model.TaskReadGrant, key string) taskReadGrantView {
	return taskReadGrantView{ID: grant.ID, TenantID: grant.TenantID,
		IssuerUserID: grant.IssuerUserID, AgentID: grant.AgentID,
		DatasourceID: grant.DatasourceID, MaxUses: grant.MaxUses, UsedCount: grant.UsedCount, ExpiresAt: grant.ExpiresAt,
		RevokedAt: grant.RevokedAt, CreatedAt: grant.CreatedAt, APIKey: key}
}

func (handler *Handler) taskReadGrantPrincipal(writer http.ResponseWriter, request *http.Request) (rbac.Principal, bool) {
	principal, ok := requestPrincipal(request)
	if !ok {
		handler.fail(writer, http.StatusUnauthorized, "unauthorized")
		return rbac.Principal{}, false
	}
	if !principal.Has(rbac.PermissionStrategyManage) || !principal.Has(rbac.PermissionQueryExecute) {
		handler.fail(writer, http.StatusForbidden, "forbidden")
		return rbac.Principal{}, false
	}
	return principal, true
}

func (handler *Handler) taskReadGrantsCreate(writer http.ResponseWriter, request *http.Request) {
	// Direct issuance would bypass the v0.8 separation of requester and
	// reviewer. Existing clients receive an explicit migration signal.
	handler.fail(writer, http.StatusGone, "use task-read-grant-requests approval workflow")
}

func (handler *Handler) taskReadGrantsList(writer http.ResponseWriter, request *http.Request) {
	if _, ok := handler.taskReadGrantPrincipal(writer, request); !ok {
		return
	}
	grants, err := handler.deps.Runtime.Store.TaskReadGrants().List(request.Context())
	if err != nil {
		handler.internal(writer, err)
		return
	}
	views := make([]taskReadGrantView, 0, len(grants))
	for _, grant := range grants {
		views = append(views, taskReadGrantToView(grant, ""))
	}
	handler.ok(writer, views)
}

func (handler *Handler) taskReadGrantsGet(writer http.ResponseWriter, request *http.Request) {
	if _, ok := handler.taskReadGrantPrincipal(writer, request); !ok {
		return
	}
	grant, err := handler.deps.Runtime.Store.TaskReadGrants().Get(request.Context(), request.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		handler.notFound(writer)
		return
	}
	if err != nil {
		handler.internal(writer, err)
		return
	}
	handler.ok(writer, taskReadGrantToView(grant, ""))
}

func (handler *Handler) taskReadGrantsRevoke(writer http.ResponseWriter, request *http.Request) {
	principal, ok := requestPrincipal(request)
	if !ok || !principal.Has(rbac.PermissionQueryExecute) {
		handler.fail(writer, http.StatusForbidden, "forbidden")
		return
	}
	existing, err := handler.deps.Runtime.Store.TaskReadGrants().Get(request.Context(), request.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		handler.notFound(writer)
		return
	}
	if err != nil {
		handler.internal(writer, err)
		return
	}
	if existing.IssuerUserID != principal.UserID && !principal.Has(rbac.PermissionStrategyManage) {
		handler.notFound(writer)
		return
	}
	grant, err := handler.deps.Runtime.Store.TaskReadGrants().Revoke(request.Context(), request.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		handler.notFound(writer)
		return
	}
	if err != nil {
		handler.internal(writer, err)
		return
	}
	if err := handler.recordTaskReadGrantAudit(request, principal, grant, "task_read_grant_revoke"); err != nil {
		handler.internal(writer, err)
		return
	}
	handler.ok(writer, taskReadGrantToView(grant, ""))
}

func (handler *Handler) recordTaskReadGrantAudit(request *http.Request, principal rbac.Principal, grant model.TaskReadGrant, action string) error {
	if handler.deps.Runtime.ManagementAudit == nil {
		return errors.New("management audit unavailable")
	}
	details, err := json.Marshal(struct {
		GrantID      string `json:"grant_id"`
		SQLSHA256    string `json:"sql_sha256"`
		IssuerUserID string `json:"issuer_user_id"`
	}{GrantID: grant.ID, SQLSHA256: grant.SQLSHA256, IssuerUserID: grant.IssuerUserID})
	if err != nil {
		return err
	}
	actorType, decision, encoded := "user", string(model.DecisionAllow), string(details)
	log := model.AuditLog{AgentID: &grant.AgentID, DatasourceID: &grant.DatasourceID,
		Decision: decision, Action: &action, ActorType: &actorType,
		ActorID: &principal.UserID, DetailsJSON: &encoded}
	recorded, err := handler.deps.Runtime.ManagementAudit.Record(request.Context(), log)
	if err == nil {
		handler.deps.Runtime.PublishManagementAudit(recorded)
	}
	return err
}
