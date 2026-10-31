package chatbi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

func (h *Handler) importDatabaseRemarks(w http.ResponseWriter, r *http.Request, p Principal,
	token string, binding Binding, revision int64, digest [32]byte, sheets []FileSheet) {
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
		if len(table.columns) == 0 || len(table.columns) > 256 {
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
		table, found := visible[row[1]]
		if !found {
			continue // Unmatched or revoked tables cannot change saved remarks.
		}
		if seenTable[row[1]] || !validManagedFileComment(row[2]) {
			writeJSON(w, http.StatusUnprocessableEntity, "Invalid table remark row")
			return
		}
		seenTable[row[1]] = true
		tableChanges[databaseTableCommentKey(table.schema, table.name)] = row[2]
	}
	for _, sheet := range sheets[1:] {
		table, found := visible[sheetTable[sheet.Name]]
		if !found {
			continue
		}
		if len(sheet.Fields) != 2 || sheet.Fields[0].Name != "字段名" || sheet.Fields[1].Name != "字段备注" {
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
			fieldChanges[databaseFieldCommentKey(table.schema, table.name, row[0])] = row[1]
		}
	}
	if len(tableChanges) > 256 || len(fieldChanges) > 4096 {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid database remark list")
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
	if s == nil || ctx == nil || prior.Workspace != p.Workspace || prior.User != p.User.ID ||
		!prior.Datasource.Valid() || !databaseBinding(prior) ||
		len(tableChanges) > 256 || len(fieldChanges) > 4096 {
		return ErrNotFound
	}
	tableComments, err := databaseRemarks(prior, false)
	if err != nil {
		return err
	}
	fieldComments, err := databaseRemarks(prior, true)
	if err != nil {
		return err
	}
	if tableComments == nil {
		tableComments = make(map[string]string)
	}
	if fieldComments == nil {
		fieldComments = make(map[string]string)
	}
	for key, comment := range tableChanges {
		parts := strings.Split(key, "\x00")
		if len(parts) != 2 || !databaseRemarkName(parts[0]) || !databaseRemarkName(parts[1]) ||
			!validManagedFileComment(comment) {
			return ErrNotFound
		}
		if comment == "" {
			delete(tableComments, key)
		} else {
			tableComments[key] = comment
		}
	}
	for key, comment := range fieldChanges {
		parts := strings.Split(key, "\x00")
		if len(parts) != 3 || !databaseRemarkName(parts[0]) || !databaseRemarkName(parts[1]) ||
			!databaseRemarkName(parts[2]) || !validManagedFileComment(comment) {
			return ErrNotFound
		}
		if comment == "" {
			delete(fieldComments, key)
		} else {
			fieldComments[key] = comment
		}
	}
	if len(tableComments) > 256 || len(fieldComments) > 4096 {
		return ErrUnavailable
	}
	encode := func(comments map[string]string) (string, error) {
		if len(comments) == 0 {
			return "", nil
		}
		body, err := json.Marshal(comments)
		if err != nil || len(body) > 2<<20 {
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
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT revision,body FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=?`,
		object.Kind, object.Scope, object.Owner, object.ID).Scan(&version, &oldBody); controlNoRows(err) {
		return ErrNotFound
	} else if err != nil {
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
	audit, err := s.seal(auditKey, map[string]any{"action": "database.remarks-bulk",
		"actor": p.User.ID, "workspace": p.Workspace, "datasource": prior.Datasource,
		"tables": len(tableChanges), "fields": len(fieldChanges), "time": time.Now().UTC()})
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
		result, err := tx.ExecContext(ctx,
			`UPDATE chatbi_objects SET revision=revision+1,body=? WHERE kind=? AND scope=? AND owner=? AND id=? AND revision=?`,
			sealed, object.Kind, object.Scope, object.Owner, object.ID, version)
		if err != nil {
			return ErrUnavailable
		}
		if count, err := result.RowsAffected(); err != nil || count != 1 {
			return ErrConflict
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO chatbi_objects(kind,scope,owner,id,revision,body) VALUES(?,?,?,?,1,?)`,
		auditKey.Kind, auditKey.Scope, auditKey.Owner, auditKey.ID, audit); err != nil {
		return ErrUnavailable
	}
	if tx.Commit() != nil {
		return ErrUnavailable
	}
	return nil
}
