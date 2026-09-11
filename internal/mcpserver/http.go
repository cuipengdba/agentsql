package mcpserver

import (
	"context"
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
	"github.com/cuipengdba/agentsql/internal/bootstrap"
	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/model"
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
}

func newAgentServerRegistry(
	runtime *bootstrap.Runtime,
	logger zerolog.Logger,
	qps int,
) *agentServerRegistry {
	return &agentServerRegistry{
		servers:  make(map[string]*mcp.Server),
		limiters: make(map[string]*rate.Limiter),
		runtime:  runtime,
		logger:   logger,
		qps:      qps,
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
	registry.resetAtCapacityLocked(key)
	bound, err := buildBoundServer(agent, plainKey, registry.runtime, registry.logger)
	if err != nil {
		return nil, err
	}
	registry.servers[key] = bound.sdk
	registry.built++
	return bound.sdk, nil
}

func (registry *agentServerRegistry) allow(agent model.Agent) bool {
	key := agentServerKey(agent)
	registry.mu.RLock()
	limiter := registry.limiters[key]
	registry.mu.RUnlock()
	if limiter != nil {
		return limiter.Allow()
	}

	registry.mu.Lock()
	defer registry.mu.Unlock()
	if limiter = registry.limiters[key]; limiter == nil {
		registry.resetAtCapacityLocked(key)
		// v0.1 uses QPS as both steady-state rate and burst capacity.
		limiter = rate.NewLimiter(rate.Limit(registry.qps), registry.qps)
		registry.limiters[key] = limiter
	}
	return limiter.Allow()
}

func (registry *agentServerRegistry) resetAtCapacityLocked(incomingKey string) {
	_, serverExists := registry.servers[incomingKey]
	_, limiterExists := registry.limiters[incomingKey]
	if (serverExists || len(registry.servers) < agentServerRegistryLimit) &&
		(limiterExists || len(registry.limiters) < agentServerRegistryLimit) {
		return
	}
	// v0.1 deliberately clears the bounded cache wholesale. Requests already
	// holding a server pointer continue safely; later requests rebuild entries.
	registry.servers = make(map[string]*mcp.Server)
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
	if agent.UpdatedAt.IsZero() {
		return agent.ID
	}
	return agent.ID + "|" + strconv.FormatInt(agent.UpdatedAt.UnixNano(), 10)
}

// NewHTTPHandler creates the stateless multi-tenant Streamable HTTP endpoint.
func NewHTTPHandler(
	runtime *bootstrap.Runtime,
	cfg config.Config,
	logger zerolog.Logger,
) (http.Handler, error) {
	handler, _, err := newHTTPHandlerWithRegistry(runtime, cfg, logger)
	return handler, err
}

func newHTTPHandlerWithRegistry(
	runtime *bootstrap.Runtime,
	cfg config.Config,
	logger zerolog.Logger,
) (http.Handler, *agentServerRegistry, error) {
	if runtime == nil || runtime.Store == nil || runtime.Pipeline == nil || runtime.Executors == nil {
		return nil, nil, fmt.Errorf("create MCP HTTP handler: runtime is incomplete")
	}
	if err := cfg.Validate(); err != nil {
		return nil, nil, fmt.Errorf("create MCP HTTP handler: invalid configuration: %w", err)
	}
	if cfg.Defaults.QPSPerAgent <= 0 {
		return nil, nil, fmt.Errorf("create MCP HTTP handler: QPS must be positive")
	}

	registry := newAgentServerRegistry(runtime, logger, cfg.Defaults.QPSPerAgent)
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
			Stateless:                    true,
			JSONResponse:                 true,
			MaxRequestBodyBytes:          maxMCPRequestBodyBytes,
			PropagateRequestCancellation: true,
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
				accessLogMiddleware(logger, sdkHandler),
			),
		),
	)
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpHandler)
	// T16 mounts the management API under /api/v1. No route is exposed here.
	mux.HandleFunc("/", func(writer http.ResponseWriter, _ *http.Request) {
		writeHTTPError(writer, http.StatusNotFound, "not found")
	})
	return mux, registry, nil
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
