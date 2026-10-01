package mcpserver

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

type protocolEnvelope struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		ProtocolVersion string         `json:"protocolVersion"`
		Meta            map[string]any `json:"_meta"`
	} `json:"params"`
}

// protocolVersionMiddleware is deliberately before the SDK. This prevents
// go-sdk's unknown-legacy fallback from silently selecting 2025-11-25.
func protocolVersionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			next.ServeHTTP(writer, request)
			return
		}
		if request.ContentLength > maxMCPRequestBodyBytes {
			writeHTTPError(writer, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		if request.Body == nil {
			writeProtocolRejection(writer, nil)
			return
		}
		body, err := io.ReadAll(io.LimitReader(request.Body, maxMCPRequestBodyBytes+1))
		_ = request.Body.Close()
		if len(body) > maxMCPRequestBodyBytes {
			writeHTTPError(writer, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		if err != nil || len(body) == 0 {
			writeProtocolRejection(writer, nil)
			return
		}
		request.Body = io.NopCloser(bytes.NewReader(body))
		var envelope protocolEnvelope
		if json.Unmarshal(body, &envelope) != nil {
			// Let the SDK return its normal JSON-RPC parse error. The gate only
			// owns version negotiation, not general request validation.
			next.ServeHTTP(writer, request)
			return
		}
		versions := request.Header.Values("MCP-Protocol-Version")
		// The negotiated protocol version is carried in InitializeParams. The
		// HTTP header is required only on subsequent requests by MCP 2025-06-18,
		// and mainstream clients commonly omit it on the initialize POST.
		if len(versions) > 1 || len(versions) == 1 && versions[0] != legacyMCPProtocolVersion || len(versions) == 0 && envelope.Method != "initialize" {
			writeProtocolRejection(writer, envelope.ID)
			return
		}
		bodyVersion := envelope.Params.ProtocolVersion
		metaVersion, _ := envelope.Params.Meta["protocolVersion"].(string)
		if bodyVersion != "" && bodyVersion != legacyMCPProtocolVersion || metaVersion != "" && metaVersion != legacyMCPProtocolVersion || bodyVersion != "" && metaVersion != "" && bodyVersion != metaVersion {
			writeProtocolRejection(writer, envelope.ID)
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func writeProtocolRejection(writer http.ResponseWriter, id json.RawMessage) {
	if !json.Valid(id) || len(id) == 0 {
		id = json.RawMessage("null")
	}
	response := struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  ToolResponse    `json:"result"`
	}{JSONRPC: "2.0", ID: id, Result: b5ErrorResponse("MCP_PROTOCOL_UNSUPPORTED", nil)}
	contents, err := json.Marshal(response)
	if err != nil {
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(contents)
}
