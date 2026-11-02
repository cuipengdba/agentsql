package chatbi

import "net/http"

func (h *Handler) switchOwnWorkspace(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	target := ID(r.PathValue("oid"))
	if !target.Valid() || !p.User.canUse(target) || p.EmbeddedApp != "" {
		failure(w, ErrUnauthenticated)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	if err := h.service.Store.SwitchWorkspace(r.Context(), p, token, target); err != nil {
		failure(w, err)
		return
	}
	current, err := h.service.Identity.Verify(r.Context(), token)
	if err != nil || current.User.ID != p.User.ID || current.Workspace != target {
		failure(w, ErrUnauthenticated)
		return
	}
	okay(w, nil)
}
