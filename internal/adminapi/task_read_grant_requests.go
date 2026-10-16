package adminapi

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

type taskReadGrantRequestInput struct {
	AgentID        string    `json:"agent_id"`
	DatasourceID   string    `json:"datasource_id"`
	SQL            string    `json:"sql"`
	ExpiresAt      time.Time `json:"expires_at"`
	IdempotencyKey string    `json:"idempotency_key"`
}

type taskReadGrantRequestView struct {
	ID              string     `json:"id"`
	TenantID        string     `json:"tenant_id"`
	RequesterUserID string     `json:"requester_user_id"`
	AgentID         string     `json:"agent_id"`
	DatasourceID    string     `json:"datasource_id"`
	SQLSHA256       string     `json:"sql_sha256"`
	SQL             string     `json:"sql,omitempty"`
	ExpiresAt       time.Time  `json:"expires_at"`
	Status          string     `json:"status"`
	ReviewerUserID  *string    `json:"reviewer_user_id,omitempty"`
	ReviewedAt      *time.Time `json:"reviewed_at,omitempty"`
	IssuedAt        *time.Time `json:"issued_at,omitempty"`
	GrantID         *string    `json:"grant_id,omitempty"`
	GrantRevokedAt  *time.Time `json:"grant_revoked_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
}

func taskReadGrantRequestToView(request model.TaskReadGrantRequest) taskReadGrantRequestView {
	return taskReadGrantRequestView{
		ID: request.ID, TenantID: request.TenantID, RequesterUserID: request.RequesterUserID,
		AgentID: request.AgentID, DatasourceID: request.DatasourceID, SQLSHA256: request.SQLSHA256,
		ExpiresAt: request.ExpiresAt, Status: request.Status,
		ReviewerUserID: request.ReviewerUserID, ReviewedAt: request.ReviewedAt,
		IssuedAt: request.IssuedAt, GrantID: request.GrantID,
		GrantRevokedAt: request.GrantRevokedAt, CreatedAt: request.CreatedAt,
	}
}

func (handler *Handler) taskReadGrantRequestPrincipal(writer http.ResponseWriter, request *http.Request) (rbac.Principal, bool) {
	principal, ok := requestPrincipal(request)
	if !ok {
		handler.fail(writer, http.StatusUnauthorized, "unauthorized")
		return rbac.Principal{}, false
	}
	if !principal.Has(rbac.PermissionQueryExecute) {
		handler.fail(writer, http.StatusForbidden, "forbidden")
		return rbac.Principal{}, false
	}
	return principal, true
}

func (handler *Handler) taskReadGrantRequestsCreate(writer http.ResponseWriter, request *http.Request) {
	principal, ok := handler.taskReadGrantRequestPrincipal(writer, request)
	if !ok {
		return
	}
	var input taskReadGrantRequestInput
	if decodeJSON(writer, request, &input) != nil {
		handler.fail(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	now := time.Now().UTC()
	if strings.TrimSpace(input.AgentID) == "" || strings.TrimSpace(input.DatasourceID) == "" ||
		strings.TrimSpace(input.SQL) == "" || !input.ExpiresAt.After(now) ||
		input.ExpiresAt.After(now.Add(store.MaxTaskReadGrantDuration)) {
		handler.fail(writer, http.StatusUnprocessableEntity, "invalid task read grant request")
		return
	}
	agent, err := handler.deps.Runtime.Store.Agents().Get(request.Context(), input.AgentID)
	if err != nil || agent.TenantID != principal.TenantID || agent.Status != "active" ||
		(agent.ExpiresAt != nil && !now.Before(*agent.ExpiresAt)) {
		handler.fail(writer, http.StatusUnprocessableEntity, "invalid task read grant request")
		return
	}
	datasource, err := handler.deps.Runtime.Store.Datasources().Get(request.Context(), input.DatasourceID)
	if err != nil || datasource.TenantID != principal.TenantID {
		handler.fail(writer, http.StatusUnprocessableEntity, "invalid task read grant request")
		return
	}
	if err := executor.ValidatePreParse(input.SQL, nil, executor.DefaultLimits); err != nil {
		handler.fail(writer, http.StatusUnprocessableEntity, "invalid task read grant request")
		return
	}
	sqlParser, err := parser.NewParser(model.DBDialect(datasource.DBType))
	if err != nil {
		handler.fail(writer, http.StatusUnprocessableEntity, "invalid task read grant request")
		return
	}
	ast, err := sqlParser.Parse(input.SQL)
	if err != nil || !model.TaskReadOnlySelect(input.SQL, ast) {
		handler.fail(writer, http.StatusUnprocessableEntity, "only one SELECT is allowed")
		return
	}
	digest := sha256.Sum256([]byte(input.SQL))
	ciphertext, err := sealTaskGrantSQL(handler.tokenKey, input.SQL)
	if err != nil {
		handler.internal(writer, err)
		return
	}
	created, err := handler.deps.Runtime.Store.TaskReadGrantRequests().Create(request.Context(), model.TaskReadGrantRequest{
		ID: uuid.NewString(), TenantID: principal.TenantID, RequesterUserID: principal.UserID,
		AgentID: agent.ID, DatasourceID: datasource.ID, SQLSHA256: hex.EncodeToString(digest[:]),
		SQLCiphertext:  ciphertext,
		IdempotencyKey: input.IdempotencyKey, ExpiresAt: input.ExpiresAt.UTC(),
	})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrTaskReadGrantRequestInvalid):
			handler.fail(writer, http.StatusUnprocessableEntity, "invalid task read grant request")
		case errors.Is(err, store.ErrTaskReadGrantRequestConflict):
			handler.fail(writer, http.StatusConflict, "idempotency key already used for another request")
		default:
			handler.internal(writer, err)
		}
		return
	}
	handler.ok(writer, taskReadGrantRequestToView(created))
}

func (handler *Handler) taskReadGrantRequestsList(writer http.ResponseWriter, request *http.Request) {
	principal, ok := handler.taskReadGrantRequestPrincipal(writer, request)
	if !ok {
		return
	}
	requests, err := handler.deps.Runtime.Store.TaskReadGrantRequests().List(request.Context(), principal.UserID,
		principal.Has(rbac.PermissionStrategyManage))
	if err != nil {
		handler.internal(writer, err)
		return
	}
	views := make([]taskReadGrantRequestView, 0, len(requests))
	for _, item := range requests {
		views = append(views, taskReadGrantRequestToView(item))
	}
	handler.ok(writer, views)
}

func (handler *Handler) taskReadGrantRequestsGet(writer http.ResponseWriter, request *http.Request) {
	principal, ok := handler.taskReadGrantRequestPrincipal(writer, request)
	if !ok {
		return
	}
	item, err := handler.deps.Runtime.Store.TaskReadGrantRequests().Get(request.Context(), request.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		handler.notFound(writer)
		return
	}
	if err != nil {
		handler.internal(writer, err)
		return
	}
	if item.RequesterUserID != principal.UserID && !principal.Has(rbac.PermissionStrategyManage) {
		handler.notFound(writer)
		return
	}
	view := taskReadGrantRequestToView(item)
	if principal.Has(rbac.PermissionStrategyManage) && item.RequesterUserID != principal.UserID {
		view.SQL, err = openTaskGrantSQL(handler.tokenKey, item.SQLCiphertext)
		if err != nil {
			handler.internal(writer, err)
			return
		}
		digest := sha256.Sum256([]byte(view.SQL))
		if hex.EncodeToString(digest[:]) != item.SQLSHA256 {
			handler.internal(writer, errors.New("task grant SQL integrity mismatch"))
			return
		}
		writer.Header().Set("Cache-Control", "no-store")
	}
	handler.ok(writer, view)
}

func (handler *Handler) taskReadGrantRequestsApprove(writer http.ResponseWriter, request *http.Request) {
	handler.reviewTaskReadGrantRequest(writer, request, true)
}

func (handler *Handler) taskReadGrantRequestsReject(writer http.ResponseWriter, request *http.Request) {
	handler.reviewTaskReadGrantRequest(writer, request, false)
}

func (handler *Handler) reviewTaskReadGrantRequest(writer http.ResponseWriter, request *http.Request, approve bool) {
	principal, ok := handler.taskReadGrantRequestPrincipal(writer, request)
	if !ok {
		return
	}
	if !principal.Has(rbac.PermissionStrategyManage) {
		handler.fail(writer, http.StatusForbidden, "forbidden")
		return
	}
	if approve {
		candidate, err := handler.deps.Runtime.Store.TaskReadGrantRequests().Get(request.Context(), request.PathValue("id"))
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				handler.notFound(writer)
			} else {
				handler.internal(writer, err)
			}
			return
		}
		sqlText, err := openTaskGrantSQL(handler.tokenKey, candidate.SQLCiphertext)
		if err != nil {
			handler.internal(writer, err)
			return
		}
		digest := sha256.Sum256([]byte(sqlText))
		if hex.EncodeToString(digest[:]) != candidate.SQLSHA256 {
			handler.internal(writer, errors.New("task grant SQL integrity mismatch"))
			return
		}
	}
	item, err := handler.deps.Runtime.Store.TaskReadGrantRequests().Review(request.Context(), request.PathValue("id"), principal.UserID, approve)
	if errors.Is(err, store.ErrTaskReadGrantRequestConflict) {
		handler.fail(writer, http.StatusConflict, "request is not pending or reviewer is requester")
		return
	}
	if err != nil {
		handler.internal(writer, err)
		return
	}
	handler.ok(writer, taskReadGrantRequestToView(item))
}

func (handler *Handler) taskReadGrantRequestsIssue(writer http.ResponseWriter, request *http.Request) {
	principal, ok := handler.taskReadGrantRequestPrincipal(writer, request)
	if !ok {
		return
	}
	candidate, err := handler.deps.Runtime.Store.TaskReadGrantRequests().Get(request.Context(), request.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		handler.notFound(writer)
		return
	}
	if err != nil {
		handler.internal(writer, err)
		return
	}
	if candidate.RequesterUserID != principal.UserID {
		handler.fail(writer, http.StatusConflict, "only the requester can issue this grant")
		return
	}
	sqlText, err := openTaskGrantSQL(handler.tokenKey, candidate.SQLCiphertext)
	if err != nil {
		handler.internal(writer, err)
		return
	}
	digest := sha256.Sum256([]byte(sqlText))
	if hex.EncodeToString(digest[:]) != candidate.SQLSHA256 {
		handler.internal(writer, errors.New("task grant SQL integrity mismatch"))
		return
	}
	key, hash, err := store.GenerateAPIKey()
	if err != nil {
		handler.internal(writer, err)
		return
	}
	// Use the request's stable ID as the grant ID. Every management event
	// already carries request_id, so this also links pre-issuance decisions to
	// later execution audits without a cross-database lookup.
	grantID := candidate.ID
	item, err := handler.deps.Runtime.Store.TaskReadGrantRequests().Issue(request.Context(),
		request.PathValue("id"), principal.UserID, grantID, hash)
	if errors.Is(err, store.ErrTaskReadGrantRequestConflict) {
		handler.fail(writer, http.StatusConflict, "request is not approved, has expired, or was already issued")
		return
	}
	if err != nil {
		handler.internal(writer, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	handler.ok(writer, taskReadGrantView{
		ID: grantID, TenantID: item.TenantID, IssuerUserID: item.RequesterUserID,
		AgentID: item.AgentID, DatasourceID: item.DatasourceID,
		ExpiresAt: item.ExpiresAt, CreatedAt: *item.IssuedAt, APIKey: key,
	})
}
