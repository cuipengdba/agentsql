package mcpserver

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// uniqueModernMetadataMiddleware keeps this route exclusive to 2026-07-28.
// The SDK also narrows negotiation/discovery through SupportedProtocolVersions;
// this gate prevents a headerless legacy initialize from reaching that code.
func uniqueModernMetadataMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			next.ServeHTTP(writer, request)
			return
		}
		for _, name := range []string{"Authorization", "Mcp-Protocol-Version", "Mcp-Method", "Mcp-Name"} {
			if len(request.Header.Values(name)) > 1 {
				writeModernRPCError(writer, nil, http.StatusBadRequest, mcp.CodeHeaderMismatch, "ambiguous MCP request headers", nil)
				return
			}
		}
		headerVersion := request.Header.Get("Mcp-Protocol-Version")
		if headerVersion == "" {
			writeModernRPCError(writer, nil, http.StatusBadRequest, mcp.CodeHeaderMismatch, "Mcp-Protocol-Version header is required", nil)
			return
		}
		if headerVersion != modernMCPProtocolVersion {
			writeModernRPCError(writer, nil, http.StatusBadRequest, mcp.CodeUnsupportedProtocolVersion,
				"unsupported MCP protocol version", mcp.UnsupportedProtocolVersionData{
					Supported: []string{modernMCPProtocolVersion}, Requested: headerVersion,
				})
			return
		}
		if request.Body == nil {
			writeHTTPError(writer, http.StatusBadRequest, "invalid MCP request body")
			return
		}
		body, err := io.ReadAll(io.LimitReader(request.Body, maxMCPRequestBodyBytes+1))
		_ = request.Body.Close()
		if len(body) > maxMCPRequestBodyBytes {
			writeHTTPError(writer, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		if err != nil {
			writeHTTPError(writer, http.StatusBadRequest, "invalid MCP request body")
			return
		}
		request.Body = io.NopCloser(bytes.NewReader(body))
		if code, message := validateModernEnvelope(body); code != 0 {
			writeModernRPCError(writer, body, http.StatusBadRequest, code, message, nil)
			return
		}
		// The 2026-07-28 transport ignores these legacy session/replay headers.
		// go-sdk v1.8.0 still rejects Last-Event-ID on POST before dispatch, so
		// remove both only on this dedicated modern route.
		request.Header = request.Header.Clone()
		request.Header.Del("Mcp-Session-Id")
		request.Header.Del("Last-Event-ID")
		next.ServeHTTP(writer, request)
	})
}

func validateModernEnvelope(body []byte) (int, string) {
	envelope, duplicate, ok := uniqueJSONObject(body)
	if duplicate {
		return -32600, "ambiguous MCP request metadata"
	}
	if !ok {
		return -32700, "invalid MCP request body"
	}
	var method string
	if err := json.Unmarshal(envelope["method"], &method); err != nil ||
		method == "" || method == "initialize" || strings.HasPrefix(method, "notifications/") {
		return -32600, "unsupported MCP method on stateless route"
	}
	params, duplicate, ok := uniqueJSONObject(envelope["params"])
	if duplicate {
		return -32600, "ambiguous MCP request metadata"
	}
	if !ok {
		return -32602, "missing MCP request params"
	}
	meta, duplicate, ok := uniqueJSONObject(params["_meta"])
	if duplicate {
		return -32600, "ambiguous MCP request metadata"
	}
	if !ok {
		return -32602, "missing MCP request metadata"
	}
	var version string
	if err := json.Unmarshal(meta["io.modelcontextprotocol/protocolVersion"], &version); err != nil || version != modernMCPProtocolVersion {
		return mcp.CodeHeaderMismatch, "Mcp-Protocol-Version header does not match request metadata"
	}
	return 0, ""
}

// A structured modern error lets clients distinguish a version negotiation
// failure from an old server that does not implement server/discover at all.
func writeModernRPCError(writer http.ResponseWriter, body []byte, status, code int, message string, data any) {
	id := json.RawMessage("null")
	if envelope, duplicate, ok := uniqueJSONObject(body); ok && !duplicate {
		if raw := envelope["id"]; len(raw) != 0 && json.Valid(raw) {
			var value any
			if err := json.Unmarshal(raw, &value); err == nil {
				switch value.(type) {
				case string, float64:
					id = raw
				}
			}
		}
	}
	response := struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    any    `json:"data,omitempty"`
		} `json:"error"`
	}{JSONRPC: "2.0", ID: id}
	response.Error.Code = code
	response.Error.Message = message
	response.Error.Data = data
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(response)
}

// uniqueJSONObject checks only one object level. Nested tool arguments retain
// the SDK's ordinary JSON contract and are never interpreted as protocol data.
func uniqueJSONObject(raw []byte) (map[string]json.RawMessage, bool, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return nil, false, false
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return nil, false, false
		}
		name, ok := key.(string)
		if !ok {
			return nil, false, false
		}
		if _, exists := fields[name]; exists {
			return nil, true, true
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, false, false
		}
		fields[name] = value
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return nil, false, false
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, false, false
	}
	return fields, false, true
}
