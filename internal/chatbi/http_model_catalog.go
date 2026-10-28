package chatbi

import (
	"context"
	"errors"
	"net/http"
	"time"
)

func (h *Handler) nativeModelAdmin(r *http.Request, p Principal, token string) error {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		return err
	}
	return h.currentIdentity(r.Context(), p, token)
}

func (h *Handler) nativeAdditionalModel(r *http.Request, p Principal) (ModelConfig, int64, error) {
	id := ID(r.PathValue("id"))
	if !id.Valid() {
		return ModelConfig{}, 0, ErrNotFound
	}
	var model ModelConfig
	revision, err := h.service.Store.get(r.Context(), additionalModelKey(p.Workspace, id), &model)
	if err != nil {
		return ModelConfig{}, 0, err
	}
	if model.ID != id || validateModelConfig(model) != nil {
		return ModelConfig{}, 0, ErrUnavailable
	}
	return model, revision, nil
}

func (h *Handler) nativeWorkspaceModelList(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.nativeModelAdmin(r, p, token); err != nil {
		failure(w, err)
		return
	}
	var current ModelConfig
	_, err := h.service.Store.get(r.Context(), objectKey{"model", string(p.Workspace), "workspace", "default"}, &current)
	if err != nil && !errors.Is(err, ErrNotFound) {
		failure(w, err)
		return
	}
	models := make([]map[string]any, 0, 1)
	if err == nil {
		if validateModelConfig(current) != nil {
			failure(w, ErrUnavailable)
			return
		}
		models = append(models, map[string]any{"id": current.ID, "name": current.Name,
			"base_model": current.Model, "default_model": true})
	}
	additional, err := h.service.Store.AdditionalWorkspaceModels(r.Context(), p.Workspace)
	if err != nil {
		failure(w, err)
		return
	}
	if len(models) == 0 && len(additional) != 0 {
		failure(w, ErrUnavailable)
		return
	}
	for _, model := range additional {
		models = append(models, map[string]any{"id": model.ID, "name": model.Name,
			"base_model": model.Model, "default_model": false})
	}
	okay(w, models)
}

func (h *Handler) nativeAdditionalModelRead(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.nativeModelAdmin(r, p, token); err != nil {
		failure(w, err)
		return
	}
	model, revision, err := h.nativeAdditionalModel(r, p)
	if err != nil {
		failure(w, err)
		return
	}
	okay(w, map[string]any{"id": model.ID, "revision": revision, "name": model.Name,
		"base_model": model.Model, "api_domain": model.Endpoint, "max_tokens": model.MaxTokens,
		"temperature": model.Temperature, "allow_http": model.AllowHTTP, "has_api_key": model.APIKey != ""})
}

func (h *Handler) nativeCreateAdditionalModel(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.nativeModelAdmin(r, p, token); err != nil {
		failure(w, err)
		return
	}
	var update WorkspaceModelUpdate
	if !readBody(w, r, &update) {
		return
	}
	if err := h.service.Store.CreateAdditionalWorkspaceModel(r.Context(), p, update); err != nil {
		failure(w, err)
		return
	}
	okay(w, true)
}

func (h *Handler) nativeAdditionalModelUpdate(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.nativeModelAdmin(r, p, token); err != nil {
		failure(w, err)
		return
	}
	if _, _, err := h.nativeAdditionalModel(r, p); err != nil {
		failure(w, err)
		return
	}
	var update WorkspaceModelUpdate
	if !readBody(w, r, &update) {
		return
	}
	if err := h.service.Store.UpdateAdditionalWorkspaceModel(r.Context(), p, ID(r.PathValue("id")), update); err != nil {
		failure(w, err)
		return
	}
	okay(w, true)
}

func (h *Handler) nativeAdditionalModelDelete(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.nativeModelAdmin(r, p, token); err != nil {
		failure(w, err)
		return
	}
	if _, _, err := h.nativeAdditionalModel(r, p); err != nil {
		failure(w, err)
		return
	}
	if err := h.service.Store.DeleteAdditionalWorkspaceModel(r.Context(), p, ID(r.PathValue("id"))); err != nil {
		failure(w, err)
		return
	}
	okay(w, true)
}

func (h *Handler) nativeAdditionalModelSetDefault(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.nativeModelAdmin(r, p, token); err != nil {
		failure(w, err)
		return
	}
	if _, _, err := h.nativeAdditionalModel(r, p); err != nil {
		failure(w, err)
		return
	}
	if err := h.service.Store.SetDefaultWorkspaceModel(r.Context(), p, ID(r.PathValue("id"))); err != nil {
		failure(w, err)
		return
	}
	okay(w, true)
}

func (h *Handler) nativeAdditionalModelCheck(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.nativeModelAdmin(r, p, token); err != nil {
		failure(w, err)
		return
	}
	if r.ContentLength > 0 {
		failure(w, ErrInvalidAccountChange)
		return
	}
	model, _, err := h.nativeAdditionalModel(r, p)
	if err != nil {
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
