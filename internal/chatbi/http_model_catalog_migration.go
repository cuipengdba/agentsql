package chatbi

import "net/http"

func (h *Handler) nativeModelCatalogPreflight(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.nativeModelAdmin(r, p, token); err != nil {
		failure(w, err)
		return
	}
	if active, err := h.service.Store.modelCatalogIsActive(r.Context()); err != nil || active {
		if err != nil {
			failure(w, err)
		} else {
			failure(w, ErrConflict)
		}
		return
	}
	snapshot, err := h.service.Store.readModelMigrationSnapshot(r.Context(), h.service.Store.db)
	if err != nil {
		failure(w, err)
		return
	}
	okay(w, map[string]any{"preview": snapshot.preview, "source_digest": snapshot.digest})
}

func (h *Handler) nativeModelCatalogStage(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.nativeModelAdmin(r, p, token); err != nil {
		failure(w, err)
		return
	}
	var input struct {
		Default ID `json:"default_model"`
	}
	if !readBody(w, r, &input) {
		return
	}
	if err := h.service.Store.StageModelCatalogMigration(r.Context(), p, input.Default); err != nil {
		failure(w, err)
		return
	}
	var staged stagedModelCatalog
	if _, err := h.service.Store.get(r.Context(), modelCatalogStageKey(), &staged); err != nil {
		failure(w, err)
		return
	}
	okay(w, staged)
}

func (h *Handler) nativeModelCatalogActivate(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.nativeModelAdmin(r, p, token); err != nil {
		failure(w, err)
		return
	}
	var input struct {
		SourceDigest string `json:"source_digest"`
	}
	if !readBody(w, r, &input) {
		return
	}
	if len(input.SourceDigest) != 64 {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid source digest")
		return
	}
	if err := h.service.Store.ActivateModelCatalogMigration(r.Context(), p, input.SourceDigest); err != nil {
		failure(w, err)
		return
	}
	okay(w, map[string]bool{"active": true})
}
