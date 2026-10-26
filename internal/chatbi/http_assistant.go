package chatbi

import (
	"net/http"
	"strconv"
)

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
