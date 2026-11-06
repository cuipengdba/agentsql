package chatbi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// legacyMCPHandler provides the old HTTP+SSE transport expected by SQLBot's
// generated MCP clients. Each tool calls the same checked ChatBI HTTP route;
// the transport itself never grants data access or holds an authorization
// snapshot. The separate mcp_assistant HTTP route requires an operator-owned
// catalog registration; it is not an SSE tool and grants no anonymous access.
func legacyMCPHandler(h *Handler) http.Handler {
	seats := make(chan struct{}, 16)
	sse := mcp.NewSSEHandler(func(r *http.Request) *mcp.Server {
		return h.legacyMCPServer(r)
	}, &mcp.SSEOptions{MaxRequestBodyBytes: 65536})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
			parsed, err := url.Parse(origin)
			if err != nil || parsed.Host != r.Host || parsed.Scheme != "http" && parsed.Scheme != "https" {
				http.Error(w, "Cross-origin MCP request denied", http.StatusForbidden)
				return
			}
		}
		if r.Method == http.MethodGet {
			select {
			case seats <- struct{}{}:
				defer func() { <-seats }()
			default:
				http.Error(w, "Too many MCP sessions", http.StatusTooManyRequests)
				return
			}
			// Anonymous legacy clients must be able to obtain a token through a
			// tool, but may not hold scarce SSE slots forever.
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
			defer cancel()
			r = r.WithContext(ctx)
		}
		sse.ServeHTTP(w, r)
	})
}

func (h *Handler) legacyMCPServer(connection *http.Request) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "AgentSQL SQLBot compatibility MCP", Version: h.version}, nil)
	connectionToken := ""
	if len(connection.Header.Values("Authorization")) == 1 && len(connection.Header.Values("X-SQLBOT-TOKEN")) == 0 {
		connectionToken = connection.Header.Get("Authorization")
	} else if len(connection.Header.Values("X-SQLBOT-TOKEN")) == 1 && len(connection.Header.Values("Authorization")) == 0 {
		connectionToken = connection.Header.Get("X-SQLBOT-TOKEN")
	}
	type toolSpec struct {
		name, path string
		properties map[string]any
		required   []string
	}
	tools := []toolSpec{
		{"access_token", "/api/v1/mcp/access_token", map[string]any{"username": map[string]any{"type": "string"}, "password": map[string]any{"type": "string"}}, []string{"username", "password"}},
		{"mcp_start", "/api/v1/mcp/mcp_start", map[string]any{"token": map[string]any{"type": "string"}, "username": map[string]any{"type": "string"}, "password": map[string]any{"type": "string"}, "oid": map[string]any{"type": []string{"string", "integer"}}}, nil},
		{"mcp_ws_list", "/api/v1/mcp/mcp_ws_list", map[string]any{"token": map[string]any{"type": "string"}}, []string{"token"}},
		{"mcp_datasource_list", "/api/v1/mcp/mcp_ds_list", map[string]any{"token": map[string]any{"type": "string"}, "oid": map[string]any{"type": []string{"string", "integer"}}}, []string{"token"}},
		{"mcp_model_list", "/api/v1/mcp/mcp_model_list", map[string]any{"oid": map[string]any{"type": []string{"string", "integer"}}, "token": map[string]any{"type": "string"}}, []string{"oid"}},
		{"mcp_question", "/api/v1/mcp/mcp_question", map[string]any{"token": map[string]any{"type": "string"}, "chat_id": map[string]any{"type": []string{"string", "integer"}}, "question": map[string]any{"type": "string"}, "datasource_id": map[string]any{"type": []string{"string", "integer", "null"}}, "stream": map[string]any{"type": "boolean"}, "return_img": map[string]any{"type": "boolean"}, "lang": map[string]any{"type": "string"}, "custom_model": map[string]any{"type": []string{"integer", "null"}}}, []string{"token", "chat_id", "question"}},
	}
	for _, spec := range tools {
		spec := spec
		if spec.required == nil {
			spec.required = []string{}
		}
		server.AddTool(&mcp.Tool{Name: spec.name, Description: "SQLBot-compatible helper with AgentSQL authorization and audit", InputSchema: map[string]any{
			"type": "object", "properties": spec.properties, "required": spec.required,
		}}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return h.legacyMCPTool(ctx, spec.path, request.Params.Arguments, connectionToken)
		})
	}
	return server
}

func (h *Handler) legacyMCPTool(ctx context.Context, path string, arguments json.RawMessage, connectionToken string) (*mcp.CallToolResult, error) {
	if len(arguments) == 0 {
		arguments = []byte("{}")
	}
	if path == "/api/v1/mcp/mcp_ws_list" {
		var input struct {
			Token string `json:"token"`
		}
		if json.Unmarshal(arguments, &input) != nil || input.Token == "" {
			return legacyMCPError(http.StatusUnprocessableEntity), nil
		}
		path += "?token=" + url.QueryEscape(input.Token)
		arguments = []byte("null")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost"+path, bytes.NewReader(arguments))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.HasPrefix(path, "/api/v1/mcp/mcp_model_list") && connectionToken != "" {
		var input struct {
			Token string `json:"token"`
		}
		if json.Unmarshal(arguments, &input) != nil {
			return legacyMCPError(http.StatusUnprocessableEntity), nil
		}
		if input.Token == "" {
			req.Header.Set("X-SQLBOT-TOKEN", connectionToken)
		}
	}
	w := &legacyMCPRecorder{header: make(http.Header), status: http.StatusOK}
	h.mux.ServeHTTP(w, req)
	if w.overflow {
		return legacyMCPError(http.StatusRequestEntityTooLarge), nil
	}
	if w.status < 200 || w.status >= 300 {
		return legacyMCPError(w.status), nil
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strings.TrimSpace(w.body.String())}}}, nil
}

func legacyMCPError(status int) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("MCP request rejected (HTTP %d)", status)}}}
}

type legacyMCPRecorder struct {
	header   http.Header
	body     bytes.Buffer
	status   int
	overflow bool
}

func (w *legacyMCPRecorder) Header() http.Header    { return w.header }
func (w *legacyMCPRecorder) WriteHeader(status int) { w.status = status }
func (w *legacyMCPRecorder) Write(body []byte) (int, error) {
	if w.body.Len()+len(body) > 4<<20 {
		w.overflow = true
		return 0, errors.New("MCP response too large")
	}
	return w.body.Write(body)
}
