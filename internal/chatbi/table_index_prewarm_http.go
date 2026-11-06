package chatbi

import "net/http"

// Status is scoped to the authenticated user's current binding. It contains
// no schema text, question, SQL, model prompt, credential or failure detail.
func (h *Handler) tableIndexPrewarmStatus(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	datasource := ID(r.PathValue("id"))
	if !datasource.Valid() {
		failure(w, ErrNotFound)
		return
	}
	if _, _, _, err := h.service.bindingSnapshot(r.Context(), p, datasource); err != nil {
		failure(w, err)
		return
	}
	okay(w, h.service.TableIndexPrewarmStatus(p, datasource))
}
