package chatbi

import (
	"encoding/json"
	"net/http"
)

func (h *Handler) accountAPIKeyList(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	keys, err := h.service.Store.ListAccountAPIKeys(r.Context(), p)
	if err != nil {
		failure(w, err)
		return
	}
	views := make([]map[string]any, 0, len(keys))
	for _, item := range keys {
		views = append(views, map[string]any{"id": json.Number(item.ID), "access_key": item.AccessKey,
			"secret_key": item.SecretKey, "create_time": item.Created, "uid": json.Number(item.Owner), "status": item.Status})
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	okay(w, views)
}

func (h *Handler) accountAPIKeyCreate(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	var empty struct{}
	if !readBody(w, r, &empty) {
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	id, err := h.service.Store.CreateAccountAPIKey(r.Context(), p)
	if err != nil {
		failure(w, err)
		return
	}
	okay(w, json.Number(id))
}

func (h *Handler) accountAPIKeyStatus(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	var input struct {
		ID     ID   `json:"id"`
		Status bool `json:"status"`
	}
	if !readBody(w, r, &input) {
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	if err := h.service.Store.ChangeAccountAPIKey(r.Context(), p, input.ID, &input.Status); err != nil {
		failure(w, err)
		return
	}
	okay(w, nil)
}

func (h *Handler) accountAPIKeyDelete(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	id := ID(r.PathValue("id"))
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	if err := h.service.Store.ChangeAccountAPIKey(r.Context(), p, id, nil); err != nil {
		failure(w, err)
		return
	}
	okay(w, nil)
}
