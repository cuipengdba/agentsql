package chatbi

import (
	"encoding/json"
	"net/http"
	"strconv"
)

func (h *Handler) assistantWorkspaceSources(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	if r.PathValue("oid") != string(p.Workspace) {
		failure(w, ErrNotFound)
		return
	}
	h.datasources(w, r, p, token)
}

func (h *Handler) assistantWorkspaceModels(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
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
	name := model.Name
	if name == "" {
		name = model.Model
	}
	okay(w, []map[string]any{{"id": model.ID, "name": name, "default_model": true, "supplier": 0}})
}

type legacyAssistantInput struct {
	ID             *ID             `json:"id,omitempty"`
	Workspace      *ID             `json:"oid,omitempty"`
	Name           string          `json:"name"`
	Description    string          `json:"description"`
	Type           int             `json:"type"`
	Domain         string          `json:"domain"`
	Configuration  string          `json:"configuration"`
	UseCustomModel *bool           `json:"enable_custom_model"`
	CustomModel    json.RawMessage `json:"custom_model"`
}

func legacyAssistantSources(configuration string, workspace ID) ([]ID, error) {
	if configuration == "" {
		return nil, nil
	}
	var parsed struct {
		Workspace *ID  `json:"oid"`
		Public    []ID `json:"public_list"`
		Private   []ID `json:"private_list"`
	}
	if json.Unmarshal([]byte(configuration), &parsed) != nil || parsed.Workspace != nil && *parsed.Workspace != workspace {
		return nil, ErrInvalidAssistant
	}
	if len(parsed.Public)+len(parsed.Private) > 32 {
		return nil, ErrInvalidAssistant
	}
	result := make([]ID, 0, len(parsed.Public)+len(parsed.Private))
	seen := map[ID]bool{}
	for _, id := range append(parsed.Public, parsed.Private...) {
		if !id.Valid() || seen[id] {
			return nil, ErrInvalidAssistant
		}
		seen[id] = true
		result = append(result, id)
	}
	return result, nil
}

func legacyAssistantView(app AssistantApplication) map[string]any {
	return map[string]any{
		"id": json.Number(app.ID), "oid": json.Number(app.Workspace),
		"name": app.Name, "description": app.Description, "type": app.Type,
		"domain": app.Domain, "configuration": app.Configuration,
		"create_time": app.Created.UnixMilli(), "app_id": nil, "app_secret": nil,
		"enable_custom_model": app.UseCustomModel, "custom_model": app.CustomModel,
	}
}

func (h *Handler) legacyAssistantList(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	items, err := h.service.Assistants(r.Context(), p)
	if err != nil {
		failure(w, err)
		return
	}
	result := make([]map[string]any, 0, len(items))
	for _, app := range items {
		if app.Type == 4 || (r.URL.Path == "/api/v1/system/assistant/advanced_application" && app.Type != 1) {
			continue
		}
		result = append(result, legacyAssistantView(app))
	}
	okay(w, result)
}

func (h *Handler) legacyAssistantSave(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	var input legacyAssistantInput
	if !readBody(w, r, &input) {
		return
	}
	if input.Type != 0 && input.Type != 1 {
		failure(w, ErrInvalidAssistant)
		return
	}
	var app AssistantApplication
	if r.Method == http.MethodPut {
		if input.ID == nil {
			failure(w, ErrInvalidAssistant)
			return
		}
		var err error
		app, err = h.service.Assistant(r.Context(), p, *input.ID)
		if err != nil {
			failure(w, err)
			return
		}
		if app.Type == 4 {
			failure(w, ErrInvalidAssistant)
			return
		}
	} else if input.ID != nil {
		failure(w, ErrInvalidAssistant)
		return
	} else {
		app.Enabled = true
	}
	if input.Workspace != nil && *input.Workspace != p.Workspace {
		failure(w, ErrInvalidAssistant)
		return
	}
	app.Name, app.Description, app.Type, app.Domain, app.Configuration = input.Name, input.Description, input.Type, input.Domain, input.Configuration
	if input.UseCustomModel != nil {
		app.UseCustomModel = *input.UseCustomModel
		if app.UseCustomModel {
			var selected ID
			if json.Unmarshal(input.CustomModel, &selected) != nil {
				failure(w, ErrInvalidAssistant)
				return
			}
			app.CustomModel = &selected
		} else {
			app.CustomModel = nil
		}
	}
	var err error
	app.DatasourceIDs, err = legacyAssistantSources(input.Configuration, p.Workspace)
	if err != nil {
		failure(w, err)
		return
	}
	saved, err := h.service.SaveAssistant(r.Context(), p, app)
	if err != nil {
		failure(w, err)
		return
	}
	okay(w, legacyAssistantView(saved))
}

func (h *Handler) legacyAssistantDelete(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	item, err := h.service.Assistant(r.Context(), p, ID(r.PathValue("id")))
	if err != nil {
		failure(w, err)
		return
	}
	if item.Type == 4 {
		failure(w, ErrInvalidAssistant)
		return
	}
	if err := h.service.DeleteAssistant(r.Context(), p, item.ID, item.Revision); err != nil {
		failure(w, err)
		return
	}
	okay(w, nil)
}

func (h *Handler) assistantList(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	items, err := h.service.Assistants(r.Context(), p)
	if err != nil {
		failure(w, err)
		return
	}
	okay(w, items)
}

func (h *Handler) assistantRead(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	item, err := h.service.Assistant(r.Context(), p, ID(r.PathValue("id")))
	if err != nil {
		failure(w, err)
		return
	}
	okay(w, item)
}

func (h *Handler) assistantSave(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	var item AssistantApplication
	if !readBody(w, r, &item) {
		return
	}
	if r.Method == http.MethodPut {
		if item.ID != ID(r.PathValue("id")) {
			writeJSON(w, 422, "Assistant ID mismatch")
			return
		}
	} else if item.ID != "" || item.Revision != 0 {
		writeJSON(w, 422, "New assistant must not carry an ID or revision")
		return
	}
	saved, err := h.service.SaveAssistant(r.Context(), p, item)
	if err != nil {
		failure(w, err)
		return
	}
	okay(w, saved)
}

func (h *Handler) assistantDelete(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	revision, err := strconv.ParseInt(r.URL.Query().Get("revision"), 10, 64)
	if err != nil {
		writeJSON(w, 422, "Invalid assistant revision")
		return
	}
	if err := h.service.DeleteAssistant(r.Context(), p, ID(r.PathValue("id")), revision); err != nil {
		failure(w, err)
		return
	}
	okay(w, true)
}
