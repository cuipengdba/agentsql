package chatbi

import (
	"errors"
	"net/http"
	"strings"
)

// The Community model administration page starts with these two read paths.
// AgentSQL currently keeps one model per workspace; do not expose another
// workspace's model or return the encrypted API key through this adapter.
func (h *Handler) legacyModelList(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	keyword := r.URL.Query().Get("keyword")
	if len(keyword) > 255 {
		failure(w, ErrInvalidAccountChange)
		return
	}
	model, err := h.currentLegacyModel(r, p, token)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			if err := h.currentIdentity(r.Context(), p, token); err != nil {
				failure(w, err)
				return
			}
			okay(w, []map[string]any{})
			return
		}
		failure(w, err)
		return
	}
	name := model.Name
	if name == "" {
		name = model.Model
	}
	if keyword != "" && !strings.Contains(strings.ToLower(name), strings.ToLower(keyword)) {
		okay(w, []map[string]any{})
		return
	}
	okay(w, []map[string]any{{"id": model.ID, "name": name,
		"model_type": 0, "base_model": model.Model, "supplier": 15,
		"protocol": 1, "default_model": true, "ws_mapping_count": 1}})
}

func (h *Handler) legacyModelRead(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	model, err := h.currentLegacyModel(r, p, token)
	if err != nil {
		failure(w, err)
		return
	}
	if r.PathValue("id") != string(model.ID) {
		failure(w, ErrNotFound)
		return
	}
	name := model.Name
	if name == "" {
		name = model.Model
	}
	okay(w, map[string]any{"id": model.ID, "name": name,
		"model_type": 0, "base_model": model.Model, "supplier": 15,
		"protocol": 1, "default_model": true, "ws_mapping_count": 1,
		"api_domain": model.Endpoint, "api_key": "", "has_api_key": model.APIKey != "",
		"config_list": []any{}})
}

func (h *Handler) currentLegacyModel(r *http.Request, p Principal, token string) (ModelConfig, error) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		return ModelConfig{}, err
	}
	var model ModelConfig
	_, err := h.service.Store.get(r.Context(), objectKey{"model", string(p.Workspace), "workspace", "default"}, &model)
	if err != nil {
		return ModelConfig{}, err
	}
	if validateModelConfig(model) != nil {
		return ModelConfig{}, ErrUnavailable
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		return ModelConfig{}, err
	}
	return model, nil
}
