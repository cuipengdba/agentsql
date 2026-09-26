//go:build ignore

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type empty struct{}

type observation struct {
	Version             string `json:"version"`
	Started             bool   `json:"started"`
	CanceledWithin700ms bool   `json:"canceled_within_700ms"`
	CancelLatencyMS     int64  `json:"cancel_latency_ms,omitempty"`
}

func main() {
	for _, version := range []string{"2025-06-18", "2025-11-25", "2026-07-28"} {
		obs, err := disconnectCase(version)
		if err != nil {
			log.Fatalf("%s: %v", version, err)
		}
		printJSON(obs)
	}
	unknownCases()
}

func disconnectCase(version string) (observation, error) {
	started := make(chan struct{})
	canceled := make(chan time.Time, 1)
	release := make(chan struct{})
	var once sync.Once
	server := mcp.NewServer(&mcp.Implementation{Name: "b5-s0", Version: "0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "block"}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, empty, error) {
		once.Do(func() { close(started) })
		select {
		case <-ctx.Done():
			canceled <- time.Now()
			return nil, empty{}, ctx.Err()
		case <-release:
			return &mcp.CallToolResult{}, empty{}, nil
		}
	})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true, PropagateRequestCancellation: true,
	})
	ts := httptest.NewServer(h)
	defer ts.Close()
	u, _ := url.Parse(ts.URL)
	conn, err := net.Dial("tcp", u.Host)
	if err != nil {
		return observation{}, err
	}
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"block","arguments":{}}}`
	if version >= "2026-07-28" {
		body = fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"block","arguments":{},"_meta":{"io.modelcontextprotocol/protocolVersion":%q,"io.modelcontextprotocol/clientInfo":{"name":"probe","version":"0"},"io.modelcontextprotocol/clientCapabilities":{}}}}`, version)
	}
	methodHeader := ""
	if version >= "2026-07-28" {
		methodHeader = "Mcp-Method: tools/call\r\nMcp-Name: block\r\n"
	}
	req := fmt.Sprintf("POST / HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nAccept: application/json, text/event-stream\r\nMCP-Protocol-Version: %s\r\n%sContent-Length: %d\r\nConnection: close\r\n\r\n%s", u.Host, version, methodHeader, len(body), body)
	if _, err := io.WriteString(conn, req); err != nil {
		return observation{}, err
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		resp, _ := io.ReadAll(conn)
		conn.Close()
		close(release)
		return observation{Version: version}, fmt.Errorf("handler did not start; response=%q", string(resp))
	}
	disconnected := time.Now()
	_ = conn.Close()
	obs := observation{Version: version, Started: true}
	select {
	case at := <-canceled:
		obs.CanceledWithin700ms = true
		obs.CancelLatencyMS = at.Sub(disconnected).Milliseconds()
	case <-time.After(700 * time.Millisecond):
		close(release)
	}
	return obs, nil
}

func unknownCases() {
	server := mcp.NewServer(&mcp.Implementation{Name: "b5-s0", Version: "0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "noop"}, func(context.Context, *mcp.CallToolRequest, empty) (*mcp.CallToolResult, empty, error) {
		return &mcp.CallToolResult{}, empty{}, nil
	})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	ts := httptest.NewServer(h)
	defer ts.Close()
	for _, tc := range []struct{ name, header, body string }{
		{"unknown_legacy_header", "2025-07-01", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"noop","arguments":{}}}`},
		{"unknown_future_header", "2099-01-01", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"noop","arguments":{},"_meta":{"io.modelcontextprotocol/protocolVersion":"2099-01-01","io.modelcontextprotocol/clientInfo":{"name":"probe","version":"0"},"io.modelcontextprotocol/clientCapabilities":{}}}}`},
		{"unknown_initialize_no_header", "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-07-01","capabilities":{},"clientInfo":{"name":"probe","version":"0"}}}`},
	} {
		req, _ := http.NewRequest(http.MethodPost, ts.URL, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if tc.header != "" {
			req.Header.Set("MCP-Protocol-Version", tc.header)
		}
		if tc.header >= "2026-07-28" {
			req.Header.Set("Mcp-Method", "tools/call")
			req.Header.Set("Mcp-Name", "noop")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			printJSON(map[string]any{"case": tc.name, "error": err.Error()})
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		var compact any
		if json.Unmarshal(b, &compact) != nil {
			compact = strings.TrimSpace(string(b))
		}
		printJSON(map[string]any{"case": tc.name, "status": resp.StatusCode, "body": compact})
	}
	_ = bufio.ErrInvalidUnreadByte // keep bufio linked out of accidental probe edits
}

func printJSON(v any) { b, _ := json.Marshal(v); fmt.Println(string(b)) }
