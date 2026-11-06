package chatbi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

const guestResumeLifetime = 7 * 24 * time.Hour
const guestResumeHeader = `{"alg":"HS256","typ":"AGENTSQL-GUEST-RESUME"}`

type guestResumeClaims struct {
	App          ID     `json:"app"`
	Workspace    ID     `json:"workspace"`
	Guest        ID     `json:"guest"`
	Visitor      string `json:"visitor"`
	AppRevision  int64  `json:"appRevision"`
	AuthRevision int64  `json:"authRevision"`
	Issued       int64  `json:"iat"`
	Expires      int64  `json:"exp"`
}

func (service *Service) liveGuestAssistant(ctx context.Context, p Principal) (AssistantApplication, error) {
	if service == nil || service.Store == nil || service.Identity == nil || service.Identity.now == nil ||
		p.EmbeddedType != 0 || !p.EmbeddedApp.Valid() || !validVisitorScope(p.VisitorScope) ||
		service.live(ctx, p) != nil {
		return AssistantApplication{}, ErrUnauthenticated
	}
	var app AssistantApplication
	revision, err := service.Store.get(ctx, assistantKey(p.Workspace, p.EmbeddedApp), &app)
	if err != nil || revision != p.EmbeddedRevision || app.Type != 0 || !app.Enabled ||
		app.PublicGuestUser != p.User.ID || len(app.AppSecret) < 32 || len(app.DatasourceIDs) == 0 {
		return AssistantApplication{}, ErrUnauthenticated
	}
	for _, source := range app.DatasourceIDs {
		if _, err := service.binding(ctx, p, source); err != nil {
			return AssistantApplication{}, ErrUnauthenticated
		}
	}
	return app, nil
}

// A resume proof is a browser-storage credential, distinct from a data JWT.
// It never grants access alone: a newly issued, current public-guest JWT is
// required too, and application/user/binding revocation is checked again.
func (service *Service) issueGuestResumeProof(ctx context.Context, p Principal) (string, error) {
	app, err := service.liveGuestAssistant(ctx, p)
	if err != nil {
		return "", err
	}
	now := service.Identity.now().Unix()
	claims := guestResumeClaims{App: app.ID, Workspace: p.Workspace, Guest: p.User.ID,
		Visitor: p.VisitorScope, AppRevision: p.EmbeddedRevision,
		AuthRevision: p.User.AuthRevision, Issued: now,
		Expires: now + int64(guestResumeLifetime.Seconds())}
	body, err := json.Marshal(claims)
	if err != nil {
		return "", ErrUnavailable
	}
	prefix := base64.RawURLEncoding.EncodeToString([]byte(guestResumeHeader)) + "." +
		base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, []byte(app.AppSecret))
	_, _ = mac.Write([]byte(prefix))
	return prefix + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (service *Service) resumePublicGuest(ctx context.Context, p Principal, proof string) (string, error) {
	app, err := service.liveGuestAssistant(ctx, p)
	if err != nil || len(proof) == 0 || len(proof) > 2048 {
		return "", ErrUnauthenticated
	}
	parts := strings.Split(proof, ".")
	if len(parts) != 3 || parts[0] != base64.RawURLEncoding.EncodeToString([]byte(guestResumeHeader)) {
		return "", ErrUnauthenticated
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(body) > 1024 {
		return "", ErrUnauthenticated
	}
	var claims guestResumeClaims
	if json.Unmarshal(body, &claims) != nil {
		return "", ErrUnauthenticated
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(signature) != sha256.Size {
		return "", ErrUnauthenticated
	}
	mac := hmac.New(sha256.New, []byte(app.AppSecret))
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return "", ErrUnauthenticated
	}
	now := service.Identity.now().Unix()
	if claims.App != app.ID || claims.Workspace != p.Workspace || claims.Guest != p.User.ID ||
		!validVisitorScope(claims.Visitor) || claims.AppRevision != p.EmbeddedRevision ||
		claims.AuthRevision != p.User.AuthRevision || claims.Issued < 0 ||
		claims.Issued > now+5 || claims.Expires <= now ||
		claims.Expires <= claims.Issued || claims.Expires-claims.Issued > int64(guestResumeLifetime.Seconds()) {
		return "", ErrUnauthenticated
	}
	return service.issuePublicGuestTokenForVisitor(ctx, app, claims.Visitor)
}
