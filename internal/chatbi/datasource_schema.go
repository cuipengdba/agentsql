package chatbi

import (
	"encoding/base64"
	"hash/fnv"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

const maxSQLBotBrowserID = (1 << 53) - 1

type visibleBrowserTable struct {
	name, schema string
	id           int64
	columns      []Column
}

// SQLBot's datasource detail page uses numeric table IDs. Derive them from
// the current datasource and qualified table name, so a changed or revoked
// schema cannot make an old ID select another table by list position.
func browserTableID(datasource ID, schema, table string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(datasource))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(schema))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(table))
	return int64(h.Sum64()%maxSQLBotBrowserID) + 1
}

func browserFieldID(tableID int64, name string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(strconv.FormatInt(tableID, 10)))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(name))
	return int64(h.Sum64()%maxSQLBotBrowserID) + 1
}

func visibleBrowserTables(datasource ID, columns []Column) ([]visibleBrowserTable, bool) {
	byName := make(map[string]*visibleBrowserTable)
	byID := make(map[int64]string)
	for _, column := range columns {
		if column.Schema == "" || column.Table == "" || column.Name == "" || len(column.Name) > 256 {
			return nil, false
		}
		qualified := column.Schema + "\x00" + column.Table
		table, exists := byName[column.Table]
		if exists && table.schema != column.Schema { // Preview names cannot disambiguate schemas.
			return nil, false
		}
		if !exists {
			id := browserTableID(datasource, column.Schema, column.Table)
			if other, collision := byID[id]; collision && other != qualified {
				return nil, false
			}
			byID[id] = qualified
			table = &visibleBrowserTable{name: column.Table, schema: column.Schema, id: id}
			byName[column.Table] = table
		}
		for _, prior := range table.columns {
			if prior.Name == column.Name {
				return nil, false
			}
		}
		table.columns = append(table.columns, column)
	}
	tables := make([]visibleBrowserTable, 0, len(byName))
	for _, table := range byName {
		tables = append(tables, *table)
	}
	sort.Slice(tables, func(i, j int) bool { return tables[i].name < tables[j].name })
	return tables, true
}

func (h *Handler) datasourceTableList(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	datasource := ID(r.PathValue("id"))
	if !datasource.Valid() {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid datasource ID")
		return
	}
	binding, revision, digest, err := h.service.bindingSnapshot(r.Context(), p, datasource)
	if err != nil {
		failure(w, err)
		return
	}
	columns, err := h.service.schema(r.Context(), p, binding)
	if err != nil {
		failure(w, err)
		return
	}
	comments, err := managedFileTableComments(binding)
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
	numeric, err := strconv.ParseInt(string(datasource), 10, 64)
	tables, valid := visibleBrowserTables(datasource, columns)
	if err != nil || numeric > maxSQLBotBrowserID || !valid {
		failure(w, ErrGateway)
		return
	}
	result := make([]map[string]any, 0, len(tables))
	for _, table := range tables {
		result = append(result, map[string]any{"id": table.id, "table_name": table.name,
			"table_comment": "", "custom_comment": comments[table.name], "embedding": nil,
			"ds_id": numeric, "checked": true})
	}
	okay(w, result)
}

func (h *Handler) datasourceFieldList(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	tableID, err := strconv.ParseInt(r.PathValue("tableID"), 10, 64)
	if err != nil || tableID < 1 || tableID > maxSQLBotBrowserID || strconv.FormatInt(tableID, 10) != r.PathValue("tableID") {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid table ID")
		return
	}
	var input struct {
		FieldName string `json:"fieldName"`
	}
	if !readBody(w, r, &input) {
		return
	}
	if len(input.FieldName) > 256 {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid field name")
		return
	}
	datasource := ID(r.PathValue("id"))
	if !datasource.Valid() {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid datasource ID")
		return
	}
	binding, revision, digest, err := h.service.bindingSnapshot(r.Context(), p, datasource)
	if err != nil {
		failure(w, err)
		return
	}
	columns, err := h.service.schema(r.Context(), p, binding)
	if err != nil {
		failure(w, err)
		return
	}
	comments, err := managedFileFieldComments(binding)
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
	numeric, err := strconv.ParseInt(string(datasource), 10, 64)
	tables, valid := visibleBrowserTables(datasource, columns)
	if err != nil || numeric > maxSQLBotBrowserID || !valid {
		failure(w, ErrGateway)
		return
	}
	for _, table := range tables {
		if table.id != tableID {
			continue
		}
		result := make([]map[string]any, 0, len(table.columns))
		ids := make(map[int64]bool)
		for index, column := range table.columns {
			id := browserFieldID(tableID, column.Name)
			if ids[id] {
				failure(w, ErrGateway)
				return
			}
			ids[id] = true
			if !strings.Contains(strings.ToLower(column.Name), strings.ToLower(input.FieldName)) {
				continue
			}
			result = append(result, map[string]any{"id": id, "field_name": column.Name,
				"field_type": column.DataType, "field_index": index,
				"field_comment": nil, "custom_comment": comments[managedFileFieldCommentKey(table.name, column.Name)],
				"ds_id": numeric, "table_id": tableID, "checked": true})
		}
		okay(w, result)
		return
	}
	failure(w, ErrNotFound)
}

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
	columns, err := h.service.schema(r.Context(), p, binding)
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
	result, err := h.service.query(r.Context(), p, binding, sql)
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
	okay(w, map[string]any{"fields": result.Fields, "data": result.Data,
		"sql": base64.StdEncoding.EncodeToString([]byte(sql))})
}
