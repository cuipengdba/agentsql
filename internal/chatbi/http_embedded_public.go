package chatbi

import (
	"context"
	"errors"
	"net/http"
)

func (service *Service) pageEmbedByAppID(ctx context.Context, appID string) (AssistantApplication, error) {
	if len(appID) < 12 || len(appID) > 255 {
		return AssistantApplication{}, ErrNotFound
	}
	rows, err := service.Store.db.QueryContext(ctx, `SELECT scope,id,body FROM chatbi_objects WHERE kind='assistant' AND owner='workspace'`)
	if err != nil {
		return AssistantApplication{}, ErrUnavailable
	}
	defer rows.Close()
	count := 0
	var result AssistantApplication
	for rows.Next() {
		count++
		if count > 1000 {
			return AssistantApplication{}, ErrUnavailable
		}
		var scope, id string
		var body []byte
		if rows.Scan(&scope, &id, &body) != nil {
			return AssistantApplication{}, ErrUnavailable
		}
		var app AssistantApplication
		if err := service.Store.decode(objectKey{"assistant", scope, "workspace", id}, body, &app); err != nil {
			return AssistantApplication{}, ErrUnavailable
		}
		if app.AppID == appID && app.Type == 4 && app.Enabled && app.Workspace == ID(scope) && app.ID == ID(id) {
			if result.ID != "" {
				return AssistantApplication{}, ErrUnavailable
			}
			result = app
		}
	}
	if rows.Err() != nil {
		return AssistantApplication{}, ErrUnavailable
	}
	if result.ID == "" {
		return AssistantApplication{}, ErrNotFound
	}
	return result, nil
}

// The upstream SDK first asks for its page application's metadata. This
// public handshake returns only display fields to a configured origin; the
// signing secret remains available solely through authenticated management.
func (h *Handler) publicEmbeddedApplication(w http.ResponseWriter, r *http.Request) {
	if !h.appLookupLimit.Allow() {
		writeJSON(w, http.StatusTooManyRequests, "Too many embedding lookups")
		return
	}
	app, err := h.service.pageEmbedByAppID(r.Context(), r.PathValue("appID"))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeJSON(w, http.StatusNotFound, "Application not found")
		} else {
			failure(w, err)
		}
		return
	}
	origins := r.Header.Values("Origin")
	if len(origins) != 1 || !assistantOriginAllowed(app.Domain, origins[0]) {
		writeJSON(w, http.StatusForbidden, "Application origin is not allowed")
		return
	}
	w.Header().Set("Vary", "Origin")
	w.Header().Set("Access-Control-Allow-Origin", origins[0])
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	okay(w, map[string]any{
		"id": app.ID, "oid": app.Workspace, "name": app.Name, "description": app.Description,
		"type": app.Type, "domain": app.Domain, "configuration": app.Configuration,
		"app_id": app.AppID, "app_secret": nil,
		"create_time": app.Created.UnixMilli(), "enable_custom_model": app.UseCustomModel, "custom_model": app.CustomModel,
	})
}
