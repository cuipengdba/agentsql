package chatbi

import (
	"net/http"
	"strconv"
)

// The original datasource detail page reads this object before listing tables.
// Database connection configuration is deliberately absent: browser-visible
// SQLBot AES is not a suitable place for server-owned gateway credentials.
func (h *Handler) getDatasource(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	id := ID(r.PathValue("id"))
	numeric, err := strconv.ParseInt(string(id), 10, 64)
	if !id.Valid() || err != nil || numeric > maxSQLBotBrowserID {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid datasource ID")
		return
	}
	binding, revision, digest, err := h.service.bindingSnapshot(r.Context(), p, id)
	if err != nil {
		failure(w, err)
		return
	}
	if binding.Type == "excel" {
		payload, err := legacyExcelDatasource(binding)
		if err != nil {
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
		okay(w, payload)
		return
	}
	kind := binding.Type
	if kind == "" {
		kind = binding.Dialect
	}
	if (kind != "pg" && kind != "mysql") || kind != binding.Dialect {
		failure(w, ErrNotFound)
		return
	}
	columns, err := h.service.schema(r.Context(), p, binding)
	if err != nil {
		failure(w, err)
		return
	}
	tables, valid := visibleBrowserTables(id, columns)
	if !valid {
		failure(w, ErrGateway)
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
	workspace, err := strconv.ParseInt(string(binding.Workspace), 10, 64)
	if err != nil {
		failure(w, ErrUnavailable)
		return
	}
	creator, err := strconv.ParseInt(string(binding.User), 10, 64)
	if err != nil {
		failure(w, ErrUnavailable)
		return
	}
	typeName := "PostgreSQL"
	if kind == "mysql" {
		typeName = "MySQL"
	}
	configuration, err := databaseDisplayConfiguration()
	if err != nil {
		failure(w, err)
		return
	}
	okay(w, map[string]any{"id": numeric, "oid": workspace, "create_by": creator,
		"name": binding.Name, "description": binding.Description, "type": kind,
		"type_name": typeName, "configuration": configuration, "status": "Success",
		"num":         strconv.Itoa(len(tables)) + "/" + strconv.Itoa(len(tables)),
		"create_time": binding.Created, "embedding": nil, "table_relation": nil})
}
