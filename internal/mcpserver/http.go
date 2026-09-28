package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cuipengdba/agentsql/internal/auth"
	authorizedexecute "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/bootstrap"
	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/metrics"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/version"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rs/zerolog"
	"golang.org/x/time/rate"
)

const (
	agentServerRegistryLimit = 256
	maxMCPRequestBodyBytes   = 4 << 20
)

type requestIdentityContextKey struct{}

type requestIdentity struct {
	agent    model.Agent
	plainKey string
}

type agentServerRegistry struct {
	mu       sync.RWMutex
	servers  map[string]*mcp.Server
	limiters map[string]*rate.Limiter
	runtime  *bootstrap.Runtime
	logger   zerolog.Logger
	qps      int
	built    int
	b5       B5Options
}

func newAgentServerRegistry(
	runtime *bootstrap.Runtime,
	logger zerolog.Logger,
	qps int,
	b5Options ...B5Options,
) *agentServerRegistry {
	var b5 B5Options
	if len(b5Options) != 0 {
		b5 = b5Options[0]
	}
	return &agentServerRegistry{
		servers:  make(map[string]*mcp.Server),
		limiters: make(map[string]*rate.Limiter),
		runtime:  runtime,
		logger:   logger,
		qps:      qps,
		b5:       b5,
	}
}

func (registry *agentServerRegistry) getOrCreate(
	agent model.Agent,
	plainKey string,
) (*mcp.Server, error) {
	key := agentServerKey(agent)
	registry.mu.RLock()
	server := registry.servers[key]
	registry.mu.RUnlock()
	if server != nil {
		return server, nil
	}

	registry.mu.Lock()
	defer registry.mu.Unlock()
	if server = registry.servers[key]; server != nil {
		return server, nil
	}
	registry.resetServersAtCapacityLocked(key)
	bound, err := buildBoundServerWithVersion(agent, plainKey, registry.runtime, registry.logger, version.Version, registry.b5)
	if err != nil {
		return nil, err
	}
	registry.servers[key] = bound.sdk
	registry.built++
	return bound.sdk, nil
}

func (registry *agentServerRegistry) allow(agent model.Agent) bool {
	key := agent.ID
	registry.mu.RLock()
	limiter := registry.limiters[key]
	registry.mu.RUnlock()
	if limiter != nil {
		return limiter.Allow()
	}

	registry.mu.Lock()
	defer registry.mu.Unlock()
	if limiter = registry.limiters[key]; limiter == nil {
		registry.resetLimitersAtCapacityLocked(key)
		// v0.1 uses QPS as both steady-state rate and burst capacity.
		limiter = rate.NewLimiter(rate.Limit(registry.qps), registry.qps)
		registry.limiters[key] = limiter
	}
	return limiter.Allow()
}

func (registry *agentServerRegistry) resetServersAtCapacityLocked(incomingKey string) {
	if _, exists := registry.servers[incomingKey]; exists || len(registry.servers) < agentServerRegistryLimit {
		return
	}
	// v0.1 uses bounded, batch eviction. Requests already holding a server
	// pointer continue safely; later requests rebuild entries.
	registry.servers = make(map[string]*mcp.Server)
}

func (registry *agentServerRegistry) resetLimitersAtCapacityLocked(incomingKey string) {
	if _, exists := registry.limiters[incomingKey]; exists || len(registry.limiters) < agentServerRegistryLimit {
		return
	}
	// Limiters use the same bounded, batch eviction strategy as server entries.
	registry.limiters = make(map[string]*rate.Limiter)
}

func (registry *agentServerRegistry) serverCount() int {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	return len(registry.servers)
}

func (registry *agentServerRegistry) buildCount() int {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	return registry.built
}

func agentServerKey(agent model.Agent) string {
	return agent.ID + "|" + agent.APIKeyHash
}

// NewHTTPHandler creates the stateless multi-tenant Streamable HTTP endpoint.
func NewHTTPHandler(
	runtime *bootstrap.Runtime,
	cfg config.Config,
	logger zerolog.Logger,
	options ...HTTPOption,
) (http.Handler, error) {
	handler, _, err := newHTTPHandlerWithRegistry(runtime, cfg, logger, options...)
	return handler, err
}

type httpHandlerOptions struct {
	adminAPI   http.Handler
	webConsole http.Handler
	b5         B5Options
}

// HTTPOption extends the T15 mux without changing its default routes.
type HTTPOption func(*httpHandlerOptions)

// WithAdminAPI mounts the T16 management API below /api/v1/.
func WithAdminAPI(handler http.Handler) HTTPOption {
	return func(options *httpHandlerOptions) {
		if handler != nil {
			options.adminAPI = handler
		}
	}
}

// WithWebConsole mounts the embedded T17 single-page application at /.
func WithWebConsole(handler http.Handler) HTTPOption {
	return func(options *httpHandlerOptions) {
		if handler != nil {
			options.webConsole = handler
		}
	}
}

// WithB5Sessions installs the production B5 surface selected by bootstrap.
// The zero value remains feature-off so component tests and explicit rollback
// configurations preserve the pre-B5 server surface.
func WithB5Sessions(options B5Options) HTTPOption {
	return func(configuration *httpHandlerOptions) {
		configuration.b5 = options
	}
}

func newHTTPHandlerWithRegistry(
	runtime *bootstrap.Runtime,
	cfg config.Config,
	logger zerolog.Logger,
	options ...HTTPOption,
) (http.Handler, *agentServerRegistry, error) {
	if runtime == nil || runtime.Store == nil || runtime.Pipeline == nil {
		return nil, nil, fmt.Errorf("create MCP HTTP handler: runtime is incomplete")
	}
	if err := cfg.Validate(); err != nil {
		return nil, nil, fmt.Errorf("create MCP HTTP handler: invalid configuration: %w", err)
	}
	if cfg.Defaults.QPSPerAgent <= 0 {
		return nil, nil, fmt.Errorf("create MCP HTTP handler: QPS must be positive")
	}
	// Runtime owns the immutable redaction assembly. HTTP closures must not keep
	// either legacy or manifest secret sources alive.
	cfg.Redaction.HashKey = ""
	cfg.Redaction.HashKeys = nil

	resolvedOptions := &httpHandlerOptions{}
	for _, option := range options {
		if option != nil {
			option(resolvedOptions)
		}
	}
	if err := resolvedOptions.b5.validate(); err != nil {
		return nil, nil, fmt.Errorf("create MCP HTTP handler: %w", err)
	}
	registry := newAgentServerRegistry(runtime, logger, cfg.Defaults.QPSPerAgent, resolvedOptions.b5)
	getServer := func(request *http.Request) *mcp.Server {
		identity, ok := identityFromContext(request.Context())
		if !ok {
			logger.Error().Msg("MCP request identity missing before SDK dispatch")
			return nil
		}
		server, err := registry.getOrCreate(identity.agent, identity.plainKey)
		if err != nil {
			logger.Error().Str("error_type", fmt.Sprintf("%T", err)).Msg("build bound MCP server")
			return nil
		}
		return server
	}
	sdkLogger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sdkHandler := mcp.NewStreamableHTTPHandler(
		getServer,
		&mcp.StreamableHTTPOptions{
			Stateless:           true,
			JSONResponse:        true,
			MaxRequestBodyBytes: maxMCPRequestBodyBytes,
			// Reviewed legacy protocols do not bind disconnect to request
			// cancellation. The coordinator's independent 300ms watchdog owns
			// cancel-taint and rollback/discard decisions.
			PropagateRequestCancellation: false,
			Logger:                       sdkLogger,
		},
	)
	authenticator := auth.NewAuthenticator(runtime.Store.Agents())
	mcpHandler := authMiddleware(
		authenticator,
		rateMiddleware(
			registry,
			recoverMiddleware(
				logger,
				accessLogMiddleware(logger, protocolVersionMiddleware(sdkHandler)),
			),
		),
	)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthHandler(cfg, runtime))
	mux.HandleFunc("GET /readyz", readinessHandler(runtime))
	mux.HandleFunc("GET /metrics", metricsEndpoint(runtime))
	mux.Handle("/mcp", authorizedexecute.SealedHTTP(mcpHandler, authorizedexecute.DefaultLimits.EnvelopeBytes))
	if resolvedOptions.adminAPI != nil {
		mux.Handle("/api/v1/", resolvedOptions.adminAPI)
	}
	if resolvedOptions.webConsole != nil {
		mux.Handle("/", authorizedexecute.SealedHTTP(resolvedOptions.webConsole, authorizedexecute.DefaultLimits.EnvelopeBytes))
	} else {
		mux.HandleFunc("/", func(writer http.ResponseWriter, _ *http.Request) {
			writeHTTPError(writer, http.StatusNotFound, "not found")
		})
	}
	return metricsMiddleware(runtime.Metrics, mux), registry, nil
}

type probeResponse struct {
	Status               string              `json:"status"`
	Version              string              `json:"version,omitempty"`
	Demo                 *demoProbeResponse  `json:"demo,omitempty"`
	RedactionUnsatisfied []int               `json:"redaction_unsatisfied,omitempty"`
	B2                   bootstrap.B2Status  `json:"b2"`
	B5                   *bootstrap.B5Status `json:"b5,omitempty"`
}

type demoProbeResponse struct {
	Enabled bool   `json:"enabled"`
	Banner  string `json:"banner"`
}

func healthHandler(cfg config.Config, runtimes ...*bootstrap.Runtime) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		b2 := bootstrap.B2Status{State: bootstrap.B2StateFeatureOff, Reason: bootstrap.B2ReasonFeatureOff, Protocol: 2}
		if len(runtimes) != 0 && runtimes[0] != nil {
			b2 = runtimes[0].B2Status()
		}
		response := probeResponse{
			Status:  "ok",
			Version: version.Version,
			B2:      b2,
		}
		if len(runtimes) != 0 && runtimes[0] != nil {
			if status, available, err := runtimes[0].B5Status(request.Context()); available {
				if err != nil {
					status = bootstrap.B5Status{Enabled: true, State: "DEGRADED", Reason: "B5_STATUS_UNAVAILABLE", Ready: false}
				}
				response.B5 = &status
			}
		}
		if cfg.DemoEnabled() {
			banner := cfg.Demo.Banner
			if banner == "" {
				banner = config.DemoDefaultBanner
			}
			response.Demo = &demoProbeResponse{Enabled: true, Banner: banner}
		}
		writer.Header().Set("Cache-Control", "no-store")
		writeProbeResponse(writer, http.StatusOK, response)
	}
}

func readinessHandler(runtime *bootstrap.Runtime) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if runtime == nil || runtime.Store == nil {
			writeProbeResponse(writer, http.StatusServiceUnavailable, probeResponse{Status: "not ready", B2: bootstrap.B2Status{State: bootstrap.B2StateDegraded, Reason: bootstrap.B2ReasonMetadataUnavailable}})
			return
		}
		b2 := runtime.B2Status()
		ctx, cancel := context.WithTimeout(request.Context(), time.Second)
		defer cancel()
		var b5 *bootstrap.B5Status
		if status, available, err := runtime.B5Status(ctx); available {
			if err != nil {
				status = bootstrap.B5Status{Enabled: true, State: "DEGRADED", Reason: "B5_STATUS_UNAVAILABLE", Ready: false}
			}
			b5 = &status
		}
		if err := runtime.Store.Ping(ctx); err != nil {
			writeProbeResponse(writer, http.StatusServiceUnavailable, probeResponse{Status: "not ready", B2: b2, B5: b5})
			return
		}
		if !runtime.B2Ready() {
			writeProbeResponse(writer, http.StatusServiceUnavailable, probeResponse{Status: "not ready", B2: b2, B5: b5})
			return
		}
		if b5 != nil && !b5.Ready {
			writeProbeResponse(writer, http.StatusServiceUnavailable, probeResponse{Status: "not ready", B2: b2, B5: b5})
			return
		}
		if ready, unsatisfied := runtime.RedactionReady(); !ready {
			writeProbeResponse(writer, http.StatusServiceUnavailable, probeResponse{
				Status: "not ready", RedactionUnsatisfied: unsatisfied, B2: b2, B5: b5,
			})
			return
		}
		writeProbeResponse(writer, http.StatusOK, probeResponse{Status: "ready", B2: b2, B5: b5})
	}
}

func metricsEndpoint(runtime *bootstrap.Runtime) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if runtime == nil || runtime.Metrics == nil {
			http.Error(writer, "metrics unavailable", http.StatusServiceUnavailable)
			return
		}
		handler := runtime.Metrics.Handler()
		if handler == nil {
			http.Error(writer, "metrics unavailable", http.StatusServiceUnavailable)
			return
		}
		handler.ServeHTTP(writer, request)
	}
}

func metricsMiddleware(hub *metrics.Metrics, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		started := time.Now()
		statusWriter := &statusResponseWriter{ResponseWriter: writer, status: http.StatusOK}
		defer func() {
			seconds := time.Since(started).Seconds()
			if seconds < 0 {
				seconds = 0
			}
			hub.ObserveHTTP(
				request.Method,
				classifyRoute(request.URL.Path),
				strconv.Itoa(statusWriter.status),
				seconds,
			)
		}()
		next.ServeHTTP(statusWriter, request)
	})
}

func classifyRoute(requestPath string) string {
	switch requestPath {
	case "/mcp", "/healthz", "/readyz", "/metrics":
		return requestPath
	}
	const prefix = "/api/v1/"
	if !strings.HasPrefix(requestPath, prefix) {
		return "/other"
	}
	segments := strings.Split(strings.Trim(strings.TrimPrefix(requestPath, prefix), "/"), "/")
	if len(segments) == 0 || segments[0] == "" || !knownAPIResource(segments[0]) {
		return "/other"
	}
	base := prefix + segments[0]
	if len(segments) == 1 {
		return base
	}
	if segments[0] == "b5" {
		return classifyB5Route(segments)
	}
	if action, ok := staticAPIAction(segments[0], segments[1]); ok {
		if len(segments) == 2 {
			return base + "/" + action
		}
		return "/other"
	}
	if !idAPIResource(segments[0]) {
		return "/other"
	}
	classified := base + "/{id}"
	if len(segments) == 2 {
		return classified
	}
	if len(segments) == 3 && knownIDAction(segments[0], segments[2]) {
		return classified + "/" + segments[2]
	}
	return "/other"
}

func idAPIResource(resource string) bool {
	switch resource {
	case "agents", "datasources", "policies", "rules", "mask_rules", "approvals":
		return true
	default:
		return false
	}
}

func knownAPIResource(resource string) bool {
	switch resource {
	case "auth", "agents", "datasources", "policies", "rules", "mask_rules",
		"audit", "approvals", "dashboard", "playground", "stream", "redaction", "b5":
		return true
	default:
		return false
	}
}

func classifyB5Route(segments []string) string {
	if len(segments) < 2 {
		return "/other"
	}
	view := segments[1]
	allowed := view == "status" || view == "sessions" || view == "transactions" || view == "quarantine" || view == "inventory" || view == "metrics" || view == "reconciliation"
	if !allowed {
		return "/other"
	}
	base := "/api/v1/b5/" + view
	if len(segments) == 2 {
		return base
	}
	if len(segments) == 3 && (view == "sessions" || view == "transactions") {
		return base + "/{id}"
	}
	if len(segments) == 4 && view == "quarantine" && segments[3] == "confirm-discard" {
		return base + "/{id}/confirm-discard"
	}
	return "/other"
}

func staticAPIAction(resource, segment string) (string, bool) {
	switch resource {
	case "auth":
		return segment, segment == "login" || segment == "me" || segment == "logout"
	case "audit":
		return segment, segment == "export"
	case "dashboard":
		return segment, segment == "summary"
	case "playground":
		return segment, segment == "assess"
	case "redaction":
		return segment, segment == "keys"
	default:
		return "", false
	}
}

func knownIDAction(resource, action string) bool {
	switch resource {
	case "agents":
		return action == "rotate-key"
	case "datasources":
		return action == "ping"
	case "approvals":
		return action == "decide"
	default:
		return false
	}
}

func writeProbeResponse(writer http.ResponseWriter, status int, response probeResponse) {
	contents, err := json.Marshal(response)
	if err != nil {
		writeHTTPError(writer, http.StatusInternalServerError, "internal error")
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_, _ = writer.Write(contents)
}

type identityAuthenticator interface {
	Authenticate(ctx context.Context, rawKey string) (model.Agent, error)
}

func authMiddleware(authenticator identityAuthenticator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		plainKey, ok := bearerKey(request.Header.Get("Authorization"))
		if !ok {
			writeHTTPError(writer, http.StatusUnauthorized, "unauthorized")
			return
		}
		agent, err := authenticator.Authenticate(request.Context(), plainKey)
		if err != nil || agent.Status != "active" || !validAgentLevel(agent.Level) {
			writeHTTPError(writer, http.StatusUnauthorized, "unauthorized")
			return
		}
		identity := requestIdentity{agent: cloneAgent(agent), plainKey: plainKey}
		ctx := context.WithValue(request.Context(), requestIdentityContextKey{}, identity)
		next.ServeHTTP(writer, request.WithContext(ctx))
	})
}

func rateMiddleware(registry *agentServerRegistry, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		identity, ok := identityFromContext(request.Context())
		if !ok {
			writeHTTPError(writer, http.StatusUnauthorized, "unauthorized")
			return
		}
		if !registry.allow(identity.agent) {
			if registry != nil && registry.runtime != nil {
				registry.runtime.Metrics.IncRejected("rate_limited")
			}
			writeHTTPError(writer, http.StatusTooManyRequests, "rate limited")
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func recoverMiddleware(logger zerolog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.Error().
					Str("panic_type", fmt.Sprintf("%T", recovered)).
					Bytes("stack", debug.Stack()).
					Msg("recovered MCP HTTP panic")
				writeHTTPError(writer, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(writer, request)
	})
}

func accessLogMiddleware(logger zerolog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		started := time.Now()
		statusWriter := &statusResponseWriter{ResponseWriter: writer, status: http.StatusOK}
		defer func() {
			agentID := ""
			if identity, ok := identityFromContext(request.Context()); ok {
				agentID = identity.agent.ID
			}
			logger.Info().
				Str("method", request.Method).
				Str("path", request.URL.Path).
				Str("agent_id", agentID).
				Int("status", statusWriter.status).
				Int64("latency_ms", time.Since(started).Milliseconds()).
				Msg("MCP HTTP request")
		}()
		next.ServeHTTP(statusWriter, request)
	})
}

type statusResponseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (writer *statusResponseWriter) WriteHeader(status int) {
	if writer.wroteHeader {
		return
	}
	writer.status = status
	writer.wroteHeader = true
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *statusResponseWriter) Write(body []byte) (int, error) {
	if !writer.wroteHeader {
		writer.WriteHeader(http.StatusOK)
	}
	return writer.ResponseWriter.Write(body)
}

func (writer *statusResponseWriter) Unwrap() http.ResponseWriter {
	return writer.ResponseWriter
}

func bearerKey(header string) (string, bool) {
	header = strings.TrimSpace(header)
	if !strings.HasPrefix(header, "Bearer ") {
		return "", false
	}
	rawKey := strings.TrimPrefix(header, "Bearer ")
	if rawKey != strings.TrimSpace(rawKey) {
		return "", false
	}
	plainKey := rawKey
	if plainKey == "" || strings.ContainsAny(plainKey, " \t\r\n") {
		return "", false
	}
	return plainKey, true
}

func identityFromContext(ctx context.Context) (requestIdentity, bool) {
	if ctx == nil {
		return requestIdentity{}, false
	}
	identity, ok := ctx.Value(requestIdentityContextKey{}).(requestIdentity)
	if !ok || strings.TrimSpace(identity.agent.ID) == "" || strings.TrimSpace(identity.plainKey) == "" {
		return requestIdentity{}, false
	}
	return identity, true
}

func validAgentLevel(level string) bool {
	switch level {
	case "readonly", "dml", "ddl":
		return true
	default:
		return false
	}
}

func writeHTTPError(writer http.ResponseWriter, status int, message string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_, _ = writer.Write([]byte(`{"error":"` + message + `"}`))
}
