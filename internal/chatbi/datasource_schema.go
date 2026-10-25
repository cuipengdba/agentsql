package chatbi

import (
	"net/http"
	"sort"
	"strings"
)

// These SQLBot-compatible metadata reads expose only the gateway's current
// visible schema. They never inspect a database directly or return credentials.
func (h *Handler) datasourceSchema(w http.ResponseWriter, r *http.Request, p Principal, token string) ([]Column, bool) {
	id := ID(r.PathValue("id"))
	if !id.Valid() {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid datasource ID")
		return nil, false
	}
	binding, err := h.service.binding(r.Context(), p, id)
	if err != nil {
		failure(w, err)
		return nil, false
	}
	columns, err := h.service.Gateway.Schema(r.Context(), p, binding)
	if err != nil {
		failure(w, err)
		return nil, false
	}
	// A revocation during the remote schema read must not release metadata.
	if err := h.service.live(r.Context(), p); err != nil {
		failure(w, err)
		return nil, false
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return nil, false
	}
	current, err := h.service.binding(r.Context(), p, id)
	if err != nil {
		failure(w, err)
		return nil, false
	}
	if current != binding {
		failure(w, ErrGateway)
		return nil, false
	}
	return columns, true
}

func (h *Handler) datasourceTables(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	columns, ok := h.datasourceSchema(w, r, p, token)
	if !ok {
		return
	}
	tables := make(map[string]struct{}, len(columns))
	for _, column := range columns {
		if column.Table == "" {
			failure(w, ErrGateway)
			return
		}
		tables[column.Table] = struct{}{}
	}
	names := make([]string, 0, len(tables))
	for name := range tables {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]map[string]any, 0, len(names))
	for _, name := range names {
		result = append(result, map[string]any{"tableName": name, "tableComment": ""})
	}
	okay(w, result)
}

func (h *Handler) datasourceCheck(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if _, ok := h.datasourceSchema(w, r, p, token); !ok {
		return
	}
	okay(w, true)
}

func (h *Handler) datasourceFields(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	table := r.PathValue("table")
	if table == "" || len(table) > 256 || strings.ContainsAny(table, "/\\\x00") {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid table name")
		return
	}
	columns, ok := h.datasourceSchema(w, r, p, token)
	if !ok {
		return
	}
	result := make([]map[string]any, 0)
	for _, column := range columns {
		if column.Table == table {
			result = append(result, map[string]any{"fieldName": column.Name, "fieldType": column.DataType, "fieldComment": ""})
		}
	}
	if len(result) == 0 {
		failure(w, ErrNotFound)
		return
	}
	sort.Slice(result, func(i, j int) bool { return result[i]["fieldName"].(string) < result[j]["fieldName"].(string) })
	okay(w, result)
}

// datasourcePreview uses the current visible schema to construct a fixed,
// bounded SELECT. The client-provided table ID is only a legacy presence
// check; neither it nor any submitted field or SQL text grants access.
func (h *Handler) datasourcePreview(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	var input struct {
		Table struct {
			ID        ID     `json:"id"`
			TableName string `json:"table_name"`
		} `json:"table"`
	}
	if !readBody(w, r, &input) {
		return
	}
	if !input.Table.ID.Valid() || input.Table.TableName == "" || len(input.Table.TableName) > 256 {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid table")
		return
	}
	columns, ok := h.datasourceSchema(w, r, p, token)
	if !ok {
		return
	}
	binding, err := h.service.binding(r.Context(), p, ID(r.PathValue("id")))
	if err != nil {
		failure(w, err)
		return
	}
	var schema string
	names := make([]string, 0, len(columns))
	seen := map[string]bool{}
	for _, column := range columns {
		if column.Table != input.Table.TableName {
			continue
		}
		if column.Schema == "" || column.Name == "" || seen[column.Name] || schema != "" && schema != column.Schema || len(column.Name) > 256 {
			failure(w, ErrGateway)
			return
		}
		schema = column.Schema
		seen[column.Name] = true
		names = append(names, column.Name)
	}
	if len(names) == 0 || len(names) > 128 {
		failure(w, ErrNotFound)
		return
	}
	quote := func(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }
	from := quote(schema) + "." + quote(input.Table.TableName)
	if binding.Dialect == "mysql" {
		quote = func(name string) string { return "`" + strings.ReplaceAll(name, "`", "``") + "`" }
		from = quote(input.Table.TableName)
	} else if binding.Dialect != "pg" {
		failure(w, ErrGateway)
		return
	}
	selected := make([]string, len(names))
	for index, name := range names {
		selected[index] = quote(name)
	}
	sql := "SELECT " + strings.Join(selected, ", ") + " FROM " + from + " LIMIT 100"
	result, err := h.service.Gateway.Query(r.Context(), p, binding, sql)
	if err != nil {
		failure(w, err)
		return
	}
	if result.AuditID <= 0 || len(result.Fields) == 0 || len(result.Data) > 100 {
		failure(w, ErrGateway)
		return
	}
	// A revoked token, user, or binding cannot receive rows after remote work.
	if err := h.service.live(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	current, err := h.service.binding(r.Context(), p, ID(r.PathValue("id")))
	if err != nil || current != binding {
		failure(w, ErrGateway)
		return
	}
	okay(w, map[string]any{"fields": result.Fields, "data": result.Data, "sql": sql})
}
