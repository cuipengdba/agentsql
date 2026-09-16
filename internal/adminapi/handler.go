package adminapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/bootstrap"
	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/policy"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/rs/zerolog"
)

type Deps struct {
	Runtime          *bootstrap.Runtime
	Config           config.Config
	AdminUsername    string
	AdminPassword    string
	TokenKey         []byte
	DatasourcePinger DatasourcePinger
}

type DatasourcePinger interface {
	Ping(ctx context.Context, datasource model.Datasource) (PingResult, error)
}

type PingResult struct {
	OK        bool  `json:"ok"`
	LatencyMS int64 `json:"latency_ms"`
}

type runtimePinger struct{ runtime *bootstrap.Runtime }

func (pinger runtimePinger) Ping(ctx context.Context, datasource model.Datasource) (PingResult, error) {
	started := time.Now()
	executor, err := pinger.runtime.ExecutorFor(datasource)
	if err != nil {
		return PingResult{}, err
	}
	_, err = executor.Query(ctx, "SELECT 1", 1)
	return PingResult{OK: err == nil, LatencyMS: time.Since(started).Milliseconds()}, err
}

type Handler struct {
	deps          Deps
	logger        zerolog.Logger
	mux           *http.ServeMux
	adminUser     string
	adminPassword string
	tokenKey      []byte
}

func NewHandler(deps Deps, logger zerolog.Logger) (http.Handler, error) {
	if deps.Runtime == nil || deps.Runtime.Store == nil {
		return nil, fmt.Errorf("create admin handler: runtime is incomplete")
	}
	if err := deps.Config.Validate(); err != nil {
		return nil, fmt.Errorf("create admin handler: invalid configuration: %w", err)
	}
	if strings.TrimSpace(deps.AdminUsername) == "" {
		deps.AdminUsername = "admin"
	}
	if deps.AdminPassword == "" {
		return nil, fmt.Errorf("create admin handler: administrator password is required")
	}
	if len(deps.TokenKey) == 0 {
		return nil, fmt.Errorf("create admin handler: token key is required")
	}
	if deps.DatasourcePinger == nil {
		deps.DatasourcePinger = runtimePinger{runtime: deps.Runtime}
	}
	handler := &Handler{deps: deps, logger: logger, adminUser: deps.AdminUsername,
		adminPassword: deps.AdminPassword, tokenKey: append([]byte(nil), deps.TokenKey...)}
	mux := http.NewServeMux()
	handler.mux = mux
	mux.HandleFunc("POST /api/v1/auth/login", handler.login)
	mux.HandleFunc("GET /api/v1/auth/me", handler.me)
	mux.HandleFunc("POST /api/v1/auth/logout", handler.logout)
	mux.HandleFunc("GET /api/v1/agents", handler.agentsList)
	mux.HandleFunc("POST /api/v1/agents", handler.agentsCreate)
	mux.HandleFunc("GET /api/v1/agents/{id}", handler.agentsGet)
	mux.HandleFunc("PUT /api/v1/agents/{id}", handler.agentsUpdate)
	mux.HandleFunc("DELETE /api/v1/agents/{id}", handler.agentsDelete)
	mux.HandleFunc("POST /api/v1/agents/{id}/rotate-key", handler.agentsRotate)
	mux.HandleFunc("GET /api/v1/datasources", handler.datasourcesList)
	mux.HandleFunc("POST /api/v1/datasources", handler.datasourcesCreate)
	mux.HandleFunc("GET /api/v1/datasources/{id}", handler.datasourcesGet)
	mux.HandleFunc("PUT /api/v1/datasources/{id}", handler.datasourcesUpdate)
	mux.HandleFunc("DELETE /api/v1/datasources/{id}", handler.datasourcesDelete)
	mux.HandleFunc("POST /api/v1/datasources/{id}/ping", handler.datasourcesPing)
	mux.HandleFunc("GET /api/v1/policies", handler.policiesList)
	mux.HandleFunc("POST /api/v1/policies", handler.policiesCreate)
	mux.HandleFunc("PUT /api/v1/policies/{id}", handler.policiesUpdate)
	mux.HandleFunc("DELETE /api/v1/policies/{id}", handler.policiesDelete)
	mux.HandleFunc("GET /api/v1/rules", handler.rulesList)
	mux.HandleFunc("POST /api/v1/rules", handler.rulesCreate)
	mux.HandleFunc("PUT /api/v1/rules/{id}", handler.rulesUpdate)
	mux.HandleFunc("DELETE /api/v1/rules/{id}", handler.rulesDelete)
	mux.HandleFunc("GET /api/v1/mask_rules", handler.maskRulesList)
	mux.HandleFunc("POST /api/v1/mask_rules", handler.maskRulesCreate)
	mux.HandleFunc("PUT /api/v1/mask_rules/{id}", handler.maskRulesUpdate)
	mux.HandleFunc("DELETE /api/v1/mask_rules/{id}", handler.maskRulesDelete)
	mux.HandleFunc("GET /api/v1/audit", handler.auditList)
	mux.HandleFunc("GET /api/v1/audit/export", handler.auditExport)
	mux.HandleFunc("GET /api/v1/approvals", handler.approvalsList)
	mux.HandleFunc("POST /api/v1/approvals/{id}/decide", handler.approvalsDecide)
	mux.HandleFunc("GET /api/v1/dashboard/summary", handler.dashboardSummary)
	mux.HandleFunc("POST /api/v1/playground/assess", handler.playgroundAssess)
	return handler.recover(handler.adminAuth(mux)), nil
}

func (handler *Handler) adminAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost && request.URL.Path == "/api/v1/auth/login" {
			next.ServeHTTP(writer, request)
			return
		}
		token, ok := adminBearerToken(request.Header.Get("Authorization"))
		if !ok || validateAdminToken(handler.tokenKey, token, time.Now()) != nil {
			handler.fail(writer, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func (handler *Handler) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		started := time.Now()
		defer func() {
			if recovered := recover(); recovered != nil {
				handler.logger.Error().Bytes("stack", debug.Stack()).Str("panic_type", fmt.Sprintf("%T", recovered)).Msg("admin API panic recovered")
				// A panic may occur after headers; the contract still avoids exposing
				// stack details to the client.
				handler.fail(writer, http.StatusInternalServerError, "internal error")
			}
			handler.logger.Info().Str("method", request.Method).Str("path", request.URL.Path).
				Str("admin_user", handler.adminUser).
				Int64("latency_ms", time.Since(started).Milliseconds()).Msg("admin API request")
		}()
		next.ServeHTTP(writer, request)
	})
}

func (handler *Handler) login(writer http.ResponseWriter, request *http.Request) {
	var input struct{ Username, Password string }
	if err := decodeJSON(writer, request, &input); err != nil {
		handler.fail(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	usernameMatch := subtle.ConstantTimeCompare([]byte(input.Username), []byte(handler.adminUser))
	passwordMatch := subtle.ConstantTimeCompare([]byte(input.Password), []byte(handler.adminPassword))
	if usernameMatch != 1 || passwordMatch != 1 {
		handler.fail(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	jtiBytes := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, jtiBytes); err != nil {
		handler.fail(writer, http.StatusInternalServerError, "internal error")
		return
	}
	token, expires, err := issueAdminToken(handler.tokenKey, time.Now(), hex.EncodeToString(jtiBytes))
	if err != nil {
		handler.fail(writer, http.StatusInternalServerError, "internal error")
		return
	}
	handler.ok(writer, map[string]any{"token": token, "expires_at": expires.Format(time.RFC3339)})
}

func (handler *Handler) me(writer http.ResponseWriter, _ *http.Request) {
	handler.ok(writer, map[string]string{"username": handler.adminUser})
}

func (handler *Handler) logout(writer http.ResponseWriter, _ *http.Request) {
	handler.ok(writer, map[string]bool{"ok": true})
}

func (handler *Handler) agentsList(writer http.ResponseWriter, request *http.Request) {
	agents, err := handler.deps.Runtime.Store.Agents().List(request.Context())
	if err != nil {
		handler.internal(writer, err)
		return
	}
	page, size, err := parsePage(request)
	if err != nil {
		handler.fail(writer, http.StatusBadRequest, err.Error())
		return
	}
	views := make([]agentView, 0, len(agents))
	for _, agent := range agents {
		views = append(views, agentToView(agent, ""))
	}
	handler.ok(writer, paginate(views, page, size))
}

// validAgentLevel mirrors mcpserver.validAgentLevel. The level vocabulary is a
// cross-package domain invariant; v0.1 keeps a local copy to avoid widening the
// refactor, and may lift it into model once a third caller appears.
func validAgentLevel(level string) bool {
	switch level {
	case "readonly", "dml", "ddl":
		return true
	default:
		return false
	}
}

func (handler *Handler) agentsCreate(writer http.ResponseWriter, request *http.Request) {
	var input agentCreateInput
	if err := decodeJSON(writer, request, &input); err != nil {
		handler.fail(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(input.ID) == "" || strings.TrimSpace(input.Name) == "" || !validAgentLevel(input.Level) {
		if !validAgentLevel(input.Level) {
			handler.fail(writer, http.StatusUnprocessableEntity, "invalid agent level")
			return
		}
		handler.fail(writer, http.StatusUnprocessableEntity, "id and name are required")
		return
	}
	status := input.Status
	if status == "" {
		status = "active"
	}
	plain, hash, err := store.GenerateAPIKey()
	if err != nil {
		handler.internal(writer, err)
		return
	}
	agent, err := handler.deps.Runtime.Store.Agents().Create(request.Context(), model.Agent{ID: input.ID, Name: input.Name, Owner: input.Owner, Status: status, APIKeyHash: hash, Level: input.Level, ExpiresAt: input.ExpiresAt})
	if err != nil {
		handler.fail(writer, http.StatusConflict, "agent already exists or is invalid")
		return
	}
	handler.ok(writer, agentToView(agent, plain))
}

func (handler *Handler) agentsGet(writer http.ResponseWriter, request *http.Request) {
	agent, err := handler.deps.Runtime.Store.Agents().Get(request.Context(), request.PathValue("id"))
	if err != nil {
		handler.notFound(writer)
		return
	}
	handler.ok(writer, agentToView(agent, ""))
}

func (handler *Handler) agentsUpdate(writer http.ResponseWriter, request *http.Request) {
	agent, err := handler.deps.Runtime.Store.Agents().Get(request.Context(), request.PathValue("id"))
	if err != nil {
		handler.notFound(writer)
		return
	}
	var input agentUpdateInput
	if err := decodeJSON(writer, request, &input); err != nil {
		handler.fail(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	if input.Name != nil {
		agent.Name = *input.Name
	}
	if input.Owner != nil {
		agent.Owner = input.Owner
	}
	if input.Status != nil {
		agent.Status = *input.Status
	}
	if input.Level != nil {
		if !validAgentLevel(*input.Level) {
			handler.fail(writer, http.StatusUnprocessableEntity, "invalid agent level")
			return
		}
		agent.Level = *input.Level
	}
	if input.ExpiresAt != nil {
		agent.ExpiresAt = input.ExpiresAt
	}
	updated, err := handler.deps.Runtime.Store.Agents().Update(request.Context(), agent)
	if err != nil {
		handler.internal(writer, err)
		return
	}
	handler.ok(writer, agentToView(updated, ""))
}

func (handler *Handler) agentsDelete(writer http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	if _, err := handler.deps.Runtime.Store.Agents().Get(request.Context(), id); err != nil {
		handler.notFound(writer)
		return
	}
	policies, err := handler.deps.Runtime.Store.Policies().ListByAgent(request.Context(), id)
	if err != nil {
		handler.internal(writer, err)
		return
	}
	if len(policies) > 0 {
		handler.fail(writer, http.StatusConflict, "agent has policies; remove them first")
		return
	}
	if err := handler.deps.Runtime.Store.Agents().Delete(request.Context(), id); err != nil {
		handler.internal(writer, err)
		return
	}
	handler.ok(writer, map[string]bool{"deleted": true})
}

func (handler *Handler) agentsRotate(writer http.ResponseWriter, request *http.Request) {
	agent, err := handler.deps.Runtime.Store.Agents().Get(request.Context(), request.PathValue("id"))
	if err != nil {
		handler.notFound(writer)
		return
	}
	plain, hash, err := store.GenerateAPIKey()
	if err != nil {
		handler.internal(writer, err)
		return
	}
	agent.APIKeyHash = hash
	updated, err := handler.deps.Runtime.Store.Agents().Update(request.Context(), agent)
	if err != nil {
		handler.internal(writer, err)
		return
	}
	handler.ok(writer, agentToView(updated, plain))
}

func (handler *Handler) datasourcesList(writer http.ResponseWriter, request *http.Request) {
	datasources, err := handler.deps.Runtime.Store.Datasources().List(request.Context())
	if err != nil {
		handler.internal(writer, err)
		return
	}
	page, size, err := parsePage(request)
	if err != nil {
		handler.fail(writer, http.StatusBadRequest, err.Error())
		return
	}
	views := make([]datasourceView, 0, len(datasources))
	for _, datasource := range datasources {
		views = append(views, datasourceToView(datasource))
	}
	handler.ok(writer, paginate(views, page, size))
}

func (handler *Handler) datasourcesCreate(writer http.ResponseWriter, request *http.Request) {
	var input datasourceInput
	if err := decodeJSON(writer, request, &input); err != nil {
		handler.fail(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	if input.Password == "" {
		handler.fail(writer, http.StatusUnprocessableEntity, "password is required")
		return
	}
	datasource := model.Datasource{ID: input.ID, Name: input.Name, DBType: input.DBType, Host: input.Host, Port: input.Port, Database: input.Database, Username: input.Username, ConnLimit: input.ConnLimit, StmtTimeoutMS: input.StmtTimeoutMS, RowLimit: input.RowLimit}
	created, err := handler.deps.Runtime.Store.Datasources().Create(request.Context(), datasource, input.Password)
	if err != nil {
		handler.fail(writer, http.StatusConflict, "datasource already exists or is invalid")
		return
	}
	handler.ok(writer, datasourceToView(created))
}

func (handler *Handler) datasourcesGet(writer http.ResponseWriter, request *http.Request) {
	datasource, err := handler.deps.Runtime.Store.Datasources().Get(request.Context(), request.PathValue("id"))
	if err != nil {
		handler.notFound(writer)
		return
	}
	handler.ok(writer, datasourceToView(datasource))
}

func (handler *Handler) datasourcesUpdate(writer http.ResponseWriter, request *http.Request) {
	current, err := handler.deps.Runtime.Store.Datasources().Get(request.Context(), request.PathValue("id"))
	if err != nil {
		handler.notFound(writer)
		return
	}
	var input datasourceInput
	if err := decodeJSON(writer, request, &input); err != nil {
		handler.fail(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	if input.ID == "" {
		input.ID = current.ID
	}
	if input.Name == "" {
		input.Name = current.Name
	}
	if input.DBType == "" {
		input.DBType = current.DBType
	}
	if input.Host == "" {
		input.Host = current.Host
	}
	if input.Port == 0 {
		input.Port = current.Port
	}
	if input.Database == "" {
		input.Database = current.Database
	}
	if input.Username == "" {
		input.Username = current.Username
	}
	if input.ConnLimit == 0 {
		input.ConnLimit = current.ConnLimit
	}
	if input.StmtTimeoutMS == 0 {
		input.StmtTimeoutMS = current.StmtTimeoutMS
	}
	if input.RowLimit == 0 {
		input.RowLimit = current.RowLimit
	}
	password := input.Password
	if password == "" {
		password, err = handler.deps.Runtime.Store.Datasources().DecryptPassword(current.PasswordEnc)
		if err != nil {
			handler.internal(writer, err)
			return
		}
	}
	updated, err := handler.deps.Runtime.Store.Datasources().Update(request.Context(), model.Datasource{ID: current.ID, Name: input.Name, DBType: input.DBType, Host: input.Host, Port: input.Port, Database: input.Database, Username: input.Username, ConnLimit: input.ConnLimit, StmtTimeoutMS: input.StmtTimeoutMS, RowLimit: input.RowLimit}, password)
	if err != nil {
		handler.internal(writer, err)
		return
	}
	handler.ok(writer, datasourceToView(updated))
}

func (handler *Handler) datasourcesDelete(writer http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	if _, err := handler.deps.Runtime.Store.Datasources().Get(request.Context(), id); err != nil {
		handler.notFound(writer)
		return
	}
	policies, err := handler.deps.Runtime.Store.Policies().List(request.Context())
	if err != nil {
		handler.internal(writer, err)
		return
	}
	for _, stored := range policies {
		if stored.DatasourceID == id {
			handler.fail(writer, http.StatusConflict, "datasource has policies; remove them first")
			return
		}
	}
	if err := handler.deps.Runtime.Store.Datasources().Delete(request.Context(), id); err != nil {
		handler.internal(writer, err)
		return
	}
	handler.ok(writer, map[string]bool{"deleted": true})
}

func (handler *Handler) datasourcesPing(writer http.ResponseWriter, request *http.Request) {
	datasource, err := handler.deps.Runtime.Store.Datasources().Get(request.Context(), request.PathValue("id"))
	if err != nil {
		handler.notFound(writer)
		return
	}
	result, err := handler.deps.DatasourcePinger.Ping(request.Context(), datasource)
	if err != nil {
		handler.internal(writer, err)
		return
	}
	handler.ok(writer, result)
}

func (handler *Handler) policiesList(writer http.ResponseWriter, request *http.Request) {
	var policies []model.Policy
	var err error
	agentID, datasourceID := request.URL.Query().Get("agent_id"), request.URL.Query().Get("datasource_id")
	if agentID != "" && datasourceID != "" {
		policies, err = handler.deps.Runtime.Store.Policies().ListByAgentAndDatasource(request.Context(), agentID, datasourceID)
	} else if agentID != "" {
		policies, err = handler.deps.Runtime.Store.Policies().ListByAgent(request.Context(), agentID)
	} else {
		policies, err = handler.deps.Runtime.Store.Policies().List(request.Context())
	}
	if err != nil {
		handler.internal(writer, err)
		return
	}
	page, size, err := parsePage(request)
	if err != nil {
		handler.fail(writer, http.StatusBadRequest, err.Error())
		return
	}
	handler.ok(writer, paginate(policyToViews(policies), page, size))
}

func policyFromInput(input policyInput) model.Policy {
	return model.Policy{ID: input.ID, AgentID: input.AgentID, DatasourceID: input.DatasourceID, ObjectType: input.ObjectType, ObjectName: input.ObjectName, Columns: input.Columns, RowFilter: input.RowFilter, Action: input.Action}
}

func validatePolicyInput(input policyInput) error {
	if strings.TrimSpace(input.ID) == "" || input.AgentID == "" || input.DatasourceID == "" || input.ObjectName == "" {
		return fmt.Errorf("policy identity fields are required")
	}
	_, err := policy.NewResolver().Resolve([]model.Policy{policyFromInput(input)}, "readonly")
	return err
}

func (handler *Handler) policiesCreate(writer http.ResponseWriter, request *http.Request) {
	var input policyInput
	if err := decodeJSON(writer, request, &input); err != nil {
		handler.fail(writer, 400, "invalid request body")
		return
	}
	if err := validatePolicyInput(input); err != nil {
		handler.fail(writer, 422, "invalid policy")
		return
	}
	created, err := handler.deps.Runtime.Store.Policies().Create(request.Context(), policyFromInput(input))
	if err != nil {
		handler.fail(writer, 409, "policy already exists or is invalid")
		return
	}
	handler.ok(writer, policyToView(created))
}
func (handler *Handler) policiesUpdate(writer http.ResponseWriter, request *http.Request) {
	var input policyInput
	if err := decodeJSON(writer, request, &input); err != nil {
		handler.fail(writer, 400, "invalid request body")
		return
	}
	input.ID = request.PathValue("id")
	if err := validatePolicyInput(input); err != nil {
		handler.fail(writer, 422, "invalid policy")
		return
	}
	updated, err := handler.deps.Runtime.Store.Policies().Update(request.Context(), policyFromInput(input))
	if err != nil {
		handler.notFound(writer)
		return
	}
	handler.ok(writer, policyToView(updated))
}
func (handler *Handler) policiesDelete(writer http.ResponseWriter, request *http.Request) {
	if err := handler.deps.Runtime.Store.Policies().Delete(request.Context(), request.PathValue("id")); err != nil {
		handler.notFound(writer)
		return
	}
	handler.ok(writer, map[string]bool{"deleted": true})
}

func (handler *Handler) rulesList(writer http.ResponseWriter, request *http.Request) {
	listed, err := handler.deps.Runtime.Store.Rules().List(request.Context(), request.URL.Query().Get("db_type"))
	if err != nil {
		handler.internal(writer, err)
		return
	}
	page, size, err := parsePage(request)
	if err != nil {
		handler.fail(writer, 400, err.Error())
		return
	}
	handler.ok(writer, paginate(ruleToViews(listed), page, size))
}
func (handler *Handler) rulesCreate(writer http.ResponseWriter, request *http.Request) {
	var input ruleInput
	if err := decodeJSON(writer, request, &input); err != nil {
		handler.fail(writer, 400, "invalid request body")
		return
	}
	if input.ID == "" || input.DBType == "" || input.PatternType == "" {
		handler.fail(writer, 422, "rule identity fields are required")
		return
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	builtin := false
	if input.Builtin != nil {
		builtin = *input.Builtin
	}
	created, err := handler.deps.Runtime.Store.Rules().Create(request.Context(), model.Rule{ID: input.ID, DBType: input.DBType, Title: input.Title, RiskLevel: input.RiskLevel, PatternType: input.PatternType, Definition: input.Definition, Enabled: enabled, Builtin: builtin})
	if err != nil {
		handler.fail(writer, 409, "rule already exists or is invalid")
		return
	}
	handler.ok(writer, ruleToView(created))
}
func (handler *Handler) rulesUpdate(writer http.ResponseWriter, request *http.Request) {
	current, err := handler.deps.Runtime.Store.Rules().Get(request.Context(), request.PathValue("id"))
	if err != nil {
		handler.notFound(writer)
		return
	}
	var input ruleInput
	if err := decodeJSON(writer, request, &input); err != nil {
		handler.fail(writer, 400, "invalid request body")
		return
	}
	if current.Builtin {
		if input.Enabled != nil {
			current.Enabled = *input.Enabled
		}
		if input.Definition != "" {
			current.Definition = input.Definition
		}
	} else {
		if input.DBType != "" {
			current.DBType = input.DBType
		}
		if input.Title != "" {
			current.Title = input.Title
		}
		if input.RiskLevel != 0 {
			current.RiskLevel = input.RiskLevel
		}
		if input.PatternType != "" {
			current.PatternType = input.PatternType
		}
		if input.Definition != "" {
			current.Definition = input.Definition
		}
		if input.Enabled != nil {
			current.Enabled = *input.Enabled
		}
		if input.Builtin != nil {
			current.Builtin = *input.Builtin
		}
	}
	updated, err := handler.deps.Runtime.Store.Rules().Update(request.Context(), current)
	if err != nil {
		handler.internal(writer, err)
		return
	}
	handler.ok(writer, ruleToView(updated))
}
func (handler *Handler) rulesDelete(writer http.ResponseWriter, request *http.Request) {
	rule, err := handler.deps.Runtime.Store.Rules().Get(request.Context(), request.PathValue("id"))
	if err != nil {
		handler.notFound(writer)
		return
	}
	if rule.Builtin {
		handler.fail(writer, 403, "builtin rules cannot be deleted")
		return
	}
	if err := handler.deps.Runtime.Store.Rules().Delete(request.Context(), rule.ID); err != nil {
		handler.internal(writer, err)
		return
	}
	handler.ok(writer, map[string]bool{"deleted": true})
}

func validateMaskInput(input maskRuleInput) error {
	if input.ID == "" || input.ColumnName == "" {
		return fmt.Errorf("mask rule identity fields are required")
	}
	_, err := mask.NewRedactor([]mask.Rule{{Column: input.ColumnName, SensitiveType: mask.SensitiveType(input.SensitiveType), Algorithm: mask.Algorithm(input.Algo)}})
	return err
}

func normalizeMaskInput(input maskRuleInput) maskRuleInput {
	input.ColumnName = mask.NormalizeColumnName(input.ColumnName)
	if input.DatasourceID != nil {
		trimmed := strings.TrimSpace(*input.DatasourceID)
		if trimmed == "" {
			input.DatasourceID = nil
		} else {
			input.DatasourceID = stringPointer(trimmed)
		}
	}
	return input
}

func (handler *Handler) maskRuleConflict(
	ctx context.Context,
	datasourceID *string,
	columnName string,
	excludeID string,
) (bool, error) {
	rules, err := handler.deps.Runtime.Store.MaskRules().List(ctx)
	if err != nil {
		return false, err
	}
	for _, rule := range rules {
		if rule.ID == excludeID || !sameMaskScope(rule.DatasourceID, datasourceID) {
			continue
		}
		if mask.NormalizeColumnName(rule.ColumnName) == columnName {
			return true, nil
		}
	}
	return false, nil
}

func sameMaskScope(left, right *string) bool {
	leftScope := ""
	if left != nil {
		leftScope = strings.TrimSpace(*left)
	}
	rightScope := ""
	if right != nil {
		rightScope = strings.TrimSpace(*right)
	}
	return leftScope == rightScope
}
func (handler *Handler) maskRulesList(writer http.ResponseWriter, request *http.Request) {
	var listed []model.MaskRule
	var err error
	if id := request.URL.Query().Get("datasource_id"); id != "" {
		listed, err = handler.deps.Runtime.Store.MaskRules().ListByDatasource(request.Context(), id)
	} else {
		listed, err = handler.listAllMaskRules(request.Context())
	}
	if err != nil {
		handler.internal(writer, err)
		return
	}
	page, size, err := parsePage(request)
	if err != nil {
		handler.fail(writer, 400, err.Error())
		return
	}
	handler.ok(writer, paginate(maskRuleToViews(listed), page, size))
}
func (handler *Handler) listAllMaskRules(ctx context.Context) ([]model.MaskRule, error) {
	return handler.deps.Runtime.Store.MaskRules().List(ctx)
}
func (handler *Handler) maskRulesCreate(writer http.ResponseWriter, request *http.Request) {
	var input maskRuleInput
	if err := decodeJSON(writer, request, &input); err != nil {
		handler.fail(writer, 400, "invalid request body")
		return
	}
	input = normalizeMaskInput(input)
	if err := validateMaskInput(input); err != nil {
		handler.fail(writer, 422, "invalid mask rule")
		return
	}
	conflict, err := handler.maskRuleConflict(request.Context(), input.DatasourceID, input.ColumnName, "")
	if err != nil {
		handler.internal(writer, err)
		return
	}
	if conflict {
		handler.fail(writer, 409, "该数据源下此列名已存在脱敏规则，v0.1 同列仅支持一条规则")
		return
	}
	created, err := handler.deps.Runtime.Store.MaskRules().Create(request.Context(), model.MaskRule{ID: input.ID, DatasourceID: input.DatasourceID, TableName: input.TableName, ColumnName: input.ColumnName, SensitiveType: input.SensitiveType, Algo: input.Algo})
	if err != nil {
		handler.fail(writer, 409, "mask rule already exists or is invalid")
		return
	}
	handler.ok(writer, maskRuleToView(created))
}
func (handler *Handler) maskRulesUpdate(writer http.ResponseWriter, request *http.Request) {
	var input maskRuleInput
	if err := decodeJSON(writer, request, &input); err != nil {
		handler.fail(writer, 400, "invalid request body")
		return
	}
	input.ID = request.PathValue("id")
	input = normalizeMaskInput(input)
	if err := validateMaskInput(input); err != nil {
		handler.fail(writer, 422, "invalid mask rule")
		return
	}
	if _, err := handler.deps.Runtime.Store.MaskRules().Get(request.Context(), input.ID); err != nil {
		handler.notFound(writer)
		return
	}
	conflict, err := handler.maskRuleConflict(request.Context(), input.DatasourceID, input.ColumnName, input.ID)
	if err != nil {
		handler.internal(writer, err)
		return
	}
	if conflict {
		handler.fail(writer, 409, "该数据源下此列名已存在脱敏规则，v0.1 同列仅支持一条规则")
		return
	}
	updated, err := handler.deps.Runtime.Store.MaskRules().Update(request.Context(), model.MaskRule{ID: input.ID, DatasourceID: input.DatasourceID, TableName: input.TableName, ColumnName: input.ColumnName, SensitiveType: input.SensitiveType, Algo: input.Algo})
	if err != nil {
		handler.notFound(writer)
		return
	}
	handler.ok(writer, maskRuleToView(updated))
}
func (handler *Handler) maskRulesDelete(writer http.ResponseWriter, request *http.Request) {
	if err := handler.deps.Runtime.Store.MaskRules().Delete(request.Context(), request.PathValue("id")); err != nil {
		handler.notFound(writer)
		return
	}
	handler.ok(writer, map[string]bool{"deleted": true})
}

func auditFilterFromRequest(request *http.Request) (model.AuditFilter, error) {
	q := request.URL.Query()
	f := model.AuditFilter{Keyword: q.Get("keyword"), ObjectLike: q.Get("object")}
	if value := q.Get("agent_id"); value != "" {
		f.AgentID = &value
	}
	if value := q.Get("datasource_id"); value != "" {
		f.DatasourceID = &value
	}
	if value := q.Get("session_id"); value != "" {
		f.SessionID = &value
	}
	if value := q.Get("mcp_tool"); value != "" {
		f.MCPTool = &value
	}
	f.Decisions = splitCSV(q.Get("decisions"))
	f.StmtTypes = splitCSV(q.Get("stmt_types"))
	if value := q.Get("risk_min"); value != "" {
		parsed, err := parseInt(value)
		if err != nil {
			return model.AuditFilter{}, fmt.Errorf("invalid risk_min")
		}
		f.RiskMin = &parsed
	}
	if value := q.Get("risk_max"); value != "" {
		parsed, err := parseInt(value)
		if err != nil {
			return model.AuditFilter{}, fmt.Errorf("invalid risk_max")
		}
		f.RiskMax = &parsed
	}
	if value := q.Get("time_start"); value != "" {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return model.AuditFilter{}, fmt.Errorf("invalid time_start")
		}
		f.TimeStart = &parsed
	}
	if value := q.Get("time_end"); value != "" {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return model.AuditFilter{}, fmt.Errorf("invalid time_end")
		}
		f.TimeEnd = &parsed
	}
	return f, nil
}
func parseInt(value string) (int, error) {
	var sign, number int = 1, 0
	for index, r := range value {
		if index == 0 && r == '-' {
			sign = -1
			continue
		}
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("invalid number")
		}
		number = number*10 + int(r-'0')
	}
	return sign * number, nil
}
func (handler *Handler) auditList(writer http.ResponseWriter, request *http.Request) {
	page, size, err := parsePage(request)
	if err != nil {
		handler.fail(writer, 400, err.Error())
		return
	}
	filter, err := auditFilterFromRequest(request)
	if err != nil {
		handler.fail(writer, 400, err.Error())
		return
	}
	listed, err := handler.deps.Runtime.Store.AuditLogs().FilteredPage(request.Context(), filter, page, size)
	if err != nil {
		handler.internal(writer, err)
		return
	}
	handler.ok(writer, pageResponse{
		Total: listed.Total, Page: listed.Page, PageSize: listed.PageSize, List: auditToViews(listed.List),
	})
}
func (handler *Handler) auditExport(writer http.ResponseWriter, request *http.Request) {
	filter, err := auditFilterFromRequest(request)
	if err != nil {
		handler.fail(writer, 400, err.Error())
		return
	}
	buffer := make([]model.AuditLog, 0, 10000)
	for page := 1; ; page++ {
		listed, err := handler.deps.Runtime.Store.AuditLogs().FilteredPage(request.Context(), filter, page, 100)
		if err != nil {
			handler.internal(writer, err)
			return
		}
		buffer = append(buffer, listed.List...)
		if len(buffer) > 10000 {
			handler.fail(writer, 422, "audit export exceeds 10000 rows")
			return
		}
		if len(listed.List) < 100 {
			break
		}
	}
	writer.Header().Set("Content-Type", "application/x-ndjson")
	writer.Header().Set("Content-Disposition", "attachment; filename=agentsql-audit.jsonl")
	encoder := json.NewEncoder(writer)
	for _, log := range buffer {
		if err := encoder.Encode(auditToView(log)); err != nil {
			return
		}
	}
}

func (handler *Handler) approvalsList(writer http.ResponseWriter, request *http.Request) {
	page, size, err := parsePage(request)
	if err != nil {
		handler.fail(writer, 400, err.Error())
		return
	}
	listed, err := handler.deps.Runtime.Store.Approvals().ListPage(request.Context(), request.URL.Query().Get("status"), page, size)
	if err != nil {
		handler.internal(writer, err)
		return
	}
	handler.ok(writer, pageResponse{
		Total: listed.Total, Page: listed.Page, PageSize: listed.PageSize, List: approvalToViews(listed.List),
	})
}
func (handler *Handler) approvalsDecide(writer http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	var input decideInput
	if err := decodeJSON(writer, request, &input); err != nil {
		handler.fail(writer, 400, "invalid request body")
		return
	}
	if input.Decision != "approve" && input.Decision != "reject" {
		handler.fail(writer, 422, "decision must be approve or reject")
		return
	}
	status := "approved"
	if input.Decision == "reject" {
		status = "rejected"
	}
	now := time.Now().UTC()
	var reason *string
	if strings.TrimSpace(input.Comment) != "" {
		reason = stringPointer(input.Comment)
	}
	updated, err := handler.deps.Runtime.Store.Approvals().DecidePending(
		request.Context(), id, status, handler.adminUser, reason, now,
	)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			handler.notFound(writer)
			return
		}
		if errors.Is(err, store.ErrApprovalNotPending) {
			handler.fail(writer, 409, "approval is no longer pending")
			return
		}
		handler.internal(writer, err)
		return
	}
	handler.ok(writer, approvalToView(updated))
}

func (handler *Handler) dashboardSummary(writer http.ResponseWriter, request *http.Request) {
	days := 14
	if value := request.URL.Query().Get("days"); value != "" {
		parsed, err := parseInt(value)
		if err != nil || parsed < 1 || parsed > 90 {
			handler.fail(writer, 400, "days must be between 1 and 90")
			return
		}
		days = parsed
	}
	summary, err := handler.deps.Runtime.Store.Dashboard().Summary(request.Context(), days)
	if err != nil {
		handler.internal(writer, err)
		return
	}
	handler.ok(writer, summary)
}

func (handler *Handler) ok(writer http.ResponseWriter, data any) {
	handler.write(writer, http.StatusOK, 0, "ok", data)
}
func (handler *Handler) fail(writer http.ResponseWriter, status int, msg string) {
	handler.write(writer, status, status, msg, nil)
}
func (handler *Handler) notFound(writer http.ResponseWriter) {
	handler.fail(writer, http.StatusNotFound, "not found")
}
func (handler *Handler) internal(writer http.ResponseWriter, err error) {
	handler.logger.Error().Str("error_type", fmt.Sprintf("%T", err)).Msg("admin API internal error")
	handler.fail(writer, http.StatusInternalServerError, "internal error")
}
func (handler *Handler) write(writer http.ResponseWriter, status, code int, msg string, data any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(apiResponse{Code: code, Msg: msg, Data: data})
}

func adminBearerToken(header string) (string, bool) {
	// Do NOT trim the whole header first: that would silently accept a trailing
	// space after the token. Require the exact single-space scheme and reject any
	// whitespace inside, before or after the token (fail-closed).
	if !strings.HasPrefix(header, "Bearer ") {
		return "", false
	}
	token := strings.TrimPrefix(header, "Bearer ")
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return "", false
	}
	return token, true
}
func stringPointer(value string) *string { return &value }
