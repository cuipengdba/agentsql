package chatbi

import "net/http"

func (h *Handler) changePasswordAtLogin(w http.ResponseWriter, r *http.Request) {
	if !h.loginLimit.Allow() {
		writeJSON(w, http.StatusTooManyRequests, "Too many login requests")
		return
	}
	var input struct {
		Account string `json:"account"`
		Current string `json:"pwd"`
		New     string `json:"new_pwd"`
	}
	if !readBody(w, r, &input) {
		return
	}
	account, e1 := h.service.Identity.DecryptLogin(input.Account)
	current, e2 := h.service.Identity.DecryptLogin(input.Current)
	replacement, e3 := h.service.Identity.DecryptLogin(input.New)
	if e1 != nil || e2 != nil || e3 != nil {
		writeJSON(w, http.StatusUnauthorized, "Invalid account change")
		return
	}
	if err := h.service.Identity.ChangePasswordByAccount(r.Context(), account, current, replacement, h.initialUserPassword); err != nil {
		failure(w, err)
		return
	}
	okay(w, nil)
}

func (h *Handler) changeOwnPassword(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	var input struct {
		Current string `json:"pwd"`
		New     string `json:"new_pwd"`
	}
	if !readBody(w, r, &input) {
		return
	}
	if err := h.service.Identity.ChangePassword(r.Context(), p, input.Current, input.New, h.initialUserPassword); err != nil {
		failure(w, err)
		return
	}
	okay(w, nil)
}

func (h *Handler) changeOwnLanguage(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	var input struct {
		Language string `json:"language"`
	}
	if !readBody(w, r, &input) {
		return
	}
	if err := h.service.Identity.ChangeLanguage(r.Context(), p, input.Language); err != nil {
		failure(w, err)
		return
	}
	okay(w, nil)
}
