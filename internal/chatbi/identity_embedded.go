package chatbi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

type embeddedClaims struct {
	Account    string `json:"account"`
	EmbeddedID *ID    `json:"embeddedId"`
	AppID      string `json:"appId"`
	Visitor    string `json:"visitor,omitempty"`
	Expires    int64  `json:"exp"`
	Issued     int64  `json:"iat"`
	NotBefore  int64  `json:"nbf"`
}

func validVisitorScope(value string) bool {
	if len(value) != 32 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func decodeEmbeddedAppID(value string) ID {
	if len(value) > 32 || value == "" {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		raw, err = base64.URLEncoding.DecodeString(value)
	}
	if err != nil || len(raw) == 0 || len(raw) > 8 {
		return ""
	}
	var padded [8]byte
	copy(padded[8-len(raw):], raw)
	valueID := ID(strconv.FormatUint(binary.BigEndian.Uint64(padded[:])^0xABCD1234, 10))
	if !valueID.Valid() {
		return ""
	}
	return valueID
}

func (identity *Identity) embeddedApplication(ctx context.Context, workspace ID, claims embeddedClaims) (AssistantApplication, int64, error) {
	id := ID("")
	if claims.EmbeddedID != nil {
		id = *claims.EmbeddedID
	} else {
		items, err := identity.store.list(ctx, "assistant", string(workspace), "workspace")
		if err != nil || len(items) > 1000 {
			return AssistantApplication{}, 0, ErrUnauthenticated
		}
		for _, raw := range items {
			var app AssistantApplication
			if json.Unmarshal(raw, &app) != nil || app.Workspace != workspace {
				return AssistantApplication{}, 0, ErrUnauthenticated
			}
			if app.Type == 4 && app.AppID == claims.AppID {
				id = app.ID
				break
			}
		}
		if id == "" {
			id = decodeEmbeddedAppID(claims.AppID)
		}
	}
	if !id.Valid() {
		return AssistantApplication{}, 0, ErrUnauthenticated
	}
	var app AssistantApplication
	revision, err := identity.store.get(ctx, assistantKey(workspace, id), &app)
	if err != nil || app.ID != id || app.Workspace != workspace || app.Type != 4 || !app.Enabled || app.AppSecret == "" {
		return AssistantApplication{}, 0, ErrUnauthenticated
	}
	return app, revision, nil
}

// VerifyEmbedded accepts the original page-embed JWT envelope while keeping
// its authority separate from ordinary user sessions and management tokens.
// A valid app credential can impersonate only an existing, enabled non-admin
// user in the same workspace; every data read rechecks app and user state.
func (identity *Identity) VerifyEmbedded(ctx context.Context, token string) (Principal, error) {
	deny := func() (Principal, error) { return Principal{}, ErrUnauthenticated }
	if len(token) == 0 || len(token) > 8192 {
		return deny()
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return deny()
	}
	headerRaw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(headerRaw) > 1024 {
		return deny()
	}
	var header struct {
		Algorithm string   `json:"alg"`
		Type      string   `json:"typ"`
		Critical  []string `json:"crit"`
	}
	if json.Unmarshal(headerRaw, &header) != nil || header.Algorithm != "HS256" || (header.Type != "" && header.Type != "JWT") || len(header.Critical) != 0 {
		return deny()
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(body) > 4096 {
		return deny()
	}
	var claims embeddedClaims
	if json.Unmarshal(body, &claims) != nil || claims.Account == "" || len(claims.Account) > 100 || claims.EmbeddedID == nil && claims.AppID == "" ||
		claims.Visitor != "" && !validVisitorScope(claims.Visitor) {
		return deny()
	}
	now := identity.now().Unix()
	maxAge := int64((8 * 24 * time.Hour).Seconds())
	if claims.Issued > now+5 || claims.NotBefore > now+5 || claims.Issued < 0 || claims.NotBefore < 0 ||
		(claims.Expires != 0 && (claims.Expires <= now || claims.Expires > now+maxAge)) ||
		(claims.Expires == 0 && (claims.Issued == 0 || claims.Issued < now-maxAge)) {
		return deny()
	}
	var userID ID
	if _, err := identity.store.get(ctx, identity.store.accountKey(claims.Account), &userID); err != nil {
		return deny()
	}
	user, _, err := identity.store.User(ctx, userID)
	if err != nil || user.Account != claims.Account || user.Admin || user.Origin != 0 || !user.canUse(user.Workspace) {
		return deny()
	}
	app, revision, err := identity.embeddedApplication(ctx, user.Workspace, claims)
	if err != nil {
		return deny()
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return deny()
	}
	mac := hmac.New(sha256.New, []byte(app.AppSecret))
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return deny()
	}
	return Principal{User: user, Workspace: user.Workspace, EmbeddedApp: app.ID, EmbeddedRevision: revision, VisitorScope: claims.Visitor}, nil
}
