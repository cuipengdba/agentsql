package chatbi

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"sort"
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
	active, err := h.service.Store.modelCatalogIsActive(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	if active {
		if err := h.currentIdentity(r.Context(), p, token); err != nil {
			failure(w, err)
			return
		}
		views, err := h.service.Store.catalogAdminModels(r.Context(), p)
		if err != nil {
			failure(w, err)
			return
		}
		sort.Slice(views, func(i, j int) bool {
			if views[i].Default != views[j].Default {
				return views[i].Default
			}
			if views[i].Model.Name != views[j].Model.Name {
				return views[i].Model.Name < views[j].Model.Name
			}
			return views[i].Model.ID < views[j].Model.ID
		})
		models := make([]map[string]any, 0, len(views))
		for _, view := range views {
			name := view.Model.Name
			if name == "" {
				name = view.Model.Model
			}
			if keyword != "" && !strings.Contains(strings.ToLower(name), strings.ToLower(keyword)) {
				continue
			}
			models = append(models, map[string]any{"id": view.Model.ID, "name": name,
				"model_type": 0, "base_model": view.Model.Model, "supplier": modelSupplier(view.Model),
				"protocol": 1, "default_model": view.Default, "ws_mapping_count": view.MappingCount})
		}
		okay(w, models)
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
			"model_type": 0, "base_model": item.Model, "supplier": modelSupplier(item),
			"protocol": 1, "default_model": index == 0, "ws_mapping_count": 1})
	}
	okay(w, models)
}

func (h *Handler) legacyModelRead(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	active, err := h.service.Store.modelCatalogIsActive(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	var model ModelConfig
	var isDefault bool
	mappingCount := 1
	if active {
		if err := h.currentIdentity(r.Context(), p, token); err != nil {
			failure(w, err)
			return
		}
		id := ID(r.PathValue("id"))
		if !id.Valid() {
			failure(w, ErrNotFound)
			return
		}
		view, err := h.service.Store.catalogAdminModel(r.Context(), p, id)
		if err != nil {
			failure(w, err)
			return
		}
		model, isDefault, mappingCount = view.Model, view.Default, view.MappingCount
	} else {
		model, _, isDefault, err = h.legacyModelByID(r, p, token, r.PathValue("id"))
	}
	if err != nil {
		failure(w, err)
		return
	}
	name := model.Name
	if name == "" {
		name = model.Model
	}
	configList := []any{}
	if model.Temperature != nil {
		configList = append(configList, map[string]any{"key": "temperature", "name": "temperature", "val": *model.Temperature})
	}
	if modelSupplier(model) == 5 && model.MaxTokens != 0 {
		configList = append(configList, map[string]any{"key": "max_tokens", "name": "max_tokens", "val": model.MaxTokens})
	}
	if model.EnableThinking != nil {
		value, _ := json.Marshal(map[string]bool{"enable_thinking": *model.EnableThinking})
		configList = append(configList, map[string]any{"key": "extra_body", "name": "extra_body", "val": string(value)})
	}
	okay(w, map[string]any{"id": model.ID, "name": name,
		"model_type": 0, "base_model": model.Model, "supplier": modelSupplier(model),
		"protocol": 1, "default_model": isDefault, "ws_mapping_count": mappingCount,
		"api_domain": model.Endpoint, "api_key": "", "has_api_key": model.APIKey != "",
		"config_list": configList})
}

// The active default cannot be deleted; secondary models can be removed with
// an audited write without affecting query execution.
func (h *Handler) legacyModelDelete(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	active, err := h.service.Store.modelCatalogIsActive(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	if active {
		if err := h.currentIdentity(r.Context(), p, token); err != nil {
			failure(w, err)
			return
		}
		id := ID(r.PathValue("id"))
		if !id.Valid() {
			failure(w, ErrNotFound)
			return
		}
		view, err := h.service.Store.catalogAdminModel(r.Context(), p, id)
		if err != nil {
			failure(w, err)
			return
		}
		if view.Default {
			writeJSON(w, http.StatusConflict, "Default model cannot be deleted")
			return
		}
		if err := h.service.Store.DeleteGlobalModel(r.Context(), p, id, view.Revision); err != nil {
			failure(w, err)
			return
		}
		okay(w, nil)
		return
	}
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
	active, err := h.service.Store.modelCatalogIsActive(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	if active {
		if err := h.currentIdentity(r.Context(), p, token); err != nil {
			failure(w, err)
			return
		}
		id := ID(r.PathValue("action"))
		if !id.Valid() {
			failure(w, ErrNotFound)
			return
		}
		if err := h.service.Store.SetGlobalDefaultModel(r.Context(), p, id); err != nil {
			failure(w, err)
			return
		}
		okay(w, nil)
		return
	}
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

// Before catalog activation, a workspace model belongs only to its current
// workspace. Active catalog mappings use the shared encrypted model record.
func (h *Handler) legacyModelMappings(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	active, err := h.service.Store.modelCatalogIsActive(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	if active {
		if err := h.currentIdentity(r.Context(), p, token); err != nil {
			failure(w, err)
			return
		}
		id := ID(r.PathValue("id"))
		mappings, _, err := h.service.Store.catalogModelMappings(r.Context(), p, id)
		if err != nil {
			failure(w, err)
			return
		}
		okay(w, mappings)
		return
	}
	_, _, _, err = h.legacyModelByID(r, p, token, r.PathValue("id"))
	if err != nil {
		failure(w, err)
		return
	}
	okay(w, []string{string(p.Workspace)})
}

func (h *Handler) legacyModelMappingsUpdate(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if h.activeCatalogMappingChange(w, r, p, token, catalogMappingsReplace) {
		return
	}
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

// The original dialog uses POST to add and DELETE to remove mappings. Before
// activation, only a duplicate add (or empty change) can be honored.
func (h *Handler) legacyModelMappingsChange(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	mode := catalogMappingsAdd
	if r.Method == http.MethodDelete {
		mode = catalogMappingsRemove
	}
	if h.activeCatalogMappingChange(w, r, p, token, mode) {
		return
	}
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

// activeCatalogMappingChange dispatches only after the activation marker is
// valid. A concurrent cutover or revision change fails closed in the store.
func (h *Handler) activeCatalogMappingChange(w http.ResponseWriter, r *http.Request, p Principal,
	token string, mode catalogMappingChange) bool {
	active, err := h.service.Store.modelCatalogIsActive(r.Context())
	if err != nil {
		failure(w, err)
		return true
	}
	if !active {
		return false
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return true
	}
	id := ID(r.PathValue("id"))
	if !id.Valid() {
		failure(w, ErrNotFound)
		return true
	}
	var spaces []ID
	if !readBody(w, r, &spaces) {
		return true
	}
	_, revision, err := h.service.Store.catalogModelMappings(r.Context(), p, id)
	if err != nil {
		failure(w, err)
		return true
	}
	mappings, err := h.service.Store.changeCatalogModelMappings(r.Context(), p, id, spaces, mode, revision)
	if err != nil {
		failure(w, err)
		return true
	}
	okay(w, mappings)
	return true
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
		ID         string            `json:"id"`
		Name       string            `json:"name"`
		ModelType  *int              `json:"model_type"`
		BaseModel  string            `json:"base_model"`
		Supplier   *int              `json:"supplier"`
		Protocol   *int              `json:"protocol"`
		APIDomain  string            `json:"api_domain"`
		APIKey     string            `json:"api_key"`
		ConfigList []json.RawMessage `json:"config_list"`
	}
	if !readBody(w, r, &input) {
		return
	}
	if input.ID != "" || input.ModelType == nil || *input.ModelType != 0 ||
		input.Supplier == nil || !supportedLegacySupplier(*input.Supplier) || input.Protocol == nil || *input.Protocol != 1 {
		failure(w, ErrInvalidAccountChange)
		return
	}
	arguments, err := parseLegacyModelArguments(input.ConfigList, *input.Supplier)
	if err != nil {
		failure(w, err)
		return
	}
	config, err := json.Marshal(input.ConfigList)
	if err != nil {
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
	update := WorkspaceModelUpdate{Name: input.Name, Model: input.BaseModel, Endpoint: endpoint, APIKey: apiKey, Temperature: arguments.Temperature, MaxTokens: arguments.MaxTokens, Supplier: *input.Supplier, EnableThinking: arguments.EnableThinking}
	active, err := h.service.Store.modelCatalogIsActive(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	if active {
		id, err := h.service.Store.CreateGlobalModel(r.Context(), p, update)
		if err != nil {
			failure(w, err)
			return
		}
		view, err := h.service.Store.catalogAdminModel(r.Context(), p, id)
		if err != nil {
			failure(w, err)
			return
		}
		okay(w, legacyCreatedModel(id, input.Name, input.BaseModel, *input.Supplier,
			input.APIDomain, input.APIKey, string(config), view.Default))
		return
	}
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
	var id ID
	if existingErr == nil {
		id, err = h.service.Store.CreateAdditionalWorkspaceModelWithID(r.Context(), p, update)
	} else {
		id, err = h.service.Store.CreateWorkspaceModelWithID(r.Context(), p, update)
	}
	if err != nil {
		failure(w, err)
		return
	}
	okay(w, legacyCreatedModel(id, input.Name, input.BaseModel, *input.Supplier,
		input.APIDomain, input.APIKey, string(config), existingErr != nil))
}

// SQLBot's create response carries the new ID. Keep encrypted request values
// encrypted in the response so existing clients can read the record without
// exposing the endpoint or API key stored in the server-side model.
func legacyCreatedModel(id ID, name, baseModel string, supplier int, domain, key, config string, isDefault bool) map[string]any {
	return map[string]any{
		"id": id, "name": name, "base_model": baseModel,
		"supplier": supplier, "model_type": 0, "protocol": 1,
		"api_domain": domain, "api_key": key, "config": config,
		"default_model": isDefault, "status": 1, "create_time": time.Now().UnixMilli(),
	}
}

// The pinned original browser encrypts both endpoint and nonempty API key
// with the same installation RSA public key as its login form. This adapter
// edits only a model owned by the current workspace. Supplier arguments are
// limited to the pinned Community browser controls; other values fail closed.
func (h *Handler) legacyModelUpdate(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	var input struct {
		ID           ID                `json:"id"`
		Name         string            `json:"name"`
		ModelType    *int              `json:"model_type"`
		BaseModel    string            `json:"base_model"`
		Supplier     *int              `json:"supplier"`
		Protocol     *int              `json:"protocol"`
		DefaultModel *bool             `json:"default_model"`
		APIDomain    string            `json:"api_domain"`
		APIKey       string            `json:"api_key"`
		ConfigList   []json.RawMessage `json:"config_list"`
	}
	if !readBody(w, r, &input) {
		return
	}
	if !input.ID.Valid() || input.ModelType == nil || *input.ModelType != 0 ||
		input.Supplier == nil || !supportedLegacySupplier(*input.Supplier) || input.Protocol == nil || *input.Protocol != 1 ||
		input.DefaultModel == nil {
		failure(w, ErrInvalidAccountChange)
		return
	}
	arguments, err := parseLegacyModelArguments(input.ConfigList, *input.Supplier)
	if err != nil {
		failure(w, err)
		return
	}
	active, err := h.service.Store.modelCatalogIsActive(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	var current ModelConfig
	var revision int64
	var isDefault bool
	if active {
		if err := h.currentIdentity(r.Context(), p, token); err != nil {
			failure(w, err)
			return
		}
		view, err := h.service.Store.catalogAdminModel(r.Context(), p, input.ID)
		if err != nil {
			failure(w, err)
			return
		}
		current, revision, isDefault = view.Model, view.Revision, view.Default
	} else {
		current, revision, isDefault, err = h.legacyModelByID(r, p, token, string(input.ID))
	}
	if err != nil {
		failure(w, err)
		return
	}
	if modelSupplier(current) != *input.Supplier {
		failure(w, ErrInvalidAccountChange)
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
	maxTokens := current.MaxTokens
	if *input.Supplier == 5 {
		maxTokens = arguments.MaxTokens
	}
	update := WorkspaceModelUpdate{Revision: revision, Name: input.Name, Model: input.BaseModel,
		Endpoint: endpoint, APIKey: apiKey, MaxTokens: maxTokens, Temperature: arguments.Temperature, ReplaceTemperature: true, Supplier: *input.Supplier,
		EnableThinking: arguments.EnableThinking, ReplaceThinking: true}
	if active {
		err = h.service.Store.UpdateGlobalModel(r.Context(), p, current.ID, update)
	} else if isDefault {
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
	active, err := h.service.Store.modelCatalogIsActive(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	var model ModelConfig
	var revision int64
	if active {
		if err := h.currentIdentity(r.Context(), p, token); err != nil {
			failure(w, err)
			return
		}
		id := ID(input.ID)
		if !id.Valid() {
			failure(w, ErrNotFound)
			return
		}
		view, err := h.service.Store.catalogAdminModel(r.Context(), p, id)
		if err != nil {
			failure(w, err)
			return
		}
		model, revision = view.Model, view.Revision
	} else {
		model, _, _, err = h.legacyModelByID(r, p, token, input.ID)
	}
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
	if active {
		view, err := h.service.Store.catalogAdminModel(r.Context(), p, model.ID)
		if err != nil || view.Revision != revision {
			failure(w, ErrModel)
			return
		}
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

type legacyModelArguments struct {
	Temperature    *float64
	MaxTokens      int
	EnableThinking *bool
}

func parseLegacyModelArguments(items []json.RawMessage, supplier int) (legacyModelArguments, error) {
	var result legacyModelArguments
	if len(items) > 3 {
		return result, ErrInvalidAccountChange
	}
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		var fields map[string]json.RawMessage
		if json.Unmarshal(item, &fields) != nil || len(fields) != 3 {
			return result, ErrInvalidAccountChange
		}
		var key, name string
		if json.Unmarshal(fields["key"], &key) != nil || json.Unmarshal(fields["name"], &name) != nil || key != name || seen[key] || strings.TrimSpace(string(fields["val"])) == "null" {
			return result, ErrInvalidAccountChange
		}
		seen[key] = true
		switch key {
		case "temperature":
			numeric, err := legacyModelNumericValue(fields["val"])
			if err != nil {
				return result, err
			}
			var value float64
			if json.Unmarshal(numeric, &value) != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 2 || value > 1 && supplier != 1 && supplier != 4 && supplier != 5 && supplier != 7 {
				return result, ErrInvalidAccountChange
			}
			result.Temperature = &value
		case "max_tokens":
			numeric, err := legacyModelNumericValue(fields["val"])
			if err != nil {
				return result, err
			}
			if supplier != 5 || json.Unmarshal(numeric, &result.MaxTokens) != nil || result.MaxTokens < 1 || result.MaxTokens > 32768 {
				return result, ErrInvalidAccountChange
			}
		case "extra_body":
			if supplier != 1 {
				return result, ErrInvalidAccountChange
			}
			var encoded string
			if json.Unmarshal(fields["val"], &encoded) != nil || len(encoded) > 128 {
				return result, ErrInvalidAccountChange
			}
			var body map[string]json.RawMessage
			if json.Unmarshal([]byte(encoded), &body) != nil || len(body) != 1 || json.Unmarshal(body["enable_thinking"], &result.EnableThinking) != nil || result.EnableThinking == nil {
				return result, ErrInvalidAccountChange
			}
		default:
			return result, ErrInvalidAccountChange
		}
	}
	return result, nil
}

// The pinned Community editor submits numeric parameter values as strings.
// Parse those with the same JSON-number grammar as native numeric values.
func legacyModelNumericValue(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || raw[0] != '"' {
		return raw, nil
	}
	var text string
	if json.Unmarshal(raw, &text) != nil || len(text) == 0 || len(text) > 32 ||
		strings.TrimSpace(text) != text || text[0] != '-' && (text[0] < '0' || text[0] > '9') {
		return nil, ErrInvalidAccountChange
	}
	return json.RawMessage(text), nil
}
