package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/auth"
	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/bootstrap"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/version"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rs/zerolog"
)

// Options configures one authenticated stdio MCP server.
type Options struct {
	APIKey  string
	Runtime *bootstrap.Runtime
	Logger  zerolog.Logger
	Version string
	B5      B5Options
}

// Server owns the SDK server and the immutable bound-Agent handlers.
type Server struct {
	sdk      *mcp.Server
	handlers *toolHandlers
	b5       B5Options
}

// NewServer authenticates the stdio API key once and registers the base tools
// plus the production-selected B5 tools.
func NewServer(ctx context.Context, options Options) (*Server, error) {
	if ctx == nil {
		return nil, fmt.Errorf("create MCP server: context is required")
	}
	if strings.TrimSpace(options.APIKey) == "" {
		return nil, fmt.Errorf("create MCP server: API key is required")
	}
	if options.Runtime == nil || options.Runtime.Store == nil {
		return nil, fmt.Errorf("create MCP server: runtime is incomplete")
	}
	agent, err := auth.NewAuthenticator(options.Runtime.Store.Agents()).Authenticate(ctx, options.APIKey)
	if err != nil {
		return nil, fmt.Errorf("create MCP server: authentication failed")
	}
	serverVersion := strings.TrimSpace(options.Version)
	if serverVersion == "" {
		serverVersion = version.Version
	}
	return buildBoundServerWithVersion(
		agent,
		options.APIKey,
		options.Runtime,
		options.Logger,
		serverVersion,
		options.B5,
	)
}

func buildBoundServer(
	agent model.Agent,
	plainKey string,
	runtime *bootstrap.Runtime,
	logger zerolog.Logger,
) (*Server, error) {
	return buildBoundServerWithVersion(agent, plainKey, runtime, logger, version.Version, B5Options{})
}

func buildBoundServerWithVersion(
	agent model.Agent,
	plainKey string,
	runtime *bootstrap.Runtime,
	logger zerolog.Logger,
	version string,
	b5Options ...B5Options,
) (*Server, error) {
	if runtime == nil || runtime.Store == nil || runtime.Pipeline == nil {
		return nil, fmt.Errorf("build bound MCP server: runtime is incomplete")
	}
	if strings.TrimSpace(agent.ID) == "" || agent.Status != "active" {
		return nil, fmt.Errorf("build bound MCP server: Agent is invalid or inactive")
	}
	switch agent.Level {
	case "readonly", "dml", "ddl":
	default:
		return nil, fmt.Errorf("build bound MCP server: Agent level is invalid")
	}
	if strings.TrimSpace(plainKey) == "" {
		return nil, fmt.Errorf("build bound MCP server: API key is required")
	}
	var b5 B5Options
	if len(b5Options) != 0 {
		b5 = b5Options[0]
	}
	if err := b5.validate(); err != nil {
		return nil, fmt.Errorf("build bound MCP server: %w", err)
	}
	sdkServer := mcp.NewServer(&mcp.Implementation{Name: "agentsql", Version: version}, nil)
	handlers := &toolHandlers{
		runtime:   runtime,
		agent:     cloneAgent(agent),
		apiKey:    plainKey,
		logger:    logger,
		schemaFor: runtime.ListDatasourceSchema,
	}
	registerTools(sdkServer, handlers)
	if b5.B5Sessions {
		registerB5Tools(sdkServer, handlers, b5)
	}
	return &Server{sdk: sdkServer, handlers: handlers, b5: b5}, nil
}

// RunStdioStreams serves the sealed length-prefixed stdio protocol over the
// supplied streams. It exists so the exact production framing can be exercised
// end-to-end without replacing process-global stdin/stdout in tests.
func (server *Server) RunStdioStreams(ctx context.Context, reader io.ReadCloser, writer io.WriteCloser) error {
	if server == nil || server.sdk == nil || reader == nil || writer == nil {
		return errors.New("run MCP stdio streams: incomplete server or streams")
	}
	transport := &mcp.IOTransport{Reader: newSealedLineReader(reader, executor.DefaultLimits.EnvelopeBytes), Writer: newSealedLineWriter(writer, executor.DefaultLimits.EnvelopeBytes)}
	err := server.sdk.Run(ctx, legacyProtocolTransport{Transport: transport})
	if server.b5.B5Sessions && server.b5.Service != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		shutdownErr := server.b5.Service.Shutdown(shutdownCtx)
		cancel()
		err = errors.Join(err, shutdownErr)
	}
	return err
}

// RunStdio blocks while serving MCP JSON-RPC on stdin/stdout. This function
// itself never writes stdout; the SDK owns the protocol channel exclusively.
func RunStdio(ctx context.Context, options Options) error {
	server, err := NewServer(ctx, options)
	if err != nil {
		return err
	}
	serverVersion := strings.TrimSpace(options.Version)
	if serverVersion == "" {
		serverVersion = version.Version
	}
	options.Logger.Info().
		Str("agent_id", server.handlers.agent.ID).
		Str("version", serverVersion).
		Msg("MCP stdio server started")
	if err := server.RunStdioStreams(ctx, os.Stdin, os.Stdout); err != nil {
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
