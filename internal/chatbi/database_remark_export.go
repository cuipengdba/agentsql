package chatbi

import (
	"net/http"
	"strconv"
)

// Database export uses only the current gateway-visible schema and remarks in
// this administrator's encrypted binding. It never queries the database with
// browser-supplied credentials or includes connection metadata in the file.
func (h *Handler) exportDatabaseRemarks(w http.ResponseWriter, r *http.Request, p Principal,
	token string, binding Binding, revision int64, digest [32]byte) {
	columns, err := h.service.schema(r.Context(), p, binding)
	if err != nil {
		failure(w, err)
		return
	}
	tables, valid := visibleBrowserTables(binding.Datasource, columns)
	if !valid || len(tables) > 256 {
		failure(w, ErrGateway)
		return
	}
	if len(tables) == 0 {
		writeJSON(w, http.StatusBadRequest, "No tables")
		return
	}
	tableComments, err := databaseRemarks(binding, false)
	if err != nil {
		failure(w, err)
		return
	}
	fieldComments, err := databaseRemarks(binding, true)
	if err != nil {
		failure(w, err)
		return
	}
	sheets := []managedRemarkSheet{{name: "数据表列表", rows: [][]string{{"Sheet名称", "表名", "表备注"}}}}
	for index, table := range tables {
		if len(table.columns) == 0 || len(table.columns) > 256 {
			failure(w, ErrGateway)
			return
		}
		sheetName := "Sheet" + strconv.Itoa(index)
		sheets[0].rows = append(sheets[0].rows,
			[]string{sheetName, table.name, tableComments[databaseTableCommentKey(table.schema, table.name)]})
		rows := [][]string{{"字段名", "字段备注"}}
		for _, column := range table.columns {
			rows = append(rows, []string{column.Name,
				fieldComments[databaseFieldCommentKey(table.schema, table.name, column.Name)]})
		}
		sheets = append(sheets, managedRemarkSheet{name: sheetName, rows: rows})
	}
	data, err := managedRemarkWorkbook(sheets)
	if err != nil {
		failure(w, ErrUnavailable)
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
	if err := h.service.auditSemanticExport(r.Context(), p, "datasource.database-remark", len(tables)); err != nil {
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
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", `attachment; filename="agentsql-datasource-`+string(binding.Datasource)+`-remarks.xlsx"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
