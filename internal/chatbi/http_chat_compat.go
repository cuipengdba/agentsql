package chatbi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
)

// SQLBot's usage widget reads metadata for the caller's own persisted turn.
// It must never replay or expose cached result rows.
func (h *Handler) recordUsage(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	id := ID(r.PathValue("id"))
	if !id.Valid() {
		writeJSON(w, 422, "Invalid ID")
		return
	}
	record, err := h.service.Record(r.Context(), p, id, false)
	if err != nil {
		failure(w, err)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	okay(w, map[string]any{
		"start_time": record.Created, "finish_time": record.FinishedAt,
		"duration": record.Duration, "total_tokens": record.TotalTokens,
		"steps": []any{},
	})
}

// The fixed SQLBot browser requests this stream for both the first-chat
// placeholder and completed turns. The current implementation returns only
// administrator-configured suggestions; it does not call a model or imply
// generated follow-up questions.
func (h *Handler) recommendQuestions(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	id := ID(r.PathValue("id"))
	if !id.Valid() {
		writeJSON(w, 422, "Invalid ID")
		return
	}
	limit := 4
	if raw := r.URL.Query().Get("articles_number"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 10 {
			writeJSON(w, 422, "Invalid recommendation limit")
			return
		}
		limit = parsed
	}
	var datasource ID
	record, err := h.service.Record(r.Context(), p, id, false)
	if err == nil {
		datasource = record.Datasource
	} else if errors.Is(err, ErrNotFound) {
		chat, _, chatErr := h.service.chat(r.Context(), p, id)
		if chatErr != nil {
			failure(w, chatErr)
			return
		}
		datasource = chat.Datasource
	} else {
		failure(w, err)
		return
	}
	config, err := h.service.Recommendations(r.Context(), p, datasource)
	if err != nil {
		failure(w, err)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	questions := make([]string, 0, limit)
	if config.Mode == 2 {
		for _, item := range config.Questions {
			if len(questions) == limit {
				break
			}
			questions = append(questions, item.Question)
		}
	}
	content, err := json.Marshal(questions)
	if err != nil {
		failure(w, ErrUnavailable)
		return
	}
	event, err := json.Marshal(map[string]any{"type": "recommended_question", "content": string(content)})
	if err != nil {
		failure(w, ErrUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(append(append([]byte("data:"), event...), '\n', '\n'))
}
