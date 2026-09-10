package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/cuipengdba/agentsql/internal/auth"
	"github.com/cuipengdba/agentsql/internal/bootstrap"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rs/zerolog"
)

// Options configures one authenticated stdio MCP server.
type Options struct {
	APIKey  string
	Runtime *bootstrap.Runtime
	Logger  zerolog.Logger
	Version string
}

// Server owns the SDK server and the immutable bound-Agent handlers.
type Server struct {
	sdk      *mcp.Server
	handlers *toolHandlers
}

// NewServer authenticates the stdio API key once and registers all seven tools.
func NewServer(ctx context.Context, options Options) (*Server, error) {
	if ctx == nil {
		return nil, fmt.Errorf("create MCP server: context is required")
	}
	if strings.TrimSpace(options.APIKey) == "" {
		return nil, fmt.Errorf("create MCP server: API key is required")
	}
	if options.Runtime == nil || options.Runtime.Store == nil ||
		options.Runtime.Pipeline == nil || options.Runtime.Executors == nil {
		return nil, fmt.Errorf("create MCP server: runtime is incomplete")
	}
	agent, err := auth.NewAuthenticator(options.Runtime.Store.Agents()).Authenticate(ctx, options.APIKey)
	if err != nil {
		return nil, fmt.Errorf("create MCP server: authentication failed")
	}
	switch agent.Level {
	case "readonly", "dml", "ddl":
	default:
		return nil, fmt.Errorf("create MCP server: authenticated Agent has invalid level")
	}
	version := strings.TrimSpace(options.Version)
	if version == "" {
		version = "dev"
	}
	sdkServer := mcp.NewServer(&mcp.Implementation{Name: "agentsql", Version: version}, nil)
	handlers := &toolHandlers{
		runtime:     options.Runtime,
		agent:       cloneAgent(agent),
		apiKey:      options.APIKey,
		logger:      options.Logger,
		executorFor: options.Runtime.ExecutorFor,
	}
	registerTools(sdkServer, handlers)
	return &Server{sdk: sdkServer, handlers: handlers}, nil
}

// RunStdio blocks while serving MCP JSON-RPC on stdin/stdout. This function
// itself never writes stdout; the SDK owns the protocol channel exclusively.
func RunStdio(ctx context.Context, options Options) error {
	server, err := NewServer(ctx, options)
	if err != nil {
		return err
	}
	options.Logger.Info().Str("agent_id", server.handlers.agent.ID).Msg("MCP stdio server started")
	if err := server.sdk.Run(ctx, &mcp.StdioTransport{}); err != nil {
		return fmt.Errorf("run MCP stdio server: %w", err)
	}
	return nil
}

func cloneAgent(agent model.Agent) model.Agent {
	cloned := agent
	if agent.Owner != nil {
		owner := *agent.Owner
		cloned.Owner = &owner
	}
	if agent.ExpiresAt != nil {
		expiresAt := *agent.ExpiresAt
		cloned.ExpiresAt = &expiresAt
	}
	return cloned
}
