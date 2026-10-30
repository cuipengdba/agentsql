package chatbi

import (
	"net/http"
	"reflect"
	"sort"
)

func validBoundDatasourceEnvelope(binding Binding, kind, configuration string) bool {
	if databaseBinding(binding) {
		return kind == binding.Dialect && validDatabaseMetadataConfiguration(configuration)
	}
	if binding.Type != "excel" || binding.Dialect != "pg" ||
		!managedFileOperationPattern.MatchString(binding.ManagedFileID) || kind != "excel" {
		return false
	}
	oldFile, oldSheets, oldErr := decodeSQLBotExcelConfig(binding.Configuration)
	newFile, newSheets, newErr := decodeSQLBotExcelConfig(configuration)
	return oldErr == nil && newErr == nil && oldFile == newFile && reflect.DeepEqual(oldSheets, newSheets)
}

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
	if !validBoundDatasourceEnvelope(binding, input.Type, input.Configuration) {
		writeJSON(w, http.StatusUnprocessableEntity, "Datasource connection settings cannot be changed")
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

// Schema discovery for the original edit form is read-only and bound to the
// current gateway configuration. It does not create a new database connection.
func (h *Handler) schemasByBoundDatasource(w http.ResponseWriter, r *http.Request, p Principal, token string) {
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
	if !databaseBinding(binding) || !validBoundDatasourceEnvelope(binding, input.Type, input.Configuration) {
		writeJSON(w, http.StatusUnprocessableEntity, "Database connection settings cannot be changed")
		return
	}
	// Use the gateway's authorized schema before the local table-selection
	// filter, because a user may need to rediscover a deselected table.
	columns, err := h.service.gatewaySchema(r.Context(), p, binding)
	if err != nil {
		failure(w, err)
		return
	}
	seen := make(map[string]bool)
	for _, column := range columns {
		if !databaseRemarkName(column.Schema) {
			failure(w, ErrGateway)
			return
		}
		seen[column.Schema] = true
		if len(seen) > 256 {
			failure(w, ErrGateway)
			return
		}
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	if err := h.service.bindingUnchanged(r.Context(), p, binding, revision, digest); err != nil {
		failure(w, err)
		return
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	okay(w, names)
}
