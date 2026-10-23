package adminapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/rbac"
	"github.com/cuipengdba/agentsql/internal/store"
)

const sqlbotTokenPurpose = "agentsql-sqlbot-human-v1"
const sqlbotTokenPrefix = "SQB1."
const sqlbotTokenLifetime = 2 * time.Minute

type sqlbotTokenPayload struct {
	Purpose   string `json:"purpose"`
	BindingID string `json:"binding_id"`
	Revision  int64  `json:"revision"`
	AgentID   string `json:"agent_id"`
	UserID    string `json:"sub"`
	TenantID  string `json:"tid"`
	IssuedAt  int64  `json:"iat"`
	Expires   int64  `json:"exp"`
}

// SQLBotDelegationVerifier authenticates a separately scoped integration token.
// A SQLBot server is trusted to authenticate its configured external user; no
// console session is synthesized and no ordinary login token is accepted here.
type SQLBotDelegationVerifier struct {
	key   []byte
	store *store.Store
	rbac  *rbac.Service
}

func NewSQLBotDelegationVerifier(key []byte, database *store.Store, roles *rbac.Service) *SQLBotDelegationVerifier {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(sqlbotTokenPurpose))
	if len(key) == 0 || database == nil || roles == nil {
		return nil
	}
	return &SQLBotDelegationVerifier{key: mac.Sum(nil), store: database, rbac: roles}
}

func (v *SQLBotDelegationVerifier) issue(b store.SQLBotBinding, now time.Time) (string, time.Time, error) {
	expires := now.Add(sqlbotTokenLifetime)
	payload := sqlbotTokenPayload{Purpose: sqlbotTokenPurpose, BindingID: b.ID, Revision: b.Revision,
		AgentID: b.AgentID, UserID: b.AgentSQLUserID, TenantID: b.TenantID, IssuedAt: now.Unix(), Expires: expires.Unix()}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", time.Time{}, err
	}
	part := base64.RawURLEncoding.EncodeToString(encoded)
	return sqlbotTokenPrefix + part + "." + base64.RawURLEncoding.EncodeToString(signToken(v.key, part)), expires, nil
}

func (v *SQLBotDelegationVerifier) parse(token string, now time.Time) (sqlbotTokenPayload, error) {
	var p sqlbotTokenPayload
	if v == nil || !strings.HasPrefix(token, sqlbotTokenPrefix) || len(token) > 4096 {
		return p, ErrInvalidAdminToken
	}
	parts := strings.Split(strings.TrimPrefix(token, sqlbotTokenPrefix), ".")
	if len(parts) != 2 {
		return p, ErrInvalidAdminToken
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(signature, signToken(v.key, parts[0])) {
		return p, ErrInvalidAdminToken
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(raw, &p) != nil || p.Purpose != sqlbotTokenPurpose || p.BindingID == "" || p.Revision < 1 ||
		p.AgentID == "" || p.UserID == "" || p.TenantID == "" || p.IssuedAt <= 0 || p.IssuedAt > now.Add(5*time.Second).Unix() ||
		p.Expires <= p.IssuedAt || p.Expires-p.IssuedAt > int64(sqlbotTokenLifetime.Seconds()) || now.Unix() >= p.Expires {
		return sqlbotTokenPayload{}, ErrInvalidAdminToken
	}
	return p, nil
}

func (v *SQLBotDelegationVerifier) VerifySQLBot(ctx context.Context, token, agentID string) (rbac.Principal, store.SQLBotBinding, error) {
	deny := func() (rbac.Principal, store.SQLBotBinding, error) {
		return rbac.Principal{}, store.SQLBotBinding{}, ErrInvalidAdminToken
	}
	p, err := v.parse(token, time.Now())
	if err != nil || p.AgentID != agentID {
		return deny()
	}
	tenantCtx, err := store.WithTenant(ctx, p.TenantID)
	if err != nil {
		return deny()
	}
	b, err := v.store.SQLBotBindings().ValidateRuntime(tenantCtx, p.BindingID, p.Revision, agentID, p.UserID)
	if err != nil {
		return deny()
	}
	principal, err := v.rbac.Principal(tenantCtx, p.TenantID, p.UserID)
	if err != nil || !principal.Has(rbac.PermissionQueryExecute) {
		return deny()
	}
	agent, err := v.store.Agents().Get(tenantCtx, agentID)
	if err != nil || agent.Status != "active" || agent.Level != "readonly" || agent.TaskGrantID != "" || agent.TenantID != p.TenantID ||
		agent.ExpiresAt != nil && !agent.ExpiresAt.After(time.Now()) {
		return deny()
	}
	ds, err := v.store.Datasources().Get(tenantCtx, b.DatasourceID)
	if err != nil || ds.DBType != "postgres" || ds.TenantID != p.TenantID {
		return deny()
	}
	return principal, b, nil
}

// sqlbotResolve accepts only a dedicated per-binding connector credential.
// Its external user ID is an assertion from the trusted SQLBot server, never
// from an unauthenticated browser or a model-generated tool argument.
func (handler *Handler) sqlbotResolve(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	headers := request.Header.Values("Authorization")
	if len(headers) != 1 {
		handler.fail(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	credential, ok := adminBearerToken(headers[0])
	if !ok || len(credential) != 43 {
		handler.fail(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	b, err := handler.deps.Runtime.Store.SQLBotBindings().ResolveConnector(request.Context(), credential)
	if err != nil {
		handler.fail(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	var input struct {
		BindingID      string `json:"binding_id"`
		WorkspaceID    string `json:"workspace_id"`
		DatasourceName string `json:"datasource_name"`
		SQLBotUserID   string `json:"sqlbot_user_id"`
	}
	if decodeJSON(writer, request, &input) != nil || input.BindingID != b.ID || input.WorkspaceID != b.WorkspaceID ||
		input.DatasourceName != b.DatasourceName || input.SQLBotUserID != b.SQLBotUserID {
		handler.fail(writer, http.StatusForbidden, "SQLBot identity or datasource mapping unavailable")
		return
	}
	ctx, err := store.WithTenant(request.Context(), b.TenantID)
	if err != nil {
		handler.fail(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	v := NewSQLBotDelegationVerifier(handler.tokenKey, handler.deps.Runtime.Store, handler.rbac)
	token, expires, err := v.issue(b, time.Now().UTC())
	if err != nil {
		handler.internal(writer, err)
		return
	}
	principal, current, err := v.VerifySQLBot(ctx, token, b.AgentID)
	if err != nil {
		handler.fail(writer, http.StatusForbidden, "SQLBot identity or datasource mapping unavailable")
		return
	}
	if handler.deps.Runtime.ManagementAudit == nil {
		handler.fail(writer, http.StatusServiceUnavailable, "audit unavailable")
		return
	}
	details, _ := json.Marshal(map[string]any{"binding_id": b.ID, "revision": b.Revision, "sqlbot_user_id": b.SQLBotUserID, "workspace_id": b.WorkspaceID, "expires_at": expires.Format(time.RFC3339)})
	action, actorType, encoded := "sqlbot_identity_issue", "user", string(details)
	recorded, err := handler.deps.Runtime.ManagementAudit.Record(ctx, model.AuditLog{AgentID: &b.AgentID, DatasourceID: &b.DatasourceID,
		Decision: string(model.DecisionAllow), Action: &action, ActorType: &actorType, ActorID: &principal.UserID, DetailsJSON: &encoded})
	if err != nil {
		handler.fail(writer, http.StatusServiceUnavailable, "audit unavailable")
		return
	}
	handler.deps.Runtime.PublishManagementAudit(recorded)
	handler.ok(writer, map[string]any{"binding_id": current.ID, "revision": current.Revision, "agent_id": current.AgentID,
		"datasource_id": current.DatasourceID, "schema": current.SchemaName, "numeric_columns": current.NumericColumns,
		"user_token": token, "expires_at": expires.Format(time.RFC3339)})
}
