package chatbi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func (h *Handler) exportDatabaseRemarks(w http.ResponseWriter, r *http.Request, p Principal, token string,
	binding Binding, revision int64, digest [32]byte) {
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
		if !validDatabaseRemarkName(table.schema) || !validDatabaseRemarkName(table.name) || len(table.columns) > 256 {
			failure(w, ErrGateway)
			return
		}
		sheetName := fmt.Sprintf("Sheet%d", index)
		sheets[0].rows = append(sheets[0].rows, []string{sheetName, table.name,
			tableComments[databaseTableRemarkKey(table.schema, table.name)]})
		fieldRows := [][]string{{"字段名", "字段备注"}}
		for _, column := range table.columns {
			if !validDatabaseRemarkName(column.Name) {
				failure(w, ErrGateway)
				return
			}
			fieldRows = append(fieldRows, []string{column.Name,
				fieldComments[databaseFieldRemarkKey(table.schema, table.name, column.Name)]})
		}
		sheets = append(sheets, managedRemarkSheet{name: sheetName, rows: fieldRows})
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
	if err := h.service.auditSemanticExport(r.Context(), p, "datasource.remark", len(tables)); err != nil {
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

func (h *Handler) importDatabaseRemarks(w http.ResponseWriter, r *http.Request, p Principal, token string,
	binding Binding, revision int64, digest [32]byte, data []byte) {
	sheets, err := parseXLSXSheetsLimited(data, 257)
	if err != nil || len(sheets) < 2 || sheets[0].Name != "数据表列表" || len(sheets[0].Fields) != 3 ||
		sheets[0].Fields[0].Name != "Sheet名称" || sheets[0].Fields[1].Name != "表名" ||
		sheets[0].Fields[2].Name != "表备注" {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid remark workbook")
		return
	}
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
	visible := make(map[string]visibleBrowserTable, len(tables))
	for _, table := range tables {
		if !validDatabaseRemarkName(table.schema) || !validDatabaseRemarkName(table.name) || len(table.columns) > 256 {
			failure(w, ErrGateway)
			return
		}
		visible[table.name] = table
	}
	sheetTable := make(map[string]string)
	seenTable := make(map[string]bool)
	tableChanges := make(map[string]string)
	fieldChanges := make(map[string]string)
	for _, row := range sheets[0].Rows {
		if len(row) != 3 || row[0] == "" || sheetTable[row[0]] != "" {
			writeJSON(w, http.StatusUnprocessableEntity, "Invalid table remark row")
			return
		}
		sheetTable[row[0]] = row[1]
		table, exists := visible[row[1]]
		if !exists {
			continue // Unknown tables cannot change this datasource.
		}
		if seenTable[table.name] || !validManagedFileComment(row[2]) {
			writeJSON(w, http.StatusUnprocessableEntity, "Invalid table remark row")
			return
		}
		seenTable[table.name] = true
		tableChanges[databaseTableRemarkKey(table.schema, table.name)] = row[2]
	}
	seenSheet := make(map[string]bool)
	for _, sheet := range sheets[1:] {
		tableName := sheetTable[sheet.Name]
		table, exists := visible[tableName]
		if !exists {
			continue
		}
		if seenSheet[sheet.Name] {
			writeJSON(w, http.StatusUnprocessableEntity, "Duplicate remark sheet")
			return
		}
		seenSheet[sheet.Name] = true
		if len(sheet.Fields) != 2 || sheet.Fields[0].Name != "字段名" ||
			sheet.Fields[1].Name != "字段备注" {
			writeJSON(w, http.StatusUnprocessableEntity, "Invalid field remark sheet")
			return
		}
		fields := make(map[string]bool, len(table.columns))
		for _, column := range table.columns {
			fields[column.Name] = true
		}
		seenField := make(map[string]bool)
		for _, row := range sheet.Rows {
			if len(row) != 2 {
				writeJSON(w, http.StatusUnprocessableEntity, "Invalid field remark row")
				return
			}
			if !fields[row[0]] {
				continue // Unknown fields retain their previous remarks.
			}
			if seenField[row[0]] || !validManagedFileComment(row[1]) {
				writeJSON(w, http.StatusUnprocessableEntity, "Invalid field remark row")
				return
			}
			seenField[row[0]] = true
			fieldChanges[databaseFieldRemarkKey(table.schema, table.name, row[0])] = row[1]
		}
	}
	if len(tableChanges) > 256 || len(fieldChanges) > 4096 {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid remark list")
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
	if err := h.service.Store.updateDatabaseRemarksBulk(r.Context(), p, binding, tableChanges, fieldChanges); err != nil {
		failure(w, err)
		return
	}
	okay(w, true)
}

func (s *Store) updateDatabaseRemarksBulk(ctx context.Context, p Principal, prior Binding,
	tableChanges, fieldChanges map[string]string) error {
	if s == nil || ctx == nil || !databaseBinding(prior) || prior.Workspace != p.Workspace ||
		prior.User != p.User.ID || !prior.Datasource.Valid() || len(tableChanges) > 256 || len(fieldChanges) > 4096 {
		return ErrNotFound
	}
	tables, err := databaseRemarks(prior, false)
	if err != nil {
		return err
	}
	fields, err := databaseRemarks(prior, true)
	if err != nil {
		return err
	}
	if tables == nil {
		tables = make(map[string]string)
	}
	if fields == nil {
		fields = make(map[string]string)
	}
	apply := func(target, changes map[string]string, parts int) error {
		for key, comment := range changes {
			components := strings.Split(key, "\x00")
			if len(components) != parts || !validManagedFileComment(comment) {
				return ErrNotFound
			}
			for _, component := range components {
				if !validDatabaseRemarkName(component) {
					return ErrNotFound
				}
			}
			if comment == "" {
				delete(target, key)
			} else {
				target[key] = comment
			}
		}
		return nil
	}
	if err := apply(tables, tableChanges, 2); err != nil {
		return err
	}
	if err := apply(fields, fieldChanges, 3); err != nil {
		return err
	}
	if len(tables) > 256 || len(fields) > 4096 {
		return ErrUnavailable
	}
	encode := func(comments map[string]string) (string, error) {
		if len(comments) == 0 {
			return "", nil
		}
		body, err := json.Marshal(comments)
		if err != nil || len(body) > 1<<20 {
			return "", ErrUnavailable
		}
		return string(body), nil
	}
	encodedTable, err := encode(tables)
	if err != nil {
		return err
	}
	encodedField, err := encode(fields)
	if err != nil {
		return err
	}
	object := bindingKey(p, prior.Datasource)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	defer tx.Rollback()
	if err := s.verifyManagedWorkspaceActor(ctx, tx, p); err != nil {
		return err
	}
	var oldBody []byte
	var revision int64
	err = tx.QueryRowContext(ctx, `SELECT revision,body FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=?`,
		object.Kind, object.Scope, object.Owner, object.ID).Scan(&revision, &oldBody)
	if controlNoRows(err) {
		return ErrNotFound
	}
	if err != nil {
		return ErrUnavailable
	}
	var current Binding
	if s.decode(object, oldBody, &current) != nil || current != prior {
		return ErrConflict
	}
	changed := current.TableComments != encodedTable || current.FieldComments != encodedField
	auditID, err := newID()
	if err != nil {
		return err
	}
	auditKey := objectKey{"management-audit", "system", "system", string(auditID)}
	audit, err := s.seal(auditKey, map[string]any{"action": "database.remarks-bulk", "actor": p.User.ID,
		"workspace": p.Workspace, "datasource": prior.Datasource, "tables": len(tableChanges),
		"fields": len(fieldChanges), "time": time.Now().UTC()})
	if err != nil {
		return err
	}
	if changed {
		current.TableComments = encodedTable
		current.FieldComments = encodedField
		sealed, err := s.seal(object, current)
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE chatbi_objects SET revision=revision+1,body=? WHERE kind=? AND scope=? AND owner=? AND id=? AND revision=?`,
			sealed, object.Kind, object.Scope, object.Owner, object.ID, revision)
		if err != nil {
			return ErrUnavailable
		}
		if affected, err := result.RowsAffected(); err != nil || affected != 1 {
			return ErrConflict
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO chatbi_objects(kind,scope,owner,id,revision,body) VALUES(?,?,?,?,1,?)`,
		auditKey.Kind, auditKey.Scope, auditKey.Owner, auditKey.ID, audit); err != nil {
		return ErrUnavailable
	}
	if tx.Commit() != nil {
		return ErrUnavailable
	}
	return nil
}
