package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/store"
)

const (
	oauthMCPPath      = "/mcp/oauth"
	oauthMetadataPath = "/.well-known/oauth-protected-resource/mcp/oauth"
	oauthAgentHeader  = "X-AgentSQL-Agent-Key"
)

// oauthAgentAuthMiddleware authenticates the second, Agent-side credential.
// The standard Authorization header remains exclusively for OAuth access
// tokens so MCP clients can complete their normal OAuth flow.
func oauthAgentAuthMiddleware(cfg config.MCPOAuthConfig, authenticator identityAuthenticator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		values := request.Header.Values(oauthAgentHeader)
		if len(values) != 1 || values[0] == "" || len(values[0]) > 512 ||
			strings.ContainsAny(values[0], " \t\r\n,") {
			oauthAgentRejected(writer, request, cfg)
			return
		}
		agent, err := authenticator.Authenticate(request.Context(), values[0])
		if err != nil || agent.Status != "active" || !validAgentLevel(agent.Level) {
			oauthAgentRejected(writer, request, cfg)
			return
		}
		tenantContext, err := store.WithTenant(request.Context(), agent.TenantID)
		if err != nil {
			oauthAgentRejected(writer, request, cfg)
			return
		}
		identity := requestIdentity{agent: cloneAgent(agent), plainKey: values[0]}
		ctx := context.WithValue(tenantContext, requestIdentityContextKey{}, identity)
		next.ServeHTTP(writer, request.WithContext(ctx))
	})
}

func oauthAgentRejected(writer http.ResponseWriter, request *http.Request, cfg config.MCPOAuthConfig) {
	if len(request.Header.Values("Authorization")) == 0 {
		oauthChallenge(writer, http.StatusUnauthorized, oauthMetadataURL(cfg.ResourceURL), cfg.RequiredScope, "")
		return
	}
	writeHTTPError(writer, http.StatusForbidden, "forbidden")
}

func oauthHumanMiddleware(cfg config.MCPOAuthConfig, verifier HumanDelegationVerifier, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		metadata := oauthMetadataURL(cfg.ResourceURL)
		identity, found := identityFromContext(request.Context())
		if !found || verifier == nil || len(request.Header.Values(humanDelegationHeader)) != 0 ||
			request.URL.Query().Has("access_token") {
			oauthChallenge(writer, http.StatusUnauthorized, metadata, cfg.RequiredScope, "")
			return
		}
		values := request.Header.Values("Authorization")
		if len(values) != 1 {
			oauthChallenge(writer, http.StatusUnauthorized, metadata, cfg.RequiredScope, "")
			return
		}
		token, ok := oauthBearer(values[0])
		if !ok {
			oauthChallenge(writer, http.StatusUnauthorized, metadata, cfg.RequiredScope, "")
			return
		}
		principal, err := verifier.Verify(request.Context(), token, identity.agent.ID)
		if errors.Is(err, errOAuthInsufficientScope) {
			oauthChallenge(writer, http.StatusForbidden, metadata, cfg.RequiredScope, "insufficient_scope")
			return
		}
		if errors.Is(err, errOAuthForbidden) {
			writeHTTPError(writer, http.StatusForbidden, "forbidden")
			return
		}
		if err != nil || principal.UserID == "" || principal.TenantID != identity.agent.TenantID ||
			identity.agent.TaskGrantID != "" && identity.agent.TaskGrantIssuerUserID != principal.UserID {
			oauthChallenge(writer, http.StatusUnauthorized, metadata, cfg.RequiredScope, "")
			return
		}
		human := delegatedHuman{userID: principal.UserID, tenantID: principal.TenantID}
		ctx := context.WithValue(request.Context(), delegatedHumanContextKey{}, human)
		next.ServeHTTP(writer, request.WithContext(ctx))
	})
}

func oauthBearer(header string) (string, bool) {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" ||
		strings.ContainsAny(parts[1], ",\r\n") {
		return "", false
	}
	return parts[1], true
}

func oauthMetadataURL(resource string) string {
	parsed, err := url.Parse(resource)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host + oauthMetadataPath
}

func oauthChallenge(writer http.ResponseWriter, status int, metadataURL, scope, oauthError string) {
	challenge := fmt.Sprintf("Bearer resource_metadata=%q, scope=%q", metadataURL, scope)
	if oauthError != "" {
		challenge += fmt.Sprintf(", error=%q", oauthError)
	}
	writer.Header().Set("WWW-Authenticate", challenge)
	writeHTTPError(writer, status, http.StatusText(status))
}

func oauthMetadataHandler(cfg config.MCPOAuthConfig) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "public, max-age=300")
		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"resource":                 cfg.ResourceURL,
			"authorization_servers":    []string{cfg.IssuerURL},
			"scopes_supported":         []string{cfg.RequiredScope},
			"bearer_methods_supported": []string{"header"},
		})
	})
}
