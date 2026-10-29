package chatbi

import (
	"encoding/json"
	"io"
	"net/http"
)

func (h *Handler) guestSession(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	w.Header().Set("Cache-Control", "no-store")
	r.Body = http.MaxBytesReader(w, r.Body, 1)
	body, bodyErr := io.ReadAll(r.Body)
	if bodyErr != nil || len(body) != 0 {
		writeJSON(w, http.StatusUnprocessableEntity, "Unexpected request body")
		return
	}
	proof, err := h.service.issueGuestResumeProof(r.Context(), p)
	if err != nil {
		failure(w, err)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	okay(w, map[string]string{"resume_token": proof})
}

func (h *Handler) resumeGuestSession(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	w.Header().Set("Cache-Control", "no-store")
	r.Body = http.MaxBytesReader(w, r.Body, 2048)
	raw, err := io.ReadAll(r.Body)
	if err != nil || len(raw) == 0 {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid resume request")
		return
	}
	var input struct {
		ResumeToken string `json:"resume_token"`
	}
	if json.Unmarshal(raw, &input) != nil || input.ResumeToken == "" {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid resume request")
		return
	}
	issued, err := h.service.resumePublicGuest(r.Context(), p, input.ResumeToken)
	if err != nil {
		failure(w, err)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	okay(w, map[string]string{"token": issued})
}
