package chatbi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// Do not expose another workspace's model or return any encrypted API key.
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
	additional, err := h.service.Store.AdditionalWorkspaceModels(r.Context(), p.Workspace)
	if err != nil {
		failure(w, err)
		return
	}
	models := make([]map[string]any, 0, len(additional)+1)
	for index, item := range append([]ModelConfig{model}, additional...) {
		name := item.Name
		if name == "" {
			name = item.Model
		}
		if keyword != "" && !strings.Contains(strings.ToLower(name), strings.ToLower(keyword)) {
			continue
		}
		models = append(models, map[string]any{"id": item.ID, "name": name,
			"model_type": 0, "base_model": item.Model, "supplier": 15,
			"protocol": 1, "default_model": index == 0, "ws_mapping_count": 1})
	}
	okay(w, models)
}

func (h *Handler) legacyModelRead(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	model, _, isDefault, err := h.legacyModelByID(r, p, token, r.PathValue("id"))
	if err != nil {
		failure(w, err)
		return
	}
	name := model.Name
	if name == "" {
		name = model.Model
	}
	okay(w, map[string]any{"id": model.ID, "name": name,
		"model_type": 0, "base_model": model.Model, "supplier": 15,
		"protocol": 1, "default_model": isDefault, "ws_mapping_count": 1,
		"api_domain": model.Endpoint, "api_key": "", "has_api_key": model.APIKey != "",
		"config_list": []any{}})
}

// The active default cannot be deleted; secondary models can be removed with
// an audited write without affecting query execution.
func (h *Handler) legacyModelDelete(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	model, _, isDefault, err := h.legacyModelByID(r, p, token, r.PathValue("id"))
	if err != nil {
		failure(w, err)
		return
	}
	if isDefault {
		writeJSON(w, http.StatusConflict, "Default model cannot be deleted")
		return
	}
	if err := h.service.Store.DeleteAdditionalWorkspaceModel(r.Context(), p, model.ID); err != nil {
		failure(w, err)
		return
	}
	okay(w, nil)
}

func (h *Handler) legacyModelSetDefault(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	model, _, isDefault, err := h.legacyModelByID(r, p, token, r.PathValue("action"))
	if err != nil {
		failure(w, err)
		return
	}
	if !isDefault {
		if err := h.service.Store.SetDefaultWorkspaceModel(r.Context(), p, model.ID); err != nil {
			failure(w, err)
			return
		}
	}
	okay(w, nil)
}

func (h *Handler) legacyModelPutTwoSegments(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if r.PathValue("id") == "default" {
		h.legacyModelSetDefault(w, r, p, token)
		return
	}
	if r.PathValue("action") == "ws_mapping" {
		h.legacyModelMappingsUpdate(w, r, p, token)
		return
	}
	failure(w, ErrNotFound)
}

// A workspace model belongs only to its current workspace. Keep the original
// mapping dialog truthful while refusing cross-workspace model sharing until
// the multi-model catalog has an explicit permission and audit contract.
func (h *Handler) legacyModelMappings(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	_, _, _, err := h.legacyModelByID(r, p, token, r.PathValue("id"))
	if err != nil {
		failure(w, err)
		return
	}
	okay(w, []string{string(p.Workspace)})
}

func (h *Handler) legacyModelMappingsUpdate(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	_, _, _, err := h.legacyModelByID(r, p, token, r.PathValue("id"))
	if err != nil {
		failure(w, err)
		return
	}
	var spaces []string
	if !readBody(w, r, &spaces) {
		return
	}
	if len(spaces) != 1 || spaces[0] != string(p.Workspace) {
		failure(w, ErrInvalidAccountChange)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	okay(w, spaces)
}

// The original dialog uses POST to add and DELETE to remove mappings. Only a
// duplicate add (or an empty change) can be honored before cross-workspace
// model ownership exists. The sole mapping must never be removed silently.
func (h *Handler) legacyModelMappingsChange(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	_, _, _, err := h.legacyModelByID(r, p, token, r.PathValue("id"))
	if err != nil {
		failure(w, err)
		return
	}
	var spaces []string
	if !readBody(w, r, &spaces) {
		return
	}
	if len(spaces) > 100 {
		failure(w, ErrInvalidAccountChange)
		return
	}
	for _, space := range spaces {
		if !ID(space).Valid() || space != string(p.Workspace) {
			writeJSON(w, http.StatusUnprocessableEntity, "Cross-workspace model sharing is unavailable")
			return
		}
	}
	if r.Method == http.MethodDelete && len(spaces) != 0 {
		writeJSON(w, http.StatusConflict, "Cannot remove the workspace's only model mapping")
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	okay(w, []string{string(p.Workspace)})
}

// The original Community form includes an empty id on create and RSA
// encrypts its endpoint and key. A new catalog entry is non-default when the
// current workspace already has a default model.
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
	var current ModelConfig
	_, existingErr := h.service.Store.get(r.Context(), objectKey{"model", string(p.Workspace), "workspace", "default"}, &current)
	if existingErr == nil && validateModelConfig(current) != nil {
		failure(w, ErrUnavailable)
		return
	}
	if existingErr != nil && !errors.Is(existingErr, ErrNotFound) {
		failure(w, existingErr)
		return
	}
	if existingErr == nil {
		err = h.service.Store.CreateAdditionalWorkspaceModel(r.Context(), p, update)
	} else {
		err = h.service.Store.CreateWorkspaceModel(r.Context(), p, update)
	}
	if err != nil {
		failure(w, err)
		return
	}
	okay(w, nil)
}

// The pinned original browser encrypts both endpoint and nonempty API key
// with the same installation RSA public key as its login form. This adapter
// edits only a model owned by the current workspace. Unsupported providers,
// advanced arguments and default switching still fail closed.
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
		input.DefaultModel == nil || len(input.ConfigList) != 0 {
		failure(w, ErrInvalidAccountChange)
		return
	}
	current, revision, isDefault, err := h.legacyModelByID(r, p, token, string(input.ID))
	if err != nil {
		failure(w, err)
		return
	}
	if *input.DefaultModel != isDefault {
		writeJSON(w, http.StatusConflict, "Default model switch is unavailable")
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
	if isDefault {
		err = h.service.Store.UpdateWorkspaceModel(r.Context(), p, update)
	} else {
		err = h.service.Store.UpdateAdditionalWorkspaceModel(r.Context(), p, current.ID, update)
	}
	if err != nil {
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
	model, _, _, err := h.legacyModelByID(r, p, token, input.ID)
	if err != nil {
		failure(w, err)
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

func (h *Handler) legacyModelByID(r *http.Request, p Principal, token, id string) (ModelConfig, int64, bool, error) {
	defaultModel, err := h.currentLegacyModel(r, p, token)
	if err != nil {
		return ModelConfig{}, 0, false, err
	}
	if id == "" || id == string(defaultModel.ID) {
		revision, err := h.service.Store.get(r.Context(), objectKey{"model", string(p.Workspace), "workspace", "default"}, &defaultModel)
		return defaultModel, revision, true, err
	}
	if !ID(id).Valid() {
		return ModelConfig{}, 0, false, ErrNotFound
	}
	var model ModelConfig
	revision, err := h.service.Store.get(r.Context(), additionalModelKey(p.Workspace, ID(id)), &model)
	if err != nil {
		return ModelConfig{}, 0, false, err
	}
	if model.ID != ID(id) || validateModelConfig(model) != nil {
		return ModelConfig{}, 0, false, ErrUnavailable
	}
	return model, revision, false, nil
}
