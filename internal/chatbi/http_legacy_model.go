package chatbi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
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

// The original Community form includes an empty id on first create and RSA
// encrypts its endpoint and key. AgentSQL can create only the first default
// model in the caller's current workspace; subsequent catalog entries need a
// separate multi-model design and cannot silently replace this model.
func (h *Handler) legacyModelCreate(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	var input struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		ModelType  *int   `json:"model_type"`
		BaseModel  string `json:"base_model"`
		Supplier   *int   `json:"supplier"`
		Protocol   *int   `json:"protocol"`
		APIDomain  string `json:"api_domain"`
		APIKey     string `json:"api_key"`
		ConfigList []any  `json:"config_list"`
	}
	if !readBody(w, r, &input) {
		return
	}
	if input.ID != "" || input.ModelType == nil || *input.ModelType != 0 ||
		input.Supplier == nil || *input.Supplier != 15 || input.Protocol == nil || *input.Protocol != 1 || len(input.ConfigList) != 0 {
		failure(w, ErrInvalidAccountChange)
		return
	}
	endpoint, err := h.service.Identity.DecryptLogin(input.APIDomain)
	if err != nil {
		failure(w, ErrInvalidAccountChange)
		return
	}
	var apiKey *string
	if input.APIKey != "" {
		value, err := h.service.Identity.DecryptLogin(input.APIKey)
		if err != nil {
			failure(w, ErrInvalidAccountChange)
			return
		}
		apiKey = &value
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	update := WorkspaceModelUpdate{Name: input.Name, Model: input.BaseModel, Endpoint: endpoint, APIKey: apiKey}
	if err := h.service.Store.CreateWorkspaceModel(r.Context(), p, update); err != nil {
		failure(w, err)
		return
	}
	okay(w, nil)
}

// The pinned original browser encrypts both endpoint and nonempty API key
// with the same installation RSA public key as its login form. This adapter
// only edits the current workspace's existing default OpenAI-compatible
// model; unsupported providers and advanced arguments fail closed.
func (h *Handler) legacyModelUpdate(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	var input struct {
		ID           ID     `json:"id"`
		Name         string `json:"name"`
		ModelType    *int   `json:"model_type"`
		BaseModel    string `json:"base_model"`
		Supplier     *int   `json:"supplier"`
		Protocol     *int   `json:"protocol"`
		DefaultModel *bool  `json:"default_model"`
		APIDomain    string `json:"api_domain"`
		APIKey       string `json:"api_key"`
		ConfigList   []any  `json:"config_list"`
	}
	if !readBody(w, r, &input) {
		return
	}
	if !input.ID.Valid() || input.ModelType == nil || *input.ModelType != 0 ||
		input.Supplier == nil || *input.Supplier != 15 || input.Protocol == nil || *input.Protocol != 1 ||
		input.DefaultModel == nil || !*input.DefaultModel || len(input.ConfigList) != 0 {
		failure(w, ErrInvalidAccountChange)
		return
	}
	key := objectKey{"model", string(p.Workspace), "workspace", "default"}
	var current ModelConfig
	revision, err := h.service.Store.get(r.Context(), key, &current)
	if errors.Is(err, ErrNotFound) {
		failure(w, ErrNotFound)
		return
	}
	if err != nil || validateModelConfig(current) != nil {
		failure(w, ErrUnavailable)
		return
	}
	if current.ID != input.ID {
		failure(w, ErrNotFound)
		return
	}
	endpoint, err := h.service.Identity.DecryptLogin(input.APIDomain)
	if err != nil {
		failure(w, ErrInvalidAccountChange)
		return
	}
	var apiKey *string
	if input.APIKey != "" {
		value, err := h.service.Identity.DecryptLogin(input.APIKey)
		if err != nil {
			failure(w, ErrInvalidAccountChange)
			return
		}
		apiKey = &value
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	update := WorkspaceModelUpdate{Revision: revision, Name: input.Name, Model: input.BaseModel,
		Endpoint: endpoint, APIKey: apiKey, MaxTokens: current.MaxTokens}
	if err := h.service.Store.UpdateWorkspaceModel(r.Context(), p, update); err != nil {
		failure(w, err)
		return
	}
	okay(w, nil)
}

// The original browser submits its editable form when checking the model.
// Probe only the saved, authorized model. Client-supplied endpoint, key and
// prompt have no authority and generated SQL is never executed.
func (h *Handler) legacyModelStatus(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	var input struct {
		ID string `json:"id"`
	}
	if !readBody(w, r, &input) {
		return
	}
	model, err := h.currentLegacyModel(r, p, token)
	if err != nil {
		failure(w, err)
		return
	}
	if input.ID != "" && input.ID != string(model.ID) {
		failure(w, ErrNotFound)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	result, modelErr := h.service.Model.Generate(ctx, model, "pg", "Return one bounded test query over the probe table", []Column{
		{Schema: "public", Table: "agentsql_probe", Name: "value", DataType: "integer"},
	})
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	if modelErr != nil || result.SQL == "" {
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Model unavailable"})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"content": "Model reachable"})
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
