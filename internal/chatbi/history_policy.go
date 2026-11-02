package chatbi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// A separate encrypted control object keeps this internal fingerprint out of
// the SQLBot-compatible chat and record response bodies. Older records have
// no fingerprint and are excluded from model context until re-created.
func recordPolicyKey(p Principal, id ID) objectKey {
	return objectKey{"record-context-policy", string(p.Workspace), string(p.User.ID), string(id)}
}

func (service *Service) modelContextFingerprint(ctx context.Context, p Principal, bindingDigest [32]byte, rules []DataPermissionRule) (string, error) {
	var user User
	_, userDigest, err := service.Store.getWithDigest(ctx, userKey(p.User.ID), &user)
	if err != nil || user.AuthRevision != p.User.AuthRevision || !user.canUse(p.Workspace) {
		return "", ErrGateway
	}
	raw, err := json.Marshal(rules)
	if err != nil {
		return "", ErrGateway
	}
	identity, err := json.Marshal([]any{p.Workspace, p.User.ID, p.EmbeddedApp, p.EmbeddedRevision, p.EmbeddedType, p.VisitorScope})
	if err != nil {
		return "", ErrGateway
	}
	digest := sha256.New()
	_, _ = digest.Write(userDigest[:])
	_, _ = digest.Write(bindingDigest[:])
	_, _ = digest.Write(raw)
	_, _ = digest.Write(identity)
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func (service *Service) modelContextUnchanged(ctx context.Context, p Principal, bindingDigest [32]byte, rules []DataPermissionRule, prior string) error {
	current, err := service.modelContextFingerprint(ctx, p, bindingDigest, rules)
	if err != nil || current != prior {
		return ErrGateway
	}
	return nil
}

func (service *Service) historyWithCurrentPolicy(ctx context.Context, p Principal, ids []ID, history []TurnContext, fingerprint string) ([]TurnContext, error) {
	if len(ids) != len(history) || fingerprint == "" {
		return nil, ErrGateway
	}
	visible := make([]TurnContext, 0, len(history))
	for index, turn := range history {
		var recorded string
		_, err := service.Store.get(ctx, recordPolicyKey(p, ids[index]), &recorded)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, ErrGateway
		}
		if recorded == fingerprint {
			visible = append(visible, turn)
		}
	}
	return visible, nil
}
