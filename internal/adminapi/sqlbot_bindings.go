package adminapi

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/rbac"
	"github.com/cuipengdba/agentsql/internal/store"
)

func (handler *Handler) sqlbotBindingsList(w http.ResponseWriter, r *http.Request) {
	items, err := handler.deps.Runtime.Store.SQLBotBindings().List(r.Context())
	if err != nil {
		handler.internal(w, err)
		return
	}
	handler.ok(w, items)
}
func (handler *Handler) sqlbotBindingsGet(w http.ResponseWriter, r *http.Request) {
	b, err := handler.deps.Runtime.Store.SQLBotBindings().Get(r.Context(), r.PathValue("id"))
	if err != nil {
		handler.sqlbotBindingError(w, err)
		return
	}
	handler.ok(w, b)
}

// A connector credential permits a trusted SQLBot server to act for the mapped
// human. Strategy management alone must not delegate another person's identity.
func (handler *Handler) sqlbotBindingOperator(w http.ResponseWriter, r *http.Request) (rbac.Principal, bool) {
	p, ok := requestPrincipal(r)
	if !ok || !p.Has(rbac.PermissionUserManage) || !p.Has(rbac.PermissionDatasourceManage) {
		handler.fail(w, http.StatusForbidden, "SQLBot identity mapping requires user and datasource management")
		return p, false
	}
	return p, true
}

func (handler *Handler) sqlbotBindingReferences(w http.ResponseWriter, r *http.Request, b store.SQLBotBinding) bool {
	p, _ := requestPrincipal(r)
	if b.TenantID != "" && b.TenantID != p.TenantID {
		handler.fail(w, http.StatusForbidden, "SQLBot binding tenant mismatch")
		return false
	}
	agent, err := handler.deps.Runtime.Store.Agents().Get(r.Context(), b.AgentID)
	if err != nil || agent.TenantID != p.TenantID || agent.Level != "readonly" || agent.TaskGrantID != "" || b.Enabled && (agent.Status != "active" || agent.ExpiresAt != nil && !agent.ExpiresAt.After(time.Now())) {
		handler.fail(w, http.StatusUnprocessableEntity, "SQLBot binding requires a live readonly Agent in this tenant")
		return false
	}
	if b.Enabled {
		user, err := handler.rbac.Principal(r.Context(), p.TenantID, b.AgentSQLUserID)
		if err != nil || !user.Has(rbac.PermissionQueryExecute) {
			handler.fail(w, http.StatusUnprocessableEntity, "SQLBot mapped user must have query permission")
			return false
		}
	}
	return true
}

func (handler *Handler) sqlbotBindingsCreate(w http.ResponseWriter, r *http.Request) {
	p, ok := handler.sqlbotBindingOperator(w, r)
	if !ok {
		return
	}
	var input store.SQLBotBinding
	if err := decodeJSON(w, r, &input); err != nil {
		handler.fail(w, http.StatusBadRequest, "invalid SQLBot binding")
		return
	}
	if !handler.sqlbotBindingReferences(w, r, input) {
		return
	}
	b, credential, err := handler.deps.Runtime.Store.SQLBotBindings().Create(r.Context(), input, p.UserID)
	if err != nil {
		handler.sqlbotBindingError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	handler.ok(w, map[string]any{"binding": b, "connector_token": credential})
}

func (handler *Handler) sqlbotBindingsUpdate(w http.ResponseWriter, r *http.Request) {
	p, ok := handler.sqlbotBindingOperator(w, r)
	if !ok {
		return
	}
	if _, err := handler.deps.Runtime.Store.SQLBotBindings().Get(r.Context(), r.PathValue("id")); err != nil {
		handler.sqlbotBindingError(w, err)
		return
	}
	var input store.SQLBotBinding
	if err := decodeJSON(w, r, &input); err != nil || input.ID != "" && input.ID != r.PathValue("id") {
		handler.fail(w, http.StatusBadRequest, "invalid SQLBot binding")
		return
	}
	input.ID = r.PathValue("id")
	if !handler.sqlbotBindingReferences(w, r, input) {
		return
	}
	b, err := handler.deps.Runtime.Store.SQLBotBindings().Update(r.Context(), input, p.UserID)
	if err != nil {
		handler.sqlbotBindingError(w, err)
		return
	}
	handler.ok(w, b)
}

func (handler *Handler) sqlbotBindingsRotate(w http.ResponseWriter, r *http.Request) {
	p, ok := handler.sqlbotBindingOperator(w, r)
	if !ok {
		return
	}
	var input struct {
		Revision int64 `json:"revision"`
	}
	if err := decodeJSON(w, r, &input); err != nil || input.Revision < 1 {
		handler.fail(w, http.StatusBadRequest, "SQLBot binding revision is required")
		return
	}
	b, credential, err := handler.deps.Runtime.Store.SQLBotBindings().Rotate(r.Context(), r.PathValue("id"), input.Revision, p.UserID)
	if err != nil {
		handler.sqlbotBindingError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	handler.ok(w, map[string]any{"binding": b, "connector_token": credential})
}

func (handler *Handler) sqlbotBindingsDelete(w http.ResponseWriter, r *http.Request) {
	p, ok := requestPrincipal(r)
	if !ok {
		handler.fail(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var input struct {
		Revision int64 `json:"revision"`
	}
	if err := decodeJSON(w, r, &input); err != nil || input.Revision < 1 {
		handler.fail(w, http.StatusBadRequest, "SQLBot binding revision is required")
		return
	}
	if err := handler.deps.Runtime.Store.SQLBotBindings().Delete(r.Context(), r.PathValue("id"), input.Revision, p.UserID); err != nil {
		handler.sqlbotBindingError(w, err)
		return
	}
	handler.ok(w, map[string]bool{"deleted": true})
}

func sqlbotEndpointValid(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" || u.Path != "/mcp" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || strings.ContainsAny(endpoint, "\r\n") {
		return false
	}
	return u.Scheme == "https" || u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")
}

func (handler *Handler) sqlbotBindingsExport(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Endpoint string `json:"endpoint"`
	}
	if err := decodeJSON(w, r, &input); err != nil || !sqlbotEndpointValid(input.Endpoint) {
		handler.fail(w, http.StatusBadRequest, "SQLBot endpoint must be HTTPS /mcp or loopback HTTP /mcp")
		return
	}
	items, err := handler.deps.Runtime.Store.SQLBotBindings().List(r.Context())
	if err != nil {
		handler.internal(w, err)
		return
	}
	bindings := []map[string]any{}
	connectorEnvs := map[string]bool{}
	agentEnvs := map[string]string{}
	for _, b := range items {
		if b.Enabled {
			if connectorEnvs[b.ConnectorTokenEnv] || connectorEnvs[b.AgentKeyEnv] || agentEnvs[b.ConnectorTokenEnv] != "" || agentEnvs[b.AgentKeyEnv] != "" && agentEnvs[b.AgentKeyEnv] != b.AgentID {
				handler.fail(w, http.StatusConflict, "SQLBot mappings need distinct credential environment variables")
				return
			}
			connectorEnvs[b.ConnectorTokenEnv] = true
			agentEnvs[b.AgentKeyEnv] = b.AgentID
			bindings = append(bindings, map[string]any{"id": b.ID, "workspace_id": b.WorkspaceID, "datasource_name": b.DatasourceName, "sqlbot_user_id": b.SQLBotUserID, "connector_token_env": b.ConnectorTokenEnv, "agent_key_env": b.AgentKeyEnv})
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	handler.ok(w, map[string]any{"version": 2, "endpoint": input.Endpoint, "timeout_seconds": 30, "min_interval_seconds": 0.6, "bindings": bindings})
}

func (handler *Handler) sqlbotBindingsDiagnostics(w http.ResponseWriter, r *http.Request) {
	b, err := handler.deps.Runtime.Store.SQLBotBindings().Get(r.Context(), r.PathValue("id"))
	if err != nil {
		handler.sqlbotBindingError(w, err)
		return
	}
	agent, agentErr := handler.deps.Runtime.Store.Agents().Get(r.Context(), b.AgentID)
	ds, dsErr := handler.deps.Runtime.Store.Datasources().Get(r.Context(), b.DatasourceID)
	p, userErr := handler.rbac.Principal(r.Context(), b.TenantID, b.AgentSQLUserID)
	checks := []map[string]any{
		{"code": "binding_enabled", "ok": b.Enabled},
		{"code": "agent_readonly_active", "ok": agentErr == nil && agent.Status == "active" && agent.Level == "readonly" && agent.TaskGrantID == "" && (agent.ExpiresAt == nil || agent.ExpiresAt.After(time.Now()))},
		{"code": "postgres_datasource", "ok": dsErr == nil && ds.DBType == "postgres"},
		{"code": "user_query_permission", "ok": userErr == nil && p.Has(rbac.PermissionQueryExecute)},
	}
	ready := true
	for _, c := range checks {
		ready = ready && c["ok"].(bool)
	}
	handler.ok(w, map[string]any{"binding_id": b.ID, "revision": b.Revision, "configuration_ready": ready, "live_query_verified": false, "checks": checks, "supported_sqlbot_version": "1.10.2"})
}

func (handler *Handler) sqlbotBindingError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		handler.notFound(w)
	case errors.Is(err, store.ErrSQLBotBindingStale):
		handler.fail(w, http.StatusPreconditionFailed, "SQLBot binding changed; refresh before editing")
	case errors.Is(err, store.ErrSQLBotBindingInvalid):
		handler.fail(w, http.StatusUnprocessableEntity, "invalid or duplicate SQLBot mapping; verify tenant, user, readonly Agent and PostgreSQL datasource")
	default:
		handler.internal(w, err)
	}
}
