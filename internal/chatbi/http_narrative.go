package chatbi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

func narrativeAction(r *http.Request) string {
	if strings.HasSuffix(r.URL.Path, "/analysis") {
		return "analysis"
	}
	if strings.HasSuffix(r.URL.Path, "/predict") {
		return "predict"
	}
	return ""
}

func (h *Handler) narrative(w http.ResponseWriter, r *http.Request, p Principal, token string) (Narrative, bool) {
	id := ID(r.PathValue("id"))
	if !id.Valid() {
		writeJSON(w, 422, "Invalid record ID")
		return Narrative{}, false
	}
	result, err := h.service.NarrateRecord(r.Context(), p, id, narrativeAction(r))
	if err != nil {
		failure(w, err)
		return Narrative{}, false
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return Narrative{}, false
	}
	return result, true
}

func (h *Handler) nativeNarrative(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	result, ok := h.narrative(w, r, p, token)
	if ok {
		okay(w, result)
	}
}

func (h *Handler) legacyNarrative(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	result, ok := h.narrative(w, r, p, token)
	if !ok {
		return
	}
	forecastAvailable := false
	if result.Kind == "predict" {
		_, err := h.service.ForecastRecord(r.Context(), p, result.RecordID)
		if err == nil {
			forecastAvailable = true
		} else if !errors.Is(err, ErrForecastUnavailable) {
			failure(w, err)
			return
		}
	}
	childID, err := h.service.SaveNarrative(r.Context(), p, result)
	if err != nil {
		failure(w, err)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	// The fixed browser accepts a single content event followed by the same
	// terminal event as its streamed model response. No partial, unaudited text
	// is emitted before the controlled query and final identity check pass.
	typeName := result.Kind + "-result"
	finishName := result.Kind + "_finish"
	if result.Kind == "predict" {
		finishName = "predict_finish"
	}
	first, _ := json.Marshal(map[string]any{"type": typeName, "content": result.Content, "reasoning_content": ""})
	idEvent, _ := json.Marshal(map[string]any{"type": "id", "id": childID})
	last, _ := json.Marshal(map[string]any{"type": finishName})
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(append(append([]byte("data:"), idEvent...), '\n', '\n'))
	_, _ = w.Write(append(append([]byte("data:"), first...), '\n', '\n'))
	if result.Kind == "predict" {
		name := "predict-failed"
		if forecastAvailable {
			name = "predict-success"
		}
		state, _ := json.Marshal(map[string]any{"type": name})
		_, _ = w.Write(append(append([]byte("data:"), state...), '\n', '\n'))
	}
	_, _ = w.Write(append(append([]byte("data:"), last...), '\n', '\n'))
}
