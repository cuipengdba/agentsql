package chatbi

import (
	"net/http"
)

type nativeRecommendationInput struct {
	Revision  *int64               `json:"revision"`
	Questions []RecommendedProblem `json:"questions"`
}

func recommendationID(w http.ResponseWriter, r *http.Request) (ID, bool) {
	id := ID(r.PathValue("id"))
	if !id.Valid() {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid datasource ID")
		return "", false
	}
	return id, true
}

func (h *Handler) nativeRecommendationRead(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	id, valid := recommendationID(w, r)
	if !valid {
		return
	}
	config, revision, err := h.service.RecommendationSettings(r.Context(), p, id)
	if err != nil {
		failure(w, err)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	okay(w, map[string]any{"datasource_id": id, "revision": revision, "questions": config.Questions})
}

func (h *Handler) nativeRecommendationSave(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	id, valid := recommendationID(w, r)
	if !valid {
		return
	}
	var input nativeRecommendationInput
	if !readBody(w, r, &input) {
		return
	}
	if input.Revision == nil {
		writeJSON(w, http.StatusUnprocessableEntity, "Missing recommendation revision")
		return
	}
	config, revision, err := h.service.SaveRecommendations(r.Context(), p, id, *input.Revision, input.Questions)
	if err != nil {
		failure(w, err)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	okay(w, map[string]any{"datasource_id": id, "revision": revision, "questions": config.Questions})
}
