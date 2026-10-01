package adminapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/bootstrap"
	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/discovery"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/policy"
	"github.com/cuipengdba/agentsql/internal/rbac"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/rs/zerolog"
)

type Deps struct {
	Runtime          *bootstrap.Runtime
	Config           config.Config
	AdminUsername    string
	AdminPassword    string
	TokenKey         []byte
	RBAC             *rbac.Service
	DatasourcePinger DatasourcePinger
	Discovery        DiscoveryRunner
	ChainManifests   ChainManifestProvider
	// B5Admin is nil only for an explicit feature-off assembly. S9 routes remain
	// registered so clients receive stable empty capability responses, while an
	// activated assembly must explicitly provide the
	// read/operation boundary.
	B5Admin B5AdminBackend
	// EventStreamHeartbeatInterval is injectable for deterministic stream tests.
	// Zero uses the production interval.
	EventStreamHeartbeatInterval time.Duration

	demoRunner DemoRunner
	demoKeys   demoProfileKeys
}

type DiscoveryRunner interface {
	Discover(context.Context, string, string, discovery.ScanRequest) (discovery.ScanResult, error)
}

type DatasourcePinger interface {
	Ping(ctx context.Context, datasource model.Datasource) (PingResult, error)
}

type PingResult struct {
	OK           bool    `json:"ok"`
	LatencyMS    int64   `json:"latency_ms"`
	ErrorCode    *string `json:"error_code,omitempty"`
	ErrorMessage *string `json:"error_message,omitempty"`
}

type runtimePinger struct{ runtime *bootstrap.Runtime }

func (pinger runtimePinger) Ping(ctx context.Context, datasource model.Datasource) (PingResult, error) {
	started := time.Now()
	err := pinger.runtime.PingDatasource(ctx, datasource)
	return pingResult(started, err), err
}

func pingResult(started time.Time, err error) PingResult {
	result := PingResult{OK: err == nil, LatencyMS: time.Since(started).Milliseconds()}
	var databaseError *executor.DBError
	if errors.As(err, &databaseError) {
		code, message := string(databaseError.Code), databaseError.Error()
		result.ErrorCode, result.ErrorMessage = &code, &message
	}
	return result
}

type Handler struct {
	deps           Deps
	logger         zerolog.Logger
	mux            *http.ServeMux
	adminUser      string
	adminPassword  string
	tokenKey       []byte
	rbac           *rbac.Service
	streamSlots    chan struct{}
	heartbeat      time.Duration
	notificationMu sync.Mutex
	chainVerifyMu  sync.Mutex
	chainVerifying map[string]bool
	chainLastStart map[string]time.Time
}

func NewHandler(deps Deps, logger zerolog.Logger) (http.Handler, error) {
	if deps.Runtime == nil || deps.Runtime.Store == nil {
		return nil, fmt.Errorf("create admin handler: runtime is incomplete")
	}
	if err := deps.Config.Validate(); err != nil {
		return nil, fmt.Errorf("create admin handler: invalid configuration: %w", err)
	}
	// The runtime owns redaction capability. The HTTP layer must never retain
	// the configured hash key merely because it needs the rest of Config.
	deps.Config.Redaction.HashKey = ""
	deps.Config.Redaction.HashKeys = nil
	if strings.TrimSpace(deps.AdminUsername) == "" {
		deps.AdminUsername = "admin"
	}
	if deps.AdminPassword == "" {
		return nil, fmt.Errorf("create admin handler: administrator password is required")
	}
	if len(deps.TokenKey) == 0 {
		return nil, fmt.Errorf("create admin handler: token key is required")
	}
	if deps.RBAC == nil {
		deps.RBAC = rbac.NewService(deps.Runtime.Store.RBAC())
	}
	if err := bootstrapRBAC(deps.RBAC, deps.AdminUsername, deps.AdminPassword); err != nil {
		return nil, fmt.Errorf("create admin handler: bootstrap RBAC: %w", err)
	}
	if deps.Config.DemoEnabled() {
		if deps.demoRunner == nil {
			return nil, fmt.Errorf("create admin handler: demo pipeline is unavailable")
		}
		if !deps.demoKeys.valid() {
			return nil, fmt.Errorf("create admin handler: demo credentials were not validated")
		}
	}
	streamEnabled := deps.Config.Server.ConsoleEnabled && deps.Config.Server.EventStream
	if streamEnabled && deps.Runtime.Events == nil {
		return nil, fmt.Errorf("create admin handler: event stream is enabled but runtime hub is unavailable")
	}
	if deps.DatasourcePinger == nil {
		deps.DatasourcePinger = runtimePinger{runtime: deps.Runtime}
	}
	if deps.Discovery == nil {
		deps.Discovery = deps.Runtime.ControlledRead
	}
	if deps.B5Admin != nil {
		deps.Runtime.SetB5StatusProvider(func(ctx context.Context) (bootstrap.B5Status, error) {
			status, err := deps.B5Admin.Status(ctx)
			return bootstrap.B5Status{Enabled: true, State: status.State, Reason: status.Reason, Ready: status.Ready}, err
		})
	}
	handler := &Handler{deps: deps, logger: logger, adminUser: deps.AdminUsername,
		adminPassword: deps.AdminPassword, tokenKey: append([]byte(nil), deps.TokenKey...), rbac: deps.RBAC,
		chainVerifying: make(map[string]bool), chainLastStart: make(map[string]time.Time)}
	if streamEnabled {
		handler.streamSlots = make(chan struct{}, deps.Config.Server.EventStreamMaxConnections)
		handler.heartbeat = deps.EventStreamHeartbeatInterval
		if handler.heartbeat <= 0 {
			handler.heartbeat = 25 * time.Second
		}
	}
	mux := http.NewServeMux()
	handler.mux = mux
	mux.HandleFunc("POST /api/v1/auth/login", handler.login)
	mux.HandleFunc("GET /api/v1/auth/me", handler.me)
	mux.HandleFunc("POST /api/v1/auth/logout", handler.logout)
	mux.HandleFunc("GET /api/v1/permissions", handler.permissionsList)
	mux.HandleFunc("GET /api/v1/users", handler.usersList)
	mux.HandleFunc("POST /api/v1/users", handler.usersCreate)
	mux.HandleFunc("GET /api/v1/users/{id}", handler.usersGet)
	mux.HandleFunc("PUT /api/v1/users/{id}", handler.usersUpdate)
	mux.HandleFunc("DELETE /api/v1/users/{id}", handler.usersDelete)
	mux.HandleFunc("PUT /api/v1/users/{id}/roles", handler.usersSetRoles)
	mux.HandleFunc("GET /api/v1/roles", handler.rolesList)
	mux.HandleFunc("POST /api/v1/roles", handler.rolesCreate)
	mux.HandleFunc("GET /api/v1/roles/{id}", handler.rolesGet)
	mux.HandleFunc("PUT /api/v1/roles/{id}", handler.rolesUpdate)
	mux.HandleFunc("DELETE /api/v1/roles/{id}", handler.rolesDelete)
	mux.HandleFunc("PUT /api/v1/roles/{id}/permissions", handler.rolesSetPermissions)
	mux.HandleFunc("GET /api/v1/tenants", handler.tenantsList)
	mux.HandleFunc("POST /api/v1/tenants", handler.tenantsCreate)
	mux.HandleFunc("GET /api/v1/tenants/{id}", handler.tenantsGet)
	mux.HandleFunc("PUT /api/v1/tenants/{id}", handler.tenantsUpdate)
	mux.HandleFunc("DELETE /api/v1/tenants/{id}", handler.tenantsDelete)
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
	mux.HandleFunc("POST /api/v1/datasources/{id}/discover", handler.datasourcesDiscover)
	mux.HandleFunc("POST /api/v1/datasources/{id}/discover/apply", handler.datasourcesDiscoverApply)
	mux.HandleFunc("GET /api/v1/policies", handler.policiesList)
	mux.HandleFunc("POST /api/v1/policies", handler.policiesCreate)
	mux.HandleFunc("GET /api/v1/policies/{id}", handler.policiesGet)
	mux.HandleFunc("PUT /api/v1/policies/{id}", handler.policiesUpdate)
	mux.HandleFunc("DELETE /api/v1/policies/{id}", handler.policiesDelete)
	mux.HandleFunc("GET /api/v1/rules", handler.rulesList)
	mux.HandleFunc("POST /api/v1/rules", handler.rulesCreate)
	mux.HandleFunc("PUT /api/v1/rules/{id}", handler.rulesUpdate)
	mux.HandleFunc("DELETE /api/v1/rules/{id}", handler.rulesDelete)
	mux.HandleFunc("GET /api/v1/mask_rules", handler.maskRulesList)
	mux.HandleFunc("GET /api/v1/redaction/keys", handler.redactionKeysList)
	mux.HandleFunc("POST /api/v1/mask_rules", handler.maskRulesCreate)
	mux.HandleFunc("PUT /api/v1/mask_rules/{id}", handler.maskRulesUpdate)
	mux.HandleFunc("DELETE /api/v1/mask_rules/{id}", handler.maskRulesDelete)
	mux.HandleFunc("GET /api/v1/audit", handler.auditList)
	mux.HandleFunc("GET /api/v1/audit/export", handler.auditExport)
	mux.HandleFunc("GET /api/v1/audit-chain/status", handler.auditChainStatus)
	mux.HandleFunc("POST /api/v1/audit-chain/verify", handler.auditChainVerify)
	mux.HandleFunc("GET /api/v1/approvals", handler.approvalsList)
	mux.HandleFunc("POST /api/v1/approvals/{id}/decide", handler.approvalsDecide)
	mux.HandleFunc("GET /api/v1/dashboard/summary", handler.dashboardSummary)
	mux.HandleFunc("GET /api/v1/b5/status", handler.b5Status)
	mux.HandleFunc("GET /api/v1/b5/sessions", handler.b5SessionsList)
	mux.HandleFunc("GET /api/v1/b5/sessions/{id}", handler.b5SessionsGet)
	mux.HandleFunc("GET /api/v1/b5/transactions", handler.b5TransactionsList)
	mux.HandleFunc("GET /api/v1/b5/transactions/{id}", handler.b5TransactionsGet)
	mux.HandleFunc("GET /api/v1/b5/quarantine", handler.b5QuarantineList)
	mux.HandleFunc("POST /api/v1/b5/quarantine/{id}/confirm-discard", handler.b5ConfirmDiscard)
	mux.HandleFunc("GET /api/v1/b5/inventory", handler.b5Inventory)
	mux.HandleFunc("GET /api/v1/b5/metrics", handler.b5Metrics)
	mux.HandleFunc("GET /api/v1/b5/reconciliation", handler.b5ReconciliationList)
	mux.HandleFunc("POST /api/v1/b5/reconciliation", handler.b5Reconcile)
	mux.HandleFunc("GET /api/v1/integrations/notifications", handler.notificationsGet)
	mux.HandleFunc("PUT /api/v1/integrations/notifications", handler.notificationsPut)
	mux.HandleFunc("POST /api/v1/integrations/notifications/test", handler.notificationsTest)
	mux.HandleFunc("GET /api/v1/integrations/notifications/health", handler.notificationsHealth)
	mux.HandleFunc("POST /api/v1/playground/assess", handler.playgroundAssess)
	if deps.Config.DemoEnabled() {
		mux.HandleFunc("POST /api/v1/playground/run", handler.playgroundRun)
	}
	if streamEnabled {
		mux.HandleFunc("GET /api/v1/stream", handler.stream)
	}
	root := handler.recover(handler.adminAuth(mux))
	sealed := executor.SealedHTTP(root, executor.DefaultLimits.EnvelopeBytes)
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		// The audit event stream is not a SQL/result transport. It remains a
		// deliberate streaming endpoint; every SQL-bearing admin route is sealed.
		if request.URL.Path == "/api/v1/stream" {
			root.ServeHTTP(writer, request)
			return
		}
		sealed.ServeHTTP(writer, request)
	}), nil
}

func bootstrapRBAC(service *rbac.Service, username, password string) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		err = service.Bootstrap(context.Background(), username, password)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "database is locked") && !strings.Contains(strings.ToLower(err.Error()), "sqlite_busy") {
			return err
		}
		if attempt < 2 {
			time.Sleep(100 * time.Millisecond)
		}
	}
	return err
}

func (handler *Handler) adminAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost && request.URL.Path == "/api/v1/auth/login" {
			next.ServeHTTP(writer, request)
			return
		}
		token, ok := adminBearerToken(request.Header.Get("Authorization"))
		payload, tokenErr := parseAdminToken(handler.tokenKey, token, time.Now())
		if !ok || tokenErr != nil {
			handler.fail(writer, http.StatusUnauthorized, "unauthorized")
			return
		}
		principal, err := handler.rbac.Principal(request.Context(), payload.TenantID, payload.UserID)
		if err != nil || principal.Username != payload.Username {
			handler.fail(writer, http.StatusUnauthorized, "unauthorized")
			return
		}
		permission, protected := requiredPermission(request.Method, request.URL.Path)
		if protected && !principal.Has(permission) {
			handler.fail(writer, http.StatusForbidden, "forbidden")
			return
		}
		if !protected && request.URL.Path != "/api/v1/auth/me" && request.URL.Path != "/api/v1/auth/logout" {
			handler.fail(writer, http.StatusForbidden, "forbidden")
			return
		}
		if principal.TenantID != rbac.DefaultTenantID && !tenantNativeRoute(request.URL.Path) {
			// Existing SQL gateway resources predate tenant ownership columns. Until
			// they are migrated, non-default tenants must never see the shared rows.
			handler.fail(writer, http.StatusForbidden, "forbidden")
			return
		}
		ctx := context.WithValue(request.Context(), principalContextKey{}, principal)
		next.ServeHTTP(writer, request.WithContext(ctx))
	})
}

func tenantNativeRoute(path string) bool {
	return strings.HasPrefix(path, "/api/v1/auth/") || strings.HasPrefix(path, "/api/v1/users") ||
		strings.HasPrefix(path, "/api/v1/roles") || strings.HasPrefix(path, "/api/v1/tenants") || path == "/api/v1/permissions"
}

type principalContextKey struct{}

func requestPrincipal(request *http.Request) (rbac.Principal, bool) {
	if request == nil {
		return rbac.Principal{}, false
	}
	principal, ok := request.Context().Value(principalContextKey{}).(rbac.Principal)
	return principal, ok && principal.UserID != "" && principal.TenantID != ""
}

func requiredPermission(method, path string) (string, bool) {
	switch {
	case strings.HasPrefix(path, "/api/v1/users"):
		return rbac.PermissionUserManage, true
	case strings.HasPrefix(path, "/api/v1/roles"), path == "/api/v1/permissions":
		return rbac.PermissionRoleManage, true
	case strings.HasPrefix(path, "/api/v1/tenants"):
		return rbac.PermissionTenantManage, true
	case strings.HasPrefix(path, "/api/v1/agents"), strings.HasPrefix(path, "/api/v1/datasources"):
		return rbac.PermissionDatasourceManage, true
	case strings.HasPrefix(path, "/api/v1/policies"), strings.HasPrefix(path, "/api/v1/rules"),
		strings.HasPrefix(path, "/api/v1/mask_rules"), strings.HasPrefix(path, "/api/v1/redaction"),
		strings.HasPrefix(path, "/api/v1/integrations"):
		return rbac.PermissionStrategyManage, true
	case strings.HasPrefix(path, "/api/v1/audit"), strings.HasPrefix(path, "/api/v1/dashboard"), path == "/api/v1/stream":
		return rbac.PermissionAuditView, true
	case strings.HasPrefix(path, "/api/v1/approvals"), strings.HasPrefix(path, "/api/v1/playground"), strings.HasPrefix(path, "/api/v1/b5"):
		return rbac.PermissionQueryExecute, true
	default:
		_ = method
		return "", false
	}
}

func (handler *Handler) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		started := time.Now()
		statusWriter := &statusRecorder{ResponseWriter: writer, status: http.StatusOK}
		defer func() {
			if recovered := recover(); recovered != nil {
				handler.logger.Error().Bytes("stack", debug.Stack()).Str("panic_type", fmt.Sprintf("%T", recovered)).Msg("admin API panic recovered")
				if !statusWriter.wroteHeader {
					handler.fail(statusWriter, http.StatusInternalServerError, "internal error")
				}
			}
			handler.logger.Info().Str("method", request.Method).Str("path", request.URL.Path).
				Str("admin_user", handler.adminUser).
				Int("status", statusWriter.status).
				Int64("latency_ms", time.Since(started).Milliseconds()).Msg("admin API request")
		}()
		next.ServeHTTP(statusWriter, request)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (writer *statusRecorder) WriteHeader(status int) {
	if writer.wroteHeader {
		return
	}
	writer.status = status
	writer.wroteHeader = true
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *statusRecorder) Write(body []byte) (int, error) {
	if !writer.wroteHeader {
		writer.WriteHeader(http.StatusOK)
	}
	return writer.ResponseWriter.Write(body)
}

func (writer *statusRecorder) Unwrap() http.ResponseWriter {
	return writer.ResponseWriter
}

func (writer *statusRecorder) AfterCommit(callback func()) {
	if transactional, ok := writer.ResponseWriter.(interface{ AfterCommit(func()) }); ok {
		transactional.AfterCommit(callback)
	}
}

func (handler *Handler) login(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
		TenantID string `json:"tenant_id,omitempty"`
	}
	if err := decodeJSON(writer, request, &input); err != nil {
		handler.fail(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	if input.TenantID == "" {
		input.TenantID = rbac.DefaultTenantID
	}
	principal, err := handler.rbac.Authenticate(request.Context(), input.TenantID, input.Username, input.Password)
	if err != nil {
		handler.fail(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	jtiBytes := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, jtiBytes); err != nil {
		handler.fail(writer, http.StatusInternalServerError, "internal error")
		return
	}
	token, expires, err := issuePrincipalToken(handler.tokenKey, time.Now(), hex.EncodeToString(jtiBytes), principal)
	if err != nil {
		handler.fail(writer, http.StatusInternalServerError, "internal error")
		return
	}
	handler.ok(writer, map[string]any{"token": token, "expires_at": expires.Format(time.RFC3339)})
}

func (handler *Handler) me(writer http.ResponseWriter, request *http.Request) {
	principal, ok := requestPrincipal(request)
	if !ok {
		handler.fail(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	handler.ok(writer, map[string]any{"id": principal.UserID, "tenant_id": principal.TenantID,
		"username": principal.Username, "roles": principal.RoleIDs, "permissions": principal.Permissions})
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
	handler.refreshNotificationNames(request.Context())
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
	if input.ExpiresAt.Present {
		agent.ExpiresAt = input.ExpiresAt.Value
	}
	updated, err := handler.deps.Runtime.Store.Agents().Update(request.Context(), agent)
	if err != nil {
		handler.internal(writer, err)
		return
	}
	handler.refreshNotificationNames(request.Context())
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
	handler.refreshNotificationNames(request.Context())
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
	handler.refreshNotificationNames(request.Context())
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
	handler.refreshNotificationNames(request.Context())
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
	handler.refreshNotificationNames(request.Context())
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
		var databaseError *executor.DBError
		if !errors.As(err, &databaseError) {
			handler.internal(writer, err)
			return
		}
		result.OK = false
		code, message := string(databaseError.Code), databaseError.Error()
		result.ErrorCode, result.ErrorMessage = &code, &message
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
	result := model.Policy{ID: input.ID, AgentID: input.AgentID, DatasourceID: input.DatasourceID, ObjectType: input.ObjectType, ObjectName: input.ObjectName, Columns: input.Columns, RelationBindingID: input.RelationBindingID, RowFilter: input.RowFilter, Action: input.Action}
	if input.ColumnPermissions != nil {
		result.ColumnPermissions = make([]model.PolicyColumnPermission, 0, len(*input.ColumnPermissions))
		for _, permission := range *input.ColumnPermissions {
			result.ColumnPermissions = append(result.ColumnPermissions, model.PolicyColumnPermission{
				PolicyID: input.ID, RelationEnrollmentID: permission.RelationEnrollmentID,
				ColumnOrdinal: permission.ColumnOrdinal, ColumnName: permission.ColumnName,
				ColumnTypeDigest: permission.ColumnTypeDigest, Usage: permission.Usage,
			})
		}
	}
	return result
}

func validatePolicyInput(input policyInput) error {
	if strings.TrimSpace(input.ID) == "" || input.AgentID == "" || input.DatasourceID == "" || input.ObjectName == "" {
		return fmt.Errorf("policy identity fields are required")
	}
	legacyTokens, err := legacyColumnTokens(input.Columns)
	if err != nil {
		return err
	}
	for _, token := range legacyTokens {
		if token == "*" {
			return fmt.Errorf("new wildcard column grants are forbidden")
		}
	}
	if input.ColumnPermissions != nil {
		seen := make(map[string]struct{}, len(*input.ColumnPermissions))
		for _, permission := range *input.ColumnPermissions {
			if permission.RelationEnrollmentID == "" || permission.ColumnOrdinal <= 0 || strings.TrimSpace(permission.ColumnName) == "" || permission.ColumnName == "*" || permission.ColumnTypeDigest == "" || (permission.Usage != "output" && permission.Usage != "reference") {
				return fmt.Errorf("invalid column permission")
			}
			key := permission.RelationEnrollmentID + "\x00" + strconv.Itoa(permission.ColumnOrdinal) + "\x00" + permission.Usage
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate column permission")
			}
			seen[key] = struct{}{}
		}
		if input.Columns != nil && !legacyPermissionsEquivalent(legacyTokens, *input.ColumnPermissions) {
			return errPolicyRepresentationsConflict
		}
	}
	_, err = policy.NewResolver().Resolve([]model.Policy{policyFromInput(input)}, "readonly")
	return err
}

var errPolicyRepresentationsConflict = errors.New("legacy columns and column_permissions conflict")

func legacyColumnTokens(columns *string) ([]string, error) {
	if columns == nil || strings.TrimSpace(*columns) == "" {
		return nil, nil
	}
	parts := strings.Split(*columns, ",")
	result := make([]string, len(parts))
	for index, part := range parts {
		result[index] = strings.TrimSpace(part)
		if result[index] == "" {
			return nil, fmt.Errorf("empty legacy column token")
		}
	}
	return result, nil
}

func legacyPermissionsEquivalent(tokens []string, permissions []columnPermissionInput) bool {
	if len(permissions) != len(tokens)*2 {
		return false
	}
	want := make(map[string]int, len(tokens)*2)
	for _, token := range tokens {
		want[token+"\x00output"]++
		want[token+"\x00reference"]++
	}
	for _, permission := range permissions {
		key := permission.ColumnName + "\x00" + permission.Usage
		if want[key] == 0 {
			return false
		}
		want[key]--
	}
	return true
}

func normalizePolicyRepresentations(input *policyInput) {
	if input.ColumnPermissions == nil {
		return
	}
	permissions := *input.ColumnPermissions
	byName := make(map[string]uint8)
	order := make([]string, 0)
	for _, permission := range permissions {
		if _, exists := byName[permission.ColumnName]; !exists {
			order = append(order, permission.ColumnName)
		}
		if permission.Usage == "output" {
			byName[permission.ColumnName] |= 1
		} else {
			byName[permission.ColumnName] |= 2
		}
	}
	representable := true
	for _, bits := range byName {
		if bits != 3 {
			representable = false
			break
		}
	}
	if input.Columns == nil && representable {
		columns := strings.Join(order, ",")
		input.Columns = &columns
	}
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
	normalizePolicyRepresentations(&input)
	policyModel := policyFromInput(input)
	policyModel.LegacyUnrepresentable = input.ColumnPermissions != nil && input.Columns == nil
	created, err := handler.deps.Runtime.Store.Policies().Create(request.Context(), policyModel)
	if err != nil {
		handler.fail(writer, 409, "policy already exists or is invalid")
		return
	}
	writer.Header().Set("ETag", policyETag(created.ID, created.Revision))
	handler.ok(writer, policyToView(created))
}

func (handler *Handler) policiesGet(writer http.ResponseWriter, request *http.Request) {
	stored, err := handler.deps.Runtime.Store.Policies().Get(request.Context(), request.PathValue("id"))
	if err != nil {
		handler.notFound(writer)
		return
	}
	writer.Header().Set("ETag", policyETag(stored.ID, stored.Revision))
	handler.ok(writer, policyToView(stored))
}

func (handler *Handler) policiesUpdate(writer http.ResponseWriter, request *http.Request) {
	id, expected, status := parsePolicyIfMatch(request)
	if status != 0 {
		handler.fail(writer, status, http.StatusText(status))
		return
	}
	if id != request.PathValue("id") {
		handler.fail(writer, http.StatusBadRequest, "If-Match policy does not match request path")
		return
	}
	current, err := handler.deps.Runtime.Store.Policies().Get(request.Context(), id)
	if err != nil {
		handler.notFound(writer)
		return
	}
	if current.Revision != expected {
		handler.fail(writer, http.StatusPreconditionFailed, "policy revision changed")
		return
	}
	var input policyInput
	if err := decodeJSON(writer, request, &input); err != nil {
		handler.fail(writer, 400, "invalid request body")
		return
	}
	input.ID = request.PathValue("id")
	if current.LegacyUnrepresentable && input.Columns != nil && input.ColumnPermissions == nil {
		handler.fail(writer, http.StatusConflict, "legacy columns cannot represent the current policy")
		return
	}
	if err := validatePolicyInput(input); err != nil {
		handler.fail(writer, http.StatusUnprocessableEntity, "invalid policy")
		return
	}
	normalizePolicyRepresentations(&input)
	policyModel := policyFromInput(input)
	policyModel.LegacyUnrepresentable = input.ColumnPermissions != nil && input.Columns == nil
	updated, err := handler.deps.Runtime.Store.Policies().UpdateIfRevision(request.Context(), policyModel, expected)
	if err != nil {
		if errors.Is(err, store.ErrRevisionMismatch) {
			handler.fail(writer, http.StatusPreconditionFailed, "policy revision changed")
			return
		}
		handler.notFound(writer)
		return
	}
	writer.Header().Set("ETag", policyETag(updated.ID, updated.Revision))
	handler.ok(writer, policyToView(updated))
}
func (handler *Handler) policiesDelete(writer http.ResponseWriter, request *http.Request) {
	id, expected, status := parsePolicyIfMatch(request)
	if status != 0 {
		handler.fail(writer, status, http.StatusText(status))
		return
	}
	if id != request.PathValue("id") {
		handler.fail(writer, http.StatusBadRequest, "If-Match policy does not match request path")
		return
	}
	if err := handler.deps.Runtime.Store.Policies().DeleteIfRevision(request.Context(), id, expected); err != nil {
		if errors.Is(err, store.ErrRevisionMismatch) {
			handler.fail(writer, http.StatusPreconditionFailed, "policy revision changed")
			return
		}
		handler.notFound(writer)
		return
	}
	handler.ok(writer, map[string]bool{"deleted": true})
}

func policyETag(id string, revision int64) string {
	return `"policy-` + base64.RawURLEncoding.EncodeToString([]byte(id)) + `-r` + strconv.FormatInt(revision, 10) + `"`
}

func parsePolicyIfMatch(request *http.Request) (string, int64, int) {
	values := request.Header.Values("If-Match")
	if len(values) == 0 {
		return "", 0, http.StatusPreconditionRequired
	}
	if len(values) != 1 {
		return "", 0, http.StatusBadRequest
	}
	value := values[0]
	if len(value) < len(`"policy--r1"`) || value[0] != '"' || value[len(value)-1] != '"' || strings.ContainsAny(value, " \t\r\n,") || strings.HasPrefix(value, `W/`) {
		return "", 0, http.StatusBadRequest
	}
	body := value[1 : len(value)-1]
	if !strings.HasPrefix(body, "policy-") {
		return "", 0, http.StatusBadRequest
	}
	encodedAndRevision := strings.TrimPrefix(body, "policy-")
	separator := strings.LastIndex(encodedAndRevision, "-r")
	if separator <= 0 {
		return "", 0, http.StatusBadRequest
	}
	encoded, revisionText := encodedAndRevision[:separator], encodedAndRevision[separator+2:]
	if revisionText == "" || revisionText[0] == '0' {
		return "", 0, http.StatusBadRequest
	}
	revision, err := strconv.ParseInt(revisionText, 10, 64)
	if err != nil || revision <= 0 {
		return "", 0, http.StatusBadRequest
	}
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(decoded) == 0 || base64.RawURLEncoding.EncodeToString(decoded) != encoded {
		return "", 0, http.StatusBadRequest
	}
	id := string(decoded)
	if !utf8.ValidString(id) || policyETag(id, revision) != value {
		return "", 0, http.StatusBadRequest
	}
	return id, revision, 0
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
	if !safeMaskScopeIdentifier(input.SchemaName) || !safeMaskScopeIdentifier(input.TableName) {
		return fmt.Errorf("schema_name or table_name is not a valid external identifier")
	}
	if input.SchemaName != "" && input.TableName == "" {
		return fmt.Errorf("schema_name must be provided together with table_name")
	}
	return mask.ValidateRule(maskRuleFromInput(input))
}

func maskRuleFromInput(input maskRuleInput) mask.Rule {
	return mask.Rule{
		Column:        input.ColumnName,
		SensitiveType: mask.SensitiveType(input.SensitiveType),
		Algorithm:     mask.Algorithm(input.Algo),
		Range:         rangeParamsFromInput(input),
	}
}

func rangeParamsFromInput(input maskRuleInput) *mask.RangeParams {
	if input.Algo != string(mask.AlgoRange) && input.RangeBucketWidth == nil &&
		input.RangeBucketOffset == nil && input.RangeGranularity == nil {
		return nil
	}
	params := &mask.RangeParams{}
	if input.RangeBucketWidth != nil {
		value := *input.RangeBucketWidth
		params.BucketWidth = &value
	}
	if input.RangeBucketOffset != nil {
		value := *input.RangeBucketOffset
		params.BucketOffset = &value
	}
	if input.RangeGranularity != nil {
		value := mask.RangeGranularity(*input.RangeGranularity)
		params.Granularity = &value
	}
	return params
}

func maskRuleModelFromInput(input maskRuleInput, enabled bool) model.MaskRule {
	rule := model.MaskRule{
		ID: input.ID, DatasourceID: input.DatasourceID, SchemaName: input.SchemaName, TableName: input.TableName,
		ColumnName: input.ColumnName, SensitiveType: input.SensitiveType, Algo: input.Algo,
		Enabled: enabled,
	}
	params := rangeParamsFromInput(input)
	if params == nil {
		return rule
	}
	if params.BucketWidth != nil {
		value := *params.BucketWidth
		rule.RangeBucketWidth = &value
	}
	if params.BucketOffset != nil {
		value := *params.BucketOffset
		rule.RangeBucketOffset = &value
	}
	if params.Granularity != nil {
		value := string(*params.Granularity)
		rule.RangeGranularity = &value
	}
	return rule
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
	if input.Algo == string(mask.AlgoRange) {
		switch mask.SensitiveType(input.SensitiveType) {
		case mask.TypeNumber:
			if input.RangeBucketOffset == nil {
				value := int64(0)
				input.RangeBucketOffset = &value
			}
		case mask.TypeDate:
			if input.RangeGranularity == nil {
				value := string(mask.RangeYear)
				input.RangeGranularity = &value
			}
		}
	}
	return input
}

func (handler *Handler) validateMaskActivation(writer http.ResponseWriter, rule mask.Rule) bool {
	err := handler.deps.Runtime.ValidateRedactionActivation(rule)
	if err == nil {
		return true
	}
	if errors.Is(err, mask.ErrHashKeyRequired) {
		handler.maskRuleFailure(writer, http.StatusServiceUnavailable, "HASH_REDACTION_UNAVAILABLE", "hash redaction is unavailable")
		return false
	}
	handler.maskRuleFailure(writer, http.StatusUnprocessableEntity, "INVALID_MASK_RULE", "invalid mask rule")
	return false
}

type maskRuleConflictKind uint8

const (
	maskRuleNoConflict maskRuleConflictKind = iota
	maskRulePhysicalConflict
	maskRuleAlgorithmScopeConflict
)

func (handler *Handler) maskRuleConflict(
	ctx context.Context,
	candidate model.MaskRule,
	excludeID string,
) (maskRuleConflictKind, error) {
	rules, err := handler.deps.Runtime.Store.MaskRules().List(ctx)
	if err != nil {
		return maskRuleNoConflict, err
	}
	for _, rule := range rules {
		if rule.ID == excludeID || !sameMaskScope(rule.DatasourceID, candidate.DatasourceID) {
			continue
		}
		if mask.NormalizeColumnName(rule.ColumnName) != mask.NormalizeColumnName(candidate.ColumnName) {
			continue
		}
		if rule.SchemaName == candidate.SchemaName && rule.TableName == candidate.TableName {
			return maskRulePhysicalConflict, nil
		}
	}
	if !candidate.Enabled {
		return maskRuleNoConflict, nil
	}
	for _, rule := range rules {
		if rule.ID == excludeID || !rule.Enabled || !maskScopesOverlap(rule.DatasourceID, candidate.DatasourceID) ||
			mask.NormalizeColumnName(rule.ColumnName) != mask.NormalizeColumnName(candidate.ColumnName) {
			continue
		}
		oneGlobalOneTable := isGlobalColumnMaskRule(rule) != isGlobalColumnMaskRule(candidate) &&
			(isGlobalColumnMaskRule(rule) || isGlobalColumnMaskRule(candidate))
		if oneGlobalOneTable && !sameMaskAlgorithm(rule, candidate) {
			return maskRuleAlgorithmScopeConflict, nil
		}
	}
	return maskRuleNoConflict, nil
}

func isGlobalColumnMaskRule(rule model.MaskRule) bool {
	return rule.SchemaName == "" && rule.TableName == ""
}

func sameMaskAlgorithm(left, right model.MaskRule) bool {
	if left.Algo != right.Algo {
		return false
	}
	if left.Algo != string(mask.AlgoRange) {
		return true
	}
	if left.SensitiveType != right.SensitiveType {
		return false
	}
	switch mask.SensitiveType(left.SensitiveType) {
	case mask.TypeNumber:
		return equalOptionalInt64(left.RangeBucketWidth, right.RangeBucketWidth) &&
			optionalInt64(left.RangeBucketOffset, 0) == optionalInt64(right.RangeBucketOffset, 0)
	case mask.TypeDate:
		return optionalString(left.RangeGranularity, string(mask.RangeYear)) ==
			optionalString(right.RangeGranularity, string(mask.RangeYear))
	default:
		return false
	}
}

func equalOptionalInt64(left, right *int64) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func optionalInt64(value *int64, fallback int64) int64 {
	if value == nil {
		return fallback
	}
	return *value
}

func optionalString(value *string, fallback string) string {
	if value == nil {
		return fallback
	}
	return *value
}

func sameMaskScope(left, right *string) bool {
	return normalizedMaskScope(left) == normalizedMaskScope(right)
}

func maskScopesOverlap(left, right *string) bool {
	leftScope, rightScope := normalizedMaskScope(left), normalizedMaskScope(right)
	return leftScope == "" || rightScope == "" || leftScope == rightScope
}

func normalizedMaskScope(scope *string) string {
	if scope == nil {
		return ""
	}
	return strings.TrimSpace(*scope)
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
		handler.maskRuleFailure(writer, http.StatusUnprocessableEntity, "INVALID_MASK_RULE", err.Error())
		return
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	if enabled {
		if !handler.validateMaskActivation(writer, maskRuleFromInput(input)) {
			return
		}
	}
	candidate := maskRuleModelFromInput(input, enabled)
	conflict, err := handler.maskRuleConflict(request.Context(), candidate, "")
	if err != nil {
		handler.internal(writer, err)
		return
	}
	if conflict == maskRulePhysicalConflict {
		handler.maskRuleFailure(writer, http.StatusConflict, "MASK_RULE_CONFLICT", "mask rule conflicts with an existing scope and column")
		return
	}
	if conflict == maskRuleAlgorithmScopeConflict {
		handler.maskRuleFailure(writer, http.StatusConflict, "MASK_RULE_SCOPE_CONFLICT", "global and table-level mask rules for this column use different algorithms; align the algorithms or delete one rule")
		return
	}
	created, err := handler.deps.Runtime.Store.MaskRules().Create(request.Context(), candidate)
	if err != nil {
		if store.IsMaskRuleConflict(err) {
			handler.maskRuleFailure(writer, http.StatusConflict, "MASK_RULE_CONFLICT", "mask rule conflicts with an existing scope and column")
			return
		}
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
	current, err := handler.deps.Runtime.Store.MaskRules().Get(request.Context(), input.ID)
	if err != nil {
		handler.notFound(writer)
		return
	}
	if err := validateMaskInput(input); err != nil {
		handler.maskRuleFailure(writer, http.StatusUnprocessableEntity, "INVALID_MASK_RULE", err.Error())
		return
	}
	enabled := current.Enabled
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	if enabled {
		if !handler.validateMaskActivation(writer, maskRuleFromInput(input)) {
			return
		}
	}
	candidate := maskRuleModelFromInput(input, enabled)
	conflict, err := handler.maskRuleConflict(request.Context(), candidate, input.ID)
	if err != nil {
		handler.internal(writer, err)
		return
	}
	if conflict == maskRulePhysicalConflict {
		handler.maskRuleFailure(writer, http.StatusConflict, "MASK_RULE_CONFLICT", "mask rule conflicts with an existing scope and column")
		return
	}
	if conflict == maskRuleAlgorithmScopeConflict {
		handler.maskRuleFailure(writer, http.StatusConflict, "MASK_RULE_SCOPE_CONFLICT", "global and table-level mask rules for this column use different algorithms; align the algorithms or delete one rule")
		return
	}
	updated, err := handler.deps.Runtime.Store.MaskRules().Update(request.Context(), candidate)
	if err != nil {
		if store.IsMaskRuleConflict(err) {
			handler.maskRuleFailure(writer, http.StatusConflict, "MASK_RULE_CONFLICT", "mask rule conflicts with an existing scope and column")
			return
		}
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
	format := request.URL.Query().Get("format")
	if format != "" && format != "jsonl" && format != "csv" {
		handler.fail(writer, http.StatusBadRequest, "format must be csv or jsonl")
		return
	}
	filter, err := auditFilterFromRequest(request)
	if err != nil {
		handler.fail(writer, http.StatusBadRequest, err.Error())
		return
	}
	logs, err := handler.collectAuditExportLogs(request.Context(), filter)
	if err != nil {
		if errors.Is(err, errAuditExportLimit) {
			handler.fail(writer, http.StatusUnprocessableEntity, errAuditExportLimit.Error())
			return
		}
		handler.internal(writer, err)
		return
	}

	var body []byte
	contentType := "application/x-ndjson"
	contentDisposition := "attachment; filename=agentsql-audit.jsonl"
	if format == "csv" {
		body, err = renderAuditCSV(logs)
		contentType = "text/csv; charset=utf-8"
		contentDisposition = "attachment; filename=agentsql-audit.csv"
	} else {
		var buffer bytes.Buffer
		encoder := json.NewEncoder(&buffer)
		for _, log := range logs {
			if encodeErr := encoder.Encode(auditToView(log)); encodeErr != nil {
				err = encodeErr
				break
			}
		}
		body = buffer.Bytes()
	}
	if err != nil {
		handler.internal(writer, err)
		return
	}

	writer.Header().Set("Content-Type", contentType)
	writer.Header().Set("Content-Disposition", contentDisposition)
	writer.Header().Set("Cache-Control", "no-store")
	written, writeErr := writer.Write(body)
	if writeErr != nil || written != len(body) {
		return
	}
	if transactional, ok := writer.(interface{ AfterCommit(func()) }); ok {
		transactional.AfterCommit(func() {
			handler.recordAuditExportTrail(request, normalizedAuditExportFormat(format), len(logs), filter)
		})
		return
	}
	handler.recordAuditExportTrail(request, normalizedAuditExportFormat(format), len(logs), filter)
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
func (handler *Handler) maskRuleFailure(writer http.ResponseWriter, status int, code, message string) {
	handler.write(writer, status, status, message, struct {
		ErrorCode string `json:"error_code"`
	}{ErrorCode: code})
}
func (handler *Handler) notFound(writer http.ResponseWriter) {
	handler.fail(writer, http.StatusNotFound, "not found")
}
func (handler *Handler) internal(writer http.ResponseWriter, err error) {
	handler.logger.Error().Str("error_type", fmt.Sprintf("%T", err)).Msg("admin API internal error")
	handler.fail(writer, http.StatusInternalServerError, "internal error")
}
func (handler *Handler) refreshNotificationNames(ctx context.Context) {
	if err := handler.deps.Runtime.RefreshNotificationNames(ctx); err != nil {
		handler.logger.Warn().Str("error_type", fmt.Sprintf("%T", err)).
			Msg("notification name cache refresh failed; retaining previous snapshot")
	}
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
