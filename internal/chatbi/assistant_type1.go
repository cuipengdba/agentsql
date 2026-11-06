package chatbi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
)

// The endpoint is configuration, never authority. Only the operator's private
// catalog registration may associate it with a real user and gateway sources.
func assistantCatalogEndpoint(configuration string) string {
	var fields struct {
		Endpoint string `json:"endpoint"`
	}
	if json.Unmarshal([]byte(configuration), &fields) != nil || len(fields.Endpoint) > 2048 {
		return ""
	}
	u, err := url.Parse(fields.Endpoint)
	if err != nil || u.Opaque != "" || u.Host == "" || u.Hostname() == "" || u.User != nil ||
		u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") ||
		u.String() != fields.Endpoint || strings.TrimSpace(fields.Endpoint) != fields.Endpoint {
		return ""
	}
	return fields.Endpoint
}

// A public Type 1 bootstrap token carries no catalog credential. It can only
// name the operator-selected account; the browser must present the separately
// registered host certificate on every request that can access data.
func (service *Service) issueType1BootstrapToken(ctx context.Context, app AssistantApplication) (string, error) {
	if app.Type != 1 || !app.Enabled || len(app.AppSecret) < 32 {
		return "", ErrUnauthenticated
	}
	endpoint := assistantCatalogEndpoint(app.Configuration)
	if endpoint == "" {
		return "", ErrUnauthenticated
	}
	items, err := service.Store.list(ctx, "assistant-catalog", "system", "system")
	if err != nil || len(items) > 1000 {
		return "", ErrUnavailable
	}
	var bridge assistantCatalogBridgeRecord
	found := false
	for _, raw := range items {
		var candidate assistantCatalogBridgeRecord
		if json.Unmarshal(raw, &candidate) != nil {
			return "", ErrUnavailable
		}
		if candidate.Assistant != app.ID || candidate.Workspace != app.Workspace || !candidate.Enabled {
			continue
		}
		if found || candidate.Endpoint != endpoint || len(candidate.Sources) == 0 {
			return "", ErrUnauthenticated
		}
		bridge, found = candidate, true
	}
	if !found {
		return "", ErrUnauthenticated
	}
	user, _, err := service.Store.User(ctx, bridge.User)
	if err != nil || user.Admin || user.Origin != 0 || !user.canUse(app.Workspace) {
		return "", ErrUnauthenticated
	}
	for _, source := range bridge.Sources {
		var binding Binding
		if _, err := service.Store.get(ctx, bindingKey(Principal{User: user, Workspace: app.Workspace}, source.Datasource), &binding); err != nil ||
			binding.User != user.ID || binding.Workspace != app.Workspace || binding.Datasource != source.Datasource {
			return "", ErrUnauthenticated
		}
	}
	var current AssistantApplication
	revision, err := service.Store.get(ctx, assistantKey(app.Workspace, app.ID), &current)
	if err != nil || current.ID != app.ID || current.Type != 1 || !current.Enabled || current.AppSecret != app.AppSecret ||
		assistantCatalogEndpoint(current.Configuration) != endpoint {
		return "", ErrUnauthenticated
	}
	now := service.Identity.now().Unix()
	claims := embeddedClaims{Account: user.Account, EmbeddedID: &app.ID, CatalogBootstrap: true,
		AppRevision: revision, AuthRevision: user.AuthRevision, Issued: now, Expires: now + 3600}
	const header = `{"alg":"HS256","typ":"JWT"}`
	body, err := json.Marshal(claims)
	if err != nil {
		return "", ErrUnavailable
	}
	prefix := base64.RawURLEncoding.EncodeToString([]byte(header)) + "." + base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, []byte(app.AppSecret))
	_, _ = mac.Write([]byte(prefix))
	token := prefix + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if _, err := service.Identity.VerifyEmbedded(ctx, token); err != nil {
		return "", ErrUnauthenticated
	}
	return token, nil
}

func (service *Service) type1BridgeUnchanged(ctx context.Context, p Principal, app AssistantApplication) error {
	if p.CatalogBridgeKey == "" || p.CatalogRevision < 1 || app.Type != 1 || p.VisitorScope != "" {
		return ErrUnauthenticated
	}
	var bridge assistantCatalogBridgeRecord
	revision, err := service.Store.get(ctx, objectKey{"assistant-catalog", "system", "system", p.CatalogBridgeKey}, &bridge)
	if err != nil || revision != p.CatalogRevision || !bridge.Enabled || bridge.Assistant != app.ID ||
		bridge.Workspace != p.Workspace || bridge.User != p.User.ID ||
		bridge.Endpoint != assistantCatalogEndpoint(app.Configuration) || len(bridge.Sources) == 0 || len(bridge.Sources) > 32 {
		return ErrUnauthenticated
	}
	return nil
}

func (service *Service) type1SourceAllowed(ctx context.Context, p Principal, datasource ID) error {
	var bridge assistantCatalogBridgeRecord
	if _, err := service.Store.get(ctx, objectKey{"assistant-catalog", "system", "system", p.CatalogBridgeKey}, &bridge); err != nil {
		return ErrUnauthenticated
	}
	for _, source := range bridge.Sources {
		if source.Datasource == datasource {
			return nil
		}
	}
	return ErrNotFound
}
