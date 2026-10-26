package chatbi

import (
	"encoding/json"
	"net/http"
)

type nativeRecommendationInput struct {
	Revision  *int64               `json:"revision"`
	Questions []RecommendedProblem `json:"questions"`
}

type legacyRecommendationInput struct {
	Datasource ID   `json:"datasource_id"`
	Mode       *int `json:"recommended_config"`
	Problems   []struct {
		ID         json.RawMessage `json:"id"`
		Datasource json.RawMessage `json:"datasource_id"`
		Question   string          `json:"question"`
		Remark     string          `json:"remark"`
	} `json:"problemInfo"`
}

func legacyOptionalRecommendationID(raw json.RawMessage) (ID, error) {
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "0" || string(raw) == `"0"` {
		return "", nil
	}
	var id ID
	if err := json.Unmarshal(raw, &id); err != nil {
		return "", err
	}
	return id, nil
}

// Keep the original SQLBot write contract while preserving AgentSQL's
// administrator, binding, compare-and-swap, and durable audit requirements.
func (h *Handler) legacySaveRecommendations(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	var input legacyRecommendationInput
	if !readBody(w, r, &input) {
		return
	}
	if !input.Datasource.Valid() || input.Mode == nil || (*input.Mode != 0 && *input.Mode != 2) ||
		len(input.Problems) > 20 || (*input.Mode == 0 && len(input.Problems) != 0) ||
		(*input.Mode == 2 && len(input.Problems) == 0) {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid recommendations")
		return
	}
	questions := make([]RecommendedProblem, 0, len(input.Problems))
	for _, item := range input.Problems {
		id, err := legacyOptionalRecommendationID(item.ID)
		if err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, "Invalid recommendation ID")
			return
		}
		itemDatasource, err := legacyOptionalRecommendationID(item.Datasource)
		if err != nil || itemDatasource != "" && itemDatasource != input.Datasource {
			writeJSON(w, http.StatusUnprocessableEntity, "Invalid recommendation datasource")
			return
		}
		questions = append(questions, RecommendedProblem{ID: id, Question: item.Question, Remark: item.Remark})
	}
	_, revision, err := h.service.RecommendationSettings(r.Context(), p, input.Datasource)
	if err != nil {
		failure(w, err)
		return
	}
	if _, _, err = h.service.SaveRecommendations(r.Context(), p, input.Datasource, revision, questions); err != nil {
		failure(w, err)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	okay(w, nil)
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
