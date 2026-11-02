package chatbi

import (
	"errors"
	"net/http"
)

func (h *Handler) nativeForecast(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	id := ID(r.PathValue("id"))
	if !id.Valid() {
		writeJSON(w, 422, "Invalid record ID")
		return
	}
	result, err := h.service.ForecastRecord(r.Context(), p, id)
	if err != nil {
		failure(w, err)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	okay(w, result)
}

func (h *Handler) legacyForecastData(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	id := ID(r.PathValue("id"))
	if !id.Valid() {
		writeJSON(w, 422, "Invalid record ID")
		return
	}
	child, err := h.service.Record(r.Context(), p, id, true)
	if err != nil {
		failure(w, err)
		return
	}
	if child.Action != "predict" || !child.PredictOf.Valid() || child.Data == nil {
		failure(w, ErrNotFound)
		return
	}
	result, err := h.service.ForecastRecord(r.Context(), p, child.PredictOf)
	if err != nil {
		if !errors.Is(err, ErrForecastUnavailable) {
			failure(w, err)
			return
		}
		// SQLBot's unchanged page expects an empty array when the saved
		// prediction has no numeric points. The child has already passed a
		// fresh, authorized and audited result-digest check above.
		if err := h.currentIdentity(r.Context(), p, token); err != nil {
			failure(w, err)
			return
		}
		okay(w, []map[string]any{})
		return
	}
	if result.digest != resultDigest(*child.Data) {
		failure(w, ErrGateway)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	rows := make([]map[string]any, 0, len(result.Points))
	for _, point := range result.Points {
		rows = append(rows, map[string]any{result.LabelField: point.Label, result.ValueField: point.Value})
	}
	okay(w, rows)
}
