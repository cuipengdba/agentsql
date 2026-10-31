package chatbi

import (
	"math"
	"net/http"
)

func (h *Handler) legacyUserResetPassword(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	id := ID(r.PathValue("id"))
	if !id.Valid() {
		failure(w, ErrInvalidAccountChange)
		return
	}
	var input struct {
		NewPassword string `json:"new_password"`
	}
	if r.ContentLength != 0 && !readBody(w, r, &input) {
		return
	}
	password := input.NewPassword
	if password == "" {
		password = h.initialUserPassword
	}
	hash, err := HashPassword(password)
	if err != nil {
		failure(w, ErrInvalidAccountChange)
		return
	}
	user, revision, err := h.service.Store.User(r.Context(), id)
	if err != nil {
		failure(w, err)
		return
	}
	if user.Admin || user.ID == p.User.ID || user.AuthRevision == math.MaxInt64 {
		failure(w, ErrUnauthenticated)
		return
	}
	user.PasswordHash, user.PasswordFormat = hash, "bcrypt"
	user.AuthRevision++
	user.AllowLegacyTokens = false
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	if err := h.service.Store.updateUserManaged(r.Context(), user, revision, p, "user.password.reset"); err != nil {
		failure(w, err)
		return
	}
	okay(w, nil)
}
