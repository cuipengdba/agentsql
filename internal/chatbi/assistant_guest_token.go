package chatbi

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
)

// issuePublicGuestToken is deliberately not exposed as a public endpoint.
// The browser SDK's RSA header is forgeable with the public key; only an
// explicitly published assistant with a separate guest identity may use this
// token. Every subsequent data read rechecks the application and gateway.
func (service *Service) issuePublicGuestToken(ctx context.Context, app AssistantApplication) (string, error) {
	if service == nil || service.Store == nil || service.Identity == nil || service.Identity.now == nil ||
		!app.ID.Valid() || !app.Workspace.Valid() {
		return "", ErrUnauthenticated
	}
	var current AssistantApplication
	revision, err := service.Store.get(ctx, assistantKey(app.Workspace, app.ID), &current)
	if err != nil || current.ID != app.ID || current.Workspace != app.Workspace ||
		current.Type != 0 || !current.Enabled || !current.PublicGuestUser.Valid() ||
		len(current.DatasourceIDs) == 0 || len(current.AppSecret) < 32 {
		return "", ErrUnauthenticated
	}
	guest, _, err := service.Store.User(ctx, current.PublicGuestUser)
	if err != nil || guest.Admin || guest.Origin != 0 || guest.Workspace != current.Workspace ||
		!guest.canUse(current.Workspace) {
		return "", ErrUnauthenticated
	}
	principal := Principal{User: guest, Workspace: current.Workspace}
	for _, source := range current.DatasourceIDs {
		if _, err := service.binding(ctx, principal, source); err != nil {
			return "", ErrUnauthenticated
		}
	}
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", ErrUnavailable
	}
	now := service.Identity.now().Unix()
	claims := embeddedClaims{Account: guest.Account, EmbeddedID: &current.ID,
		Visitor: hex.EncodeToString(entropy[:]), PublicGuest: true,
		AppRevision: revision, AuthRevision: guest.AuthRevision,
		Issued: now, Expires: now + 3600}
	const header = `{"alg":"HS256","typ":"JWT"}`
	body, err := json.Marshal(claims)
	if err != nil {
		return "", ErrUnavailable
	}
	prefix := base64.RawURLEncoding.EncodeToString([]byte(header)) + "." +
		base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, []byte(current.AppSecret))
	_, _ = mac.Write([]byte(prefix))
	token := prefix + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if _, err := service.Identity.VerifyEmbedded(ctx, token); err != nil {
		return "", ErrUnauthenticated
	}
	return token, nil
}
