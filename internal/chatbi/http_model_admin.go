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
	var lease modelLease
	active, err := h.service.Store.modelCatalogIsActive(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	var revision int64
	if active {
		model, lease, err = h.service.Store.modelForWorkspace(r.Context(), p.Workspace)
		if err == nil {
			var view catalogModelView
			view, err = h.service.Store.catalogAdminModel(r.Context(), p, model.ID)
			revision = view.Revision
		}
	} else {
		revision, err = h.service.Store.get(r.Context(), objectKey{"model", string(p.Workspace), "workspace", "default"}, &model)
	}
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
	if active {
		if err := h.service.Store.modelLeaseUnchanged(r.Context(), p.Workspace, model, lease); err != nil {
			failure(w, err)
			return
		}
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
	active, err := h.service.Store.modelCatalogIsActive(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	if active {
		model, _, err := h.service.Store.modelForWorkspace(r.Context(), p.Workspace)
		if err == nil {
			err = h.service.Store.UpdateGlobalModel(r.Context(), p, model.ID, update)
		}
	} else {
		err = h.service.Store.UpdateWorkspaceModel(r.Context(), p, update)
	}
	if err != nil {
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
	active, err := h.service.Store.modelCatalogIsActive(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	if active {
		_, err = h.service.Store.CreateGlobalModel(r.Context(), p, update)
	} else {
		err = h.service.Store.CreateWorkspaceModel(r.Context(), p, update)
	}
	if err != nil {
		failure(w, err)
		return
	}
	okay(w, true)
}

// The probe never executes generated SQL and returns no model secret. Its
// lease check rejects a model or default switch during the remote probe.
func (h *Handler) nativeCheckWorkspaceModel(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	if r.ContentLength > 0 {
		failure(w, ErrInvalidAccountChange)
		return
	}
	model, lease, err := h.service.Store.modelForWorkspace(r.Context(), p.Workspace)
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
	if err := h.service.Store.modelLeaseUnchanged(r.Context(), p.Workspace, model, lease); err != nil {
		failure(w, err)
		return
	}
	okay(w, map[string]any{"reachable": true})
}
