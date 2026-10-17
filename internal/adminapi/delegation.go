package adminapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/rbac"
	"github.com/cuipengdba/agentsql/internal/store"
)

const delegationTokenPurpose = "agentsql-mcp-human-delegation-v1"
const delegationTokenLifetime = 30 * time.Minute

type delegationTokenPayload struct {
	Purpose   string `json:"purpose"`
	JTI       string `json:"jti,omitempty"`
	IssuedAt  int64  `json:"iat"`
	Expires   int64  `json:"exp"`
	UserID    string `json:"sub"`
	TenantID  string `json:"tid"`
	Username  string `json:"username"`
	AgentID   string `json:"agent_id"`
	ParentJTI string `json:"parent_jti"`
	ParentSID string `json:"parent_sid"`
}

// HumanDelegationVerifier validates a scoped, short-lived AgentSQL token on
// every MCP request. The token is bound to one Agent and one live human login.
type HumanDelegationVerifier struct {
	key      []byte
	sessions *store.AdminSessionRepository
	rbac     *rbac.Service
}

func NewHumanDelegationVerifier(adminTokenKey []byte, sessions *store.AdminSessionRepository, roles *rbac.Service) (*HumanDelegationVerifier, error) {
	if len(adminTokenKey) == 0 || sessions == nil || roles == nil {
		return nil, errors.New("human delegation verifier requires a signing key, sessions, and RBAC")
	}
	return &HumanDelegationVerifier{key: deriveDelegationKey(adminTokenKey), sessions: sessions, rbac: roles}, nil
}

func deriveDelegationKey(adminTokenKey []byte) []byte {
	mac := hmac.New(sha256.New, adminTokenKey)
	_, _ = mac.Write([]byte(delegationTokenPurpose))
	return mac.Sum(nil)
}

func issueDelegationToken(adminTokenKey []byte, now time.Time, parent tokenPayload, principal rbac.Principal, agentID string) (string, time.Time, error) {
	if len(adminTokenKey) == 0 || now.IsZero() || parent.JTI == "" || parent.SessionID == "" ||
		parent.UserID != principal.UserID || parent.TenantID != principal.TenantID || parent.Username != principal.Username ||
		!principal.Has(rbac.PermissionQueryExecute) || strings.TrimSpace(agentID) == "" {
		return "", time.Time{}, ErrInvalidAdminToken
	}
	expires := now.Add(delegationTokenLifetime)
	if parentExpiry := time.Unix(parent.Expires, 0); parentExpiry.Before(expires) {
		expires = parentExpiry
	}
	if !expires.After(now) {
		return "", time.Time{}, ErrInvalidAdminToken
	}
	jti, err := randomHex(16)
	if err != nil {
		return "", time.Time{}, err
	}
	payload := delegationTokenPayload{
		Purpose: delegationTokenPurpose, JTI: jti, IssuedAt: now.Unix(), Expires: expires.Unix(),
		UserID: principal.UserID, TenantID: principal.TenantID, Username: principal.Username,
		AgentID: agentID, ParentJTI: parent.JTI, ParentSID: parent.SessionID,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", time.Time{}, err
	}
	part := base64.RawURLEncoding.EncodeToString(encoded)
	return part + "." + base64.RawURLEncoding.EncodeToString(signToken(deriveDelegationKey(adminTokenKey), part)), expires, nil
}

func parseDelegationToken(key []byte, token string, now time.Time) (delegationTokenPayload, error) {
	if len(key) == 0 || len(token) > 8192 || token == "" || strings.TrimSpace(token) != token || now.IsZero() {
		return delegationTokenPayload{}, ErrInvalidAdminToken
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return delegationTokenPayload{}, ErrInvalidAdminToken
	}
	provided, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(provided) != sha256.Size || subtle.ConstantTimeCompare(provided, signToken(key, parts[0])) != 1 {
		return delegationTokenPayload{}, ErrInvalidAdminToken
	}
	contents, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return delegationTokenPayload{}, ErrInvalidAdminToken
	}
	var payload delegationTokenPayload
	var jtiBytes []byte
	if json.Unmarshal(contents, &payload) != nil || payload.Purpose != delegationTokenPurpose ||
		payload.IssuedAt <= 0 || payload.Expires <= payload.IssuedAt ||
		payload.Expires-payload.IssuedAt > int64(delegationTokenLifetime.Seconds()) ||
		payload.IssuedAt > now.Add(time.Minute).Unix() || now.Unix() >= payload.Expires ||
		payload.UserID == "" || payload.TenantID == "" || payload.Username == "" ||
		payload.AgentID == "" || payload.ParentJTI == "" || payload.ParentSID == "" {
		return delegationTokenPayload{}, ErrInvalidAdminToken
	}
	// v1.0.0 tokens had no JTI. Keep them valid for their remaining (at most
	// 30-minute) lifetime; only newly issued tokens support individual revoke.
	if payload.JTI != "" {
		jtiBytes, err = hex.DecodeString(payload.JTI)
		if err != nil || len(jtiBytes) != 16 || hex.EncodeToString(jtiBytes) != payload.JTI {
			return delegationTokenPayload{}, ErrInvalidAdminToken
		}
	}
	return payload, nil
}

// Verify checks signature, expiry, Agent binding, parent session revocation,
// and live tenant/user/RBAC state. No secret or untrusted claim enters errors.
func (verifier *HumanDelegationVerifier) Verify(ctx context.Context, token, agentID string) (rbac.Principal, error) {
	if verifier == nil || ctx == nil || verifier.sessions == nil || verifier.rbac == nil || agentID == "" {
		return rbac.Principal{}, ErrInvalidAdminToken
	}
	now := time.Now()
	payload, err := parseDelegationToken(verifier.key, token, now)
	if err != nil || payload.AgentID != agentID {
		return rbac.Principal{}, ErrInvalidAdminToken
	}
	valid, err := verifier.sessions.ValidateAccessToken(ctx, payload.ParentJTI, payload.ParentSID, now)
	if err != nil || !valid {
		return rbac.Principal{}, ErrInvalidAdminToken
	}
	if payload.JTI != "" {
		valid, err = verifier.sessions.ValidateAccessToken(ctx, payload.JTI, payload.ParentSID, now)
		if err != nil || !valid {
			return rbac.Principal{}, ErrInvalidAdminToken
		}
	}
	principal, err := verifier.rbac.Principal(ctx, payload.TenantID, payload.UserID)
	if err != nil || principal.Username != payload.Username || !principal.Has(rbac.PermissionQueryExecute) {
		return rbac.Principal{}, ErrInvalidAdminToken
	}
	return principal, nil
}

func (handler *Handler) delegationTokenIssue(writer http.ResponseWriter, request *http.Request) {
	if !handler.deps.Config.EffectiveMCP().Delegation.RequireHumanToken {
		handler.fail(writer, http.StatusForbidden, "human delegation is disabled")
		return
	}
	principal, principalOK := requestPrincipal(request)
	parent, parentOK := requestTokenPayload(request)
	if !principalOK || !parentOK || !principal.Has(rbac.PermissionQueryExecute) {
		handler.fail(writer, http.StatusForbidden, "forbidden")
		return
	}
	var input struct {
		AgentID string `json:"agent_id"`
	}
	if err := decodeJSON(writer, request, &input); err != nil || input.AgentID == "" || strings.TrimSpace(input.AgentID) != input.AgentID {
		handler.fail(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	agent, err := handler.deps.Runtime.Store.Agents().Get(request.Context(), input.AgentID)
	if err != nil || agent.Status != "active" || agent.TenantID != principal.TenantID {
		handler.fail(writer, http.StatusNotFound, "agent not found")
		return
	}
	if agent.TaskGrantID != "" && agent.TaskGrantIssuerUserID != principal.UserID {
		handler.fail(writer, http.StatusForbidden, "forbidden")
		return
	}
	token, expires, err := issueDelegationToken(handler.tokenKey, time.Now().UTC(), parent, principal, agent.ID)
	if err != nil {
		handler.internal(writer, err)
		return
	}
	if handler.deps.Runtime.ManagementAudit == nil {
		handler.fail(writer, http.StatusServiceUnavailable, "audit unavailable")
		return
	}
	issuedPayload, err := parseDelegationToken(deriveDelegationKey(handler.tokenKey), token, time.Now().UTC())
	if err != nil {
		handler.internal(writer, err)
		return
	}
	details, err := json.Marshal(map[string]string{"agent_id": agent.ID, "token_id": issuedPayload.JTI, "expires_at": expires.Format(time.RFC3339)})
	if err != nil {
		handler.internal(writer, err)
		return
	}
	action, actorType, decision, encoded := "mcp_human_delegation_issue", "user", string(model.DecisionAllow), string(details)
	recorded, err := handler.deps.Runtime.ManagementAudit.Record(request.Context(), model.AuditLog{
		AgentID: &agent.ID, Decision: decision, Action: &action, ActorType: &actorType,
		ActorID: &principal.UserID, DetailsJSON: &encoded,
	})
	if err != nil {
		handler.fail(writer, http.StatusServiceUnavailable, "audit unavailable")
		return
	}
	handler.deps.Runtime.PublishManagementAudit(recorded)
	writer.Header().Set("Cache-Control", "no-store")
	handler.ok(writer, map[string]string{"token": token, "token_id": issuedPayload.JTI, "expires_at": expires.Format(time.RFC3339), "agent_id": agent.ID})
}

func (handler *Handler) delegationTokenRevoke(writer http.ResponseWriter, request *http.Request) {
	if !handler.deps.Config.EffectiveMCP().Delegation.RequireHumanToken {
		handler.fail(writer, http.StatusForbidden, "human delegation is disabled")
		return
	}
	principal, principalOK := requestPrincipal(request)
	parent, parentOK := requestTokenPayload(request)
	if !principalOK || !parentOK || !principal.Has(rbac.PermissionQueryExecute) {
		handler.fail(writer, http.StatusForbidden, "forbidden")
		return
	}
	var input struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(writer, request, &input); err != nil {
		handler.fail(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	now := time.Now().UTC()
	payload, err := parseDelegationToken(deriveDelegationKey(handler.tokenKey), input.Token, now)
	if err != nil || payload.JTI == "" || payload.UserID != principal.UserID || payload.TenantID != principal.TenantID ||
		payload.ParentJTI != parent.JTI || payload.ParentSID != parent.SessionID {
		handler.fail(writer, http.StatusForbidden, "forbidden")
		return
	}
	if err := handler.deps.Runtime.Store.AdminSessions().RevokeAccessJTI(request.Context(), payload.JTI, time.Unix(payload.Expires, 0), now); err != nil {
		handler.internal(writer, err)
		return
	}
	if handler.deps.Runtime.ManagementAudit == nil {
		handler.fail(writer, http.StatusServiceUnavailable, "audit unavailable")
		return
	}
	details, _ := json.Marshal(map[string]string{"agent_id": payload.AgentID, "token_id": payload.JTI})
	action, actorType, decision, encoded := "mcp_human_delegation_revoke", "user", string(model.DecisionAllow), string(details)
	recorded, err := handler.deps.Runtime.ManagementAudit.Record(request.Context(), model.AuditLog{
		AgentID: &payload.AgentID, Decision: decision, Action: &action, ActorType: &actorType,
		ActorID: &principal.UserID, DetailsJSON: &encoded,
	})
	if err != nil {
		handler.fail(writer, http.StatusServiceUnavailable, "audit unavailable")
		return
	}
	handler.deps.Runtime.PublishManagementAudit(recorded)
	handler.ok(writer, map[string]bool{"revoked": true})
}
