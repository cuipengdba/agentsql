package adminapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/parser"
	"github.com/cuipengdba/agentsql/internal/rbac"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/google/uuid"
)

type taskReadGrantInput struct {
	AgentID      string    `json:"agent_id"`
	DatasourceID string    `json:"datasource_id"`
	SQL          string    `json:"sql"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type taskReadGrantView struct {
	ID           string     `json:"id"`
	TenantID     string     `json:"tenant_id"`
	IssuerUserID string     `json:"issuer_user_id"`
	AgentID      string     `json:"agent_id"`
	DatasourceID string     `json:"datasource_id"`
	ExpiresAt    time.Time  `json:"expires_at"`
	RevokedAt    *time.Time `json:"revoked_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	APIKey       string     `json:"api_key,omitempty"`
}

func taskReadGrantToView(grant model.TaskReadGrant, key string) taskReadGrantView {
	return taskReadGrantView{ID: grant.ID, TenantID: grant.TenantID,
		IssuerUserID: grant.IssuerUserID, AgentID: grant.AgentID,
		DatasourceID: grant.DatasourceID, ExpiresAt: grant.ExpiresAt,
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
	principal, ok := handler.taskReadGrantPrincipal(writer, request)
	if !ok {
		return
	}
	var input taskReadGrantInput
	if decodeJSON(writer, request, &input) != nil {
		handler.fail(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	now := time.Now().UTC()
	if strings.TrimSpace(input.AgentID) == "" || strings.TrimSpace(input.DatasourceID) == "" ||
		strings.TrimSpace(input.SQL) == "" || !input.ExpiresAt.After(now) ||
		input.ExpiresAt.After(now.Add(store.MaxTaskReadGrantDuration)) {
		handler.fail(writer, http.StatusUnprocessableEntity, "invalid task read grant")
		return
	}
	agent, err := handler.deps.Runtime.Store.Agents().Get(request.Context(), input.AgentID)
	if err != nil || agent.TenantID != principal.TenantID || agent.Status != "active" ||
		(agent.ExpiresAt != nil && !now.Before(*agent.ExpiresAt)) {
		handler.fail(writer, http.StatusUnprocessableEntity, "invalid task read grant")
		return
	}
	datasource, err := handler.deps.Runtime.Store.Datasources().Get(request.Context(), input.DatasourceID)
	if err != nil || datasource.TenantID != principal.TenantID {
		handler.fail(writer, http.StatusUnprocessableEntity, "invalid task read grant")
		return
	}
	if err := executor.ValidatePreParse(input.SQL, nil, executor.DefaultLimits); err != nil {
		handler.fail(writer, http.StatusUnprocessableEntity, "invalid task read grant")
		return
	}
	sqlParser, err := parser.NewParser(model.DBDialect(datasource.DBType))
	if err != nil {
		handler.fail(writer, http.StatusUnprocessableEntity, "invalid task read grant")
		return
	}
	ast, err := sqlParser.Parse(input.SQL)
	if err != nil || !model.TaskReadOnlySelect(input.SQL, ast) {
		handler.fail(writer, http.StatusUnprocessableEntity, "only one SELECT is allowed")
		return
	}
	key, hash, err := store.GenerateAPIKey()
	if err != nil {
		handler.internal(writer, err)
		return
	}
	digest := sha256.Sum256([]byte(input.SQL))
	grant, err := handler.deps.Runtime.Store.TaskReadGrants().Create(request.Context(), model.TaskReadGrant{
		ID: uuid.NewString(), TenantID: principal.TenantID, IssuerUserID: principal.UserID,
		AgentID: agent.ID, DatasourceID: datasource.ID, SQLSHA256: hex.EncodeToString(digest[:]),
		KeyHash: hash, ExpiresAt: input.ExpiresAt.UTC(),
	})
	if err != nil {
		if errors.Is(err, store.ErrTaskReadGrantInvalid) {
			handler.fail(writer, http.StatusUnprocessableEntity, "invalid task read grant")
		} else {
			handler.internal(writer, err)
		}
		return
	}
	if err := handler.recordTaskReadGrantAudit(request, principal, grant, "task_read_grant_create"); err != nil {
		// Never disclose a usable key when the management audit could not be
		// durably written. The client may already have disconnected, so use a
		// bounded context that retains tenant identity for compensation.
		rollbackContext, cancel := context.WithTimeout(context.WithoutCancel(request.Context()), 5*time.Second)
		_, rollbackErr := handler.deps.Runtime.Store.TaskReadGrants().Revoke(rollbackContext, grant.ID)
		cancel()
		if rollbackErr != nil {
			err = errors.Join(err, fmt.Errorf("revoke undisclosed task grant after audit failure: %w", rollbackErr))
		}
		handler.internal(writer, err)
		return
	}
	handler.ok(writer, taskReadGrantToView(grant, key))
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
	principal, ok := handler.taskReadGrantPrincipal(writer, request)
	if !ok {
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
