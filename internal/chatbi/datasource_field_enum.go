package chatbi

import (
	"net/http"
	"reflect"
	"strconv"
	"strings"
)

// SQLBot's legacy fieldEnum endpoint was hidden from its OpenAPI schema. Keep
// its path and value-array response, but resolve the field exclusively from
// the caller's current gateway-visible schema before an audited, bounded read.
func (h *Handler) datasourceFieldEnum(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	datasource := ID(r.PathValue("datasourceID"))
	fieldRaw := r.PathValue("fieldID")
	fieldID, err := strconv.ParseInt(fieldRaw, 10, 64)
	if !datasource.Valid() || fieldID < 1 || fieldID > maxSQLBotBrowserID || strconv.FormatInt(fieldID, 10) != fieldRaw {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid datasource or field ID")
		return
	}
	binding, revision, digest, err := h.service.bindingSnapshot(r.Context(), p, datasource)
	if err != nil {
		failure(w, err)
		return
	}
	columns, err := h.service.semanticSchema(r.Context(), p, binding)
	if err != nil {
		failure(w, err)
		return
	}
	tables, valid := visibleBrowserTables(datasource, columns)
	if !valid {
		failure(w, ErrGateway)
		return
	}
	var found *Column
	for _, table := range tables {
		for _, column := range table.columns {
			if browserFieldID(table.id, column.Name) != fieldID {
				continue
			}
			if found != nil {
				failure(w, ErrGateway)
				return
			}
			candidate := column
			found = &candidate
		}
	}
	if found == nil {
		failure(w, ErrNotFound)
		return
	}
	quote := func(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }
	if binding.Dialect == "mysql" {
		quote = func(name string) string { return "`" + strings.ReplaceAll(name, "`", "``") + "`" }
	} else if binding.Dialect != "pg" {
		failure(w, ErrGateway)
		return
	}
	selectClause := "SELECT DISTINCT "
	if binding.Dialect == "mysql" {
		// The MySQL closed column binder accepts a direct bounded projection,
		// but cannot prove the DISTINCT result shape. Deduplicate its bounded
		// result in memory after the audited gateway query.
		selectClause = "SELECT "
	}
	sql := selectClause + quote(found.Name) + " FROM " + quote(found.Schema) + "." + quote(found.Table) + " LIMIT 100"
	result, err := h.service.query(r.Context(), p, binding, sql)
	if err != nil {
		failure(w, err)
		return
	}
	if result.AuditID <= 0 || len(result.Fields) != 1 || result.Fields[0] != found.Name || len(result.Data) > 100 || result.Truncated {
		failure(w, ErrGateway)
		return
	}
	values := make([]any, 0, len(result.Data))
	for _, row := range result.Data {
		value, ok := row[found.Name]
		if !ok {
			failure(w, ErrGateway)
			return
		}
		duplicate := false
		for _, existing := range values {
			if reflect.DeepEqual(existing, value) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			values = append(values, value)
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
	okay(w, values)
}
