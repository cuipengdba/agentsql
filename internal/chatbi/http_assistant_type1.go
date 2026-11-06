package chatbi

import (
	"encoding/base64"
	"net/http"
	"net/url"
)

// SQLBot's unchanged Type 1 browser sends btoa(encodeURIComponent(JSON)) on
// each data request. The decoded value is only a lookup secret for an exact
// operator registration; it never supplies a user or database connection.
func type1RequestCredential(r *http.Request) (string, error) {
	values := r.Header.Values("X-SQLBOT-ASSISTANT-CERTIFICATE")
	if len(values) != 1 || len(values[0]) == 0 || len(values[0]) > 16384 {
		return "", ErrUnauthenticated
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(values[0])
	if err != nil || len(raw) == 0 || len(raw) > 8192 {
		return "", ErrUnauthenticated
	}
	authorization, err := url.QueryUnescape(string(raw))
	if err != nil || len(authorization) == 0 || len(authorization) > 8192 {
		return "", ErrUnauthenticated
	}
	return authorization, nil
}

func (h *Handler) bindType1Request(r *http.Request, bootstrap Principal) (Principal, error) {
	var app AssistantApplication
	appRevision, err := h.service.Store.get(r.Context(), assistantKey(bootstrap.Workspace, bootstrap.EmbeddedApp), &app)
	if err != nil || appRevision != bootstrap.EmbeddedRevision || app.Type != 1 || !app.Enabled {
		return Principal{}, ErrUnauthenticated
	}
	origins := r.Header.Values("X-SQLBOT-HOST-ORIGIN")
	if len(origins) != 1 || !assistantOriginAllowed(app.Domain, origins[0]) {
		return Principal{}, ErrUnauthenticated
	}
	authorization, err := type1RequestCredential(r)
	if err != nil {
		return Principal{}, err
	}
	endpoint := assistantCatalogEndpoint(app.Configuration)
	caller, _, revision, err := h.service.resolveAssistantCatalogBridge(r.Context(), endpoint, authorization)
	if err != nil || caller.User.ID != bootstrap.User.ID || caller.User.AuthRevision != bootstrap.User.AuthRevision ||
		caller.Workspace != bootstrap.Workspace {
		return Principal{}, ErrUnauthenticated
	}
	credentials, err := parseMCPAssistantCatalogIdentity(endpoint, authorization)
	if err != nil {
		return Principal{}, ErrUnauthenticated
	}
	key := h.service.Store.assistantCatalogBridgeKey(endpoint, credentials)
	bootstrap.CatalogBridgeKey, bootstrap.CatalogRevision = key.ID, revision
	if err := h.service.live(r.Context(), bootstrap); err != nil {
		return Principal{}, ErrUnauthenticated
	}
	return bootstrap, nil
}

func (h *Handler) type1AssistantDatasources(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.live(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	var app AssistantApplication
	if _, err := h.service.Store.get(r.Context(), assistantKey(p.Workspace, p.EmbeddedApp), &app); err != nil {
		failure(w, ErrUnauthenticated)
		return
	}
	authorization, err := type1RequestCredential(r)
	if err != nil {
		failure(w, err)
		return
	}
	caller, entries, err := h.service.fetchAssistantCatalog(r.Context(), assistantCatalogEndpoint(app.Configuration), authorization, nil)
	if err != nil || caller.User.ID != p.User.ID || caller.User.AuthRevision != p.User.AuthRevision || caller.Workspace != p.Workspace {
		failure(w, ErrUnauthenticated)
		return
	}
	result := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		binding, _, _, err := h.service.bindingSnapshot(r.Context(), p, entry.Datasource)
		if err != nil {
			failure(w, err)
			return
		}
		typeName := "PostgreSQL"
		if binding.Dialect == "mysql" {
			typeName = "MySQL"
		}
		result = append(result, map[string]any{"id": entry.Datasource, "name": entry.Name,
			"description": binding.Description, "type": binding.Dialect, "type_name": typeName, "num": 0})
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	okay(w, result)
}
