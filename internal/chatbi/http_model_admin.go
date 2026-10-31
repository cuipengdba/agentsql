package chatbi

import (
	"context"
	"net/http"
	"time"
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
		"base_model": model.Model, "api_domain": model.Endpoint, "max_tokens": model.MaxTokens, "temperature": model.Temperature,
		"allow_http": model.AllowHTTP, "has_api_key": model.APIKey != "", "supplier": modelSupplier(model)})
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

func (h *Handler) nativeCreateWorkspaceModel(w http.ResponseWriter, r *http.Request, p Principal, token string) {
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
	if err := h.service.Store.CreateWorkspaceModel(r.Context(), p, update); err != nil {
		failure(w, err)
		return
	}
	okay(w, true)
}

// The probe never executes generated SQL. It checks only the selected local
// workspace model and returns no prompt, completion, endpoint, or key.
func (h *Handler) nativeCheckWorkspaceModel(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	if r.ContentLength > 0 {
		failure(w, ErrInvalidAccountChange)
		return
	}
	var model ModelConfig
	_, err := h.service.Store.get(r.Context(), objectKey{"model", string(p.Workspace), "workspace", "default"}, &model)
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
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	result, err := h.service.Model.Generate(ctx, model, "pg", "Return one bounded test query over the probe table", []Column{
		{Schema: "public", Table: "agentsql_probe", Name: "value", DataType: "integer"},
	})
	if err != nil || result.SQL == "" {
		failure(w, ErrModel)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	okay(w, map[string]any{"reachable": true})
}
