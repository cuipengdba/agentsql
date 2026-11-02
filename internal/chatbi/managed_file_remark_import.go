package chatbi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

func readManagedRemarkUpload(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxSemanticWorkbook+65536)
	reader, err := r.MultipartReader()
	if err != nil {
		return nil, errSemanticWorkbook
	}
	part, err := reader.NextPart()
	if err != nil || part.FormName() != "file" || !strings.HasSuffix(strings.ToLower(part.FileName()), ".xlsx") {
		return nil, errSemanticWorkbook
	}
	data, err := io.ReadAll(io.LimitReader(part, maxSemanticWorkbook+1))
	_ = part.Close()
	if err != nil || len(data) == 0 || len(data) > maxSemanticWorkbook {
		return nil, errSemanticWorkbook
	}
	if _, err := reader.NextPart(); err != io.EOF {
		return nil, errSemanticWorkbook
	}
	return data, nil
}

func (h *Handler) importManagedFileRemarks(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	datasource := ID(r.PathValue("id"))
	if !datasource.Valid() {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid datasource ID")
		return
	}
	data, err := readManagedRemarkUpload(w, r)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid XLSX upload")
		return
	}
	binding, revision, digest, err := h.service.bindingSnapshot(r.Context(), p, datasource)
	if err == nil && databaseBinding(binding) {
		h.importDatabaseRemarks(w, r, p, token, binding, revision, digest, data)
		return
	}
	if err != nil || binding.Type != "excel" || binding.Dialect != "pg" ||
		!managedFileOperationPattern.MatchString(binding.ManagedFileID) {
		failure(w, ErrNotFound)
		return
	}
	sheets, err := parseXLSXSheetsLimited(data, 9)
	if err != nil || len(sheets) == 0 || sheets[0].Name != "数据表列表" || len(sheets[0].Fields) != 3 ||
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
	tables, valid := visibleBrowserTables(datasource, columns)
	if !valid || len(tables) > 8 {
		failure(w, ErrGateway)
		return
	}
	_, importedSheets, err := decodeSQLBotExcelConfig(binding.Configuration)
	if err != nil {
		failure(w, ErrUnavailable)
		return
	}
	imported := make(map[string]bool, len(importedSheets))
	for _, sheet := range importedSheets {
		imported[sheet.TableName] = true
	}
	visible := make(map[string]map[string]bool, len(tables))
	for _, table := range tables {
		if table.schema != "agentsql_files" || !imported[table.name] {
			failure(w, ErrGateway)
			return
		}
		fields := make(map[string]bool, len(table.columns))
		for _, column := range table.columns {
			fields[column.Name] = true
		}
		visible[table.name] = fields
	}
	tableChanges := make(map[string]string)
	fieldChanges := make(map[string]string)
	sheetTable := make(map[string]string)
	seenTable := make(map[string]bool)
	for _, row := range sheets[0].Rows {
		if len(row) != 3 || row[0] == "" || sheetTable[row[0]] != "" {
			writeJSON(w, http.StatusUnprocessableEntity, "Invalid table remark row")
			return
		}
		sheetTable[row[0]] = row[1]
		if !imported[row[1]] || visible[row[1]] == nil {
			continue // The original import leaves unmatched names unchanged.
		}
		if seenTable[row[1]] || !validManagedFileComment(row[2]) {
			writeJSON(w, http.StatusUnprocessableEntity, "Invalid table remark row")
			return
		}
		seenTable[row[1]] = true
		tableChanges[row[1]] = row[2]
	}
	if len(tableChanges) > 8 {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid table remark list")
		return
	}
	for _, sheet := range sheets[1:] {
		table := sheetTable[sheet.Name]
		if visible[table] == nil {
			continue // Unmatched table sheets cannot affect this datasource.
		}
		if len(sheet.Fields) != 2 || sheet.Fields[0].Name != "字段名" ||
			sheet.Fields[1].Name != "字段备注" {
			writeJSON(w, http.StatusUnprocessableEntity, "Invalid field remark sheet")
			return
		}
		seenField := make(map[string]bool)
		for _, row := range sheet.Rows {
			if len(row) != 2 {
				writeJSON(w, http.StatusUnprocessableEntity, "Invalid field remark row")
				return
			}
			if !visible[table][row[0]] {
				continue // Unknown fields retain their previous remarks.
			}
			if seenField[row[0]] || !validManagedFileComment(row[1]) {
				writeJSON(w, http.StatusUnprocessableEntity, "Invalid field remark row")
				return
			}
			seenField[row[0]] = true
			fieldChanges[managedFileFieldCommentKey(table, row[0])] = row[1]
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
	if err := h.service.Store.updateManagedFileRemarksBulk(r.Context(), p, binding, tableChanges, fieldChanges); err != nil {
		failure(w, err)
		return
	}
	okay(w, true)
}

func (s *Store) updateManagedFileRemarksBulk(ctx context.Context, p Principal, prior Binding, tableChanges, fieldChanges map[string]string) error {
	if s == nil || ctx == nil || prior.Workspace != p.Workspace || prior.User != p.User.ID ||
		!prior.Datasource.Valid() || prior.Type != "excel" || prior.Dialect != "pg" ||
		!managedFileOperationPattern.MatchString(prior.ManagedFileID) ||
		len(tableChanges) > 8 || len(fieldChanges) > 256 {
		return ErrNotFound
	}
	_, sheets, err := decodeSQLBotExcelConfig(prior.Configuration)
	if err != nil {
		return ErrUnavailable
	}
	imported := make(map[string]bool, len(sheets))
	for _, sheet := range sheets {
		imported[sheet.TableName] = true
	}
	tableComments, err := managedFileTableComments(prior)
	if err != nil {
		return err
	}
	if tableComments == nil {
		tableComments = make(map[string]string)
	}
	fieldComments, err := managedFileFieldComments(prior)
	if err != nil {
		return err
	}
	if fieldComments == nil {
		fieldComments = make(map[string]string)
	}
	for table, comment := range tableChanges {
		if !imported[table] || !managedFileNamePattern.MatchString(table) || !validManagedFileComment(comment) {
			return ErrNotFound
		}
		if comment == "" {
			delete(tableComments, table)
		} else {
			tableComments[table] = comment
		}
	}
	for key, comment := range fieldChanges {
		parts := strings.Split(key, "\x00")
		if len(parts) != 2 || !imported[parts[0]] || parts[1] == "" || len(parts[1]) > 256 ||
			!validManagedFileComment(comment) {
			return ErrNotFound
		}
		if comment == "" {
			delete(fieldComments, key)
		} else {
			fieldComments[key] = comment
		}
	}
	if len(tableComments) > 8 || len(fieldComments) > 256 {
		return ErrUnavailable
	}
	encode := func(comments map[string]string) (string, error) {
		if len(comments) == 0 {
			return "", nil
		}
		body, err := json.Marshal(comments)
		if err != nil {
			return "", ErrUnavailable
		}
		return string(body), nil
	}
	encodedTable, err := encode(tableComments)
	if err != nil {
		return err
	}
	encodedField, err := encode(fieldComments)
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
	audit, err := s.seal(auditKey, map[string]any{"action": "file-import.remarks-bulk", "actor": p.User.ID,
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
