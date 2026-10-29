package chatbi

import (
	"net/http"
	"reflect"
)

// SQLBot's edit form posts its full display configuration when testing a
// connection. Only an existing server-owned binding can be probed here; the
// browser's public AES envelope never supplies a connection target.
func (h *Handler) checkBoundDatasource(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	var input struct {
		ID            ID     `json:"id"`
		Type          string `json:"type"`
		Configuration string `json:"configuration"`
	}
	if !readBody(w, r, &input) {
		return
	}
	if !input.ID.Valid() {
		writeJSON(w, http.StatusUnprocessableEntity, "An existing datasource ID is required")
		return
	}
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	binding, revision, digest, err := h.service.bindingSnapshot(r.Context(), p, input.ID)
	if err != nil {
		failure(w, err)
		return
	}
	if databaseBinding(binding) {
		if input.Type != binding.Dialect || !validDatabaseMetadataConfiguration(input.Configuration) {
			writeJSON(w, http.StatusUnprocessableEntity, "Database connection settings cannot be changed")
			return
		}
	} else if binding.Type == "excel" && binding.Dialect == "pg" &&
		managedFileOperationPattern.MatchString(binding.ManagedFileID) {
		oldFile, oldSheets, oldErr := decodeSQLBotExcelConfig(binding.Configuration)
		newFile, newSheets, newErr := decodeSQLBotExcelConfig(input.Configuration)
		if input.Type != "excel" || oldErr != nil || newErr != nil || oldFile != newFile || !reflect.DeepEqual(oldSheets, newSheets) {
			writeJSON(w, http.StatusUnprocessableEntity, "Excel datasource file cannot be changed")
			return
		}
	} else {
		failure(w, ErrNotFound)
		return
	}
	if _, err := h.service.schema(r.Context(), p, binding); err != nil {
		failure(w, err)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	if err := h.service.bindingUnchanged(r.Context(), p, binding, revision, digest); err != nil {
		failure(w, err)
		return
	}
	okay(w, true)
}
