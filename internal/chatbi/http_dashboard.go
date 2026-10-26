package chatbi

import (
	"net/http"
	"strconv"
)

func (h *Handler) dashboardList(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	items, err := h.service.Dashboards(r.Context(), p)
	if err != nil {
		failure(w, err)
		return
	}
	okay(w, items)
}

func (h *Handler) dashboardRead(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	view, err := h.service.Dashboard(r.Context(), p, r.PathValue("id"), true)
	if err != nil {
		failure(w, err)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	okay(w, view)
}

func (h *Handler) dashboardSave(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	var item Dashboard
	if !readBody(w, r, &item) {
		return
	}
	if r.Method == http.MethodPut {
		if item.ID != r.PathValue("id") {
			writeJSON(w, 422, "Dashboard ID mismatch")
			return
		}
	} else if item.ID != "" || item.Revision != 0 {
		writeJSON(w, 422, "New dashboard must not carry an ID or revision")
		return
	}
	saved, err := h.service.SaveDashboard(r.Context(), p, item)
	if err != nil {
		failure(w, err)
		return
	}
	okay(w, saved)
}

func (h *Handler) dashboardDelete(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	revision, err := strconv.ParseInt(r.URL.Query().Get("revision"), 10, 64)
	if err != nil {
		writeJSON(w, 422, "Invalid dashboard revision")
		return
	}
	if err := h.service.DeleteDashboard(r.Context(), p, r.PathValue("id"), revision); err != nil {
		failure(w, err)
		return
	}
	okay(w, true)
}
