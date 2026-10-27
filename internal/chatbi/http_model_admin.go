package chatbi

import (
	"net/http"
)

func (h *Handler) nativeWorkspaceModel(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	var model ModelConfig
	revision, err := h.service.Store.get(r.Context(), objectKey{"model", string(p.Workspace), "workspace", "default"}, &model)
	if err != nil {
		failure(w, err)
		return
	}
	if validateModelConfig(model) != nil {
		failure(w, ErrUnavailable)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	okay(w, map[string]any{"id": model.ID, "revision": revision, "name": model.Name,
		"base_model": model.Model, "api_domain": model.Endpoint, "max_tokens": model.MaxTokens,
		"allow_http": model.AllowHTTP, "has_api_key": model.APIKey != ""})
}

func (h *Handler) nativeUpdateWorkspaceModel(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	var update WorkspaceModelUpdate
	if !readBody(w, r, &update) {
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	if err := h.service.Store.UpdateWorkspaceModel(r.Context(), p, update); err != nil {
		failure(w, err)
		return
	}
	okay(w, true)
}
