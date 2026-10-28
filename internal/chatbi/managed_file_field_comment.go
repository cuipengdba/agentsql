package chatbi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func managedFileFieldCommentKey(table, field string) string { return table + "\x00" + field }

// Field remarks are owner-scoped UI metadata. They never grant access or enter SQL context.
func managedFileFieldComments(binding Binding) (map[string]string, error) {
	if binding.FieldComments == "" {
		return nil, nil
	}
	if binding.Type != "excel" || binding.Dialect != "pg" ||
		!managedFileOperationPattern.MatchString(binding.ManagedFileID) {
		return nil, ErrUnavailable
	}
	var comments map[string]string
	if json.Unmarshal([]byte(binding.FieldComments), &comments) != nil || len(comments) > 256 {
		return nil, ErrUnavailable
	}
	for key, comment := range comments {
		parts := strings.Split(key, "\x00")
		if len(parts) != 2 || !managedFileNamePattern.MatchString(parts[0]) ||
			parts[1] == "" || len(parts[1]) > 256 || comment == "" || !validManagedFileComment(comment) {
			return nil, ErrUnavailable
		}
	}
	return comments, nil
}

func (h *Handler) editManagedFileFieldComment(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	var input struct {
		ID            ID              `json:"id"`
		DatasourceID  ID              `json:"ds_id"`
		TableID       ID              `json:"table_id"`
		FieldName     string          `json:"field_name"`
		FieldType     string          `json:"field_type"`
		FieldIndex    *int            `json:"field_index"`
		FieldComment  json.RawMessage `json:"field_comment"`
		CustomComment *string         `json:"custom_comment"`
		Checked       *bool           `json:"checked"`
	}
	if !readBody(w, r, &input) {
		return
	}
	fieldID, fieldErr := strconv.ParseInt(string(input.ID), 10, 64)
	tableID, tableErr := strconv.ParseInt(string(input.TableID), 10, 64)
	if fieldErr != nil || tableErr != nil || fieldID < 1 || tableID < 1 ||
		fieldID > maxSQLBotBrowserID || tableID > maxSQLBotBrowserID ||
		strconv.FormatInt(fieldID, 10) != string(input.ID) ||
		strconv.FormatInt(tableID, 10) != string(input.TableID) ||
		!input.DatasourceID.Valid() || input.FieldName == "" || len(input.FieldName) > 256 ||
		strings.ContainsRune(input.FieldName, '\x00') || input.FieldIndex == nil || *input.FieldIndex < 0 ||
		input.CustomComment == nil || !validManagedFileComment(*input.CustomComment) ||
		input.Checked == nil || !*input.Checked ||
		len(input.FieldComment) > 0 && string(input.FieldComment) != "null" {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid field remark")
		return
	}
	binding, revision, digest, err := h.service.bindingSnapshot(r.Context(), p, input.DatasourceID)
	if err != nil || binding.Type != "excel" || binding.Dialect != "pg" ||
		!managedFileOperationPattern.MatchString(binding.ManagedFileID) {
		failure(w, ErrNotFound)
		return
	}
	columns, err := h.service.schema(r.Context(), p, binding)
	if err != nil {
		failure(w, err)
		return
	}
	tables, valid := visibleBrowserTables(input.DatasourceID, columns)
	if !valid {
		failure(w, ErrGateway)
		return
	}
	tableName := ""
	for _, table := range tables {
		if table.id != tableID || table.schema != "agentsql_files" || !managedFileNamePattern.MatchString(table.name) {
			continue
		}
		for index, column := range table.columns {
			if index == *input.FieldIndex && column.Name == input.FieldName &&
				column.DataType == input.FieldType && browserFieldID(tableID, column.Name) == fieldID {
				tableName = table.name
				break
			}
		}
	}
	if tableName == "" {
		failure(w, ErrNotFound)
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
	if err := h.service.Store.updateManagedFileFieldComment(r.Context(), p, binding, tableName, input.FieldName, *input.CustomComment); err != nil {
		failure(w, err)
		return
	}
	okay(w, nil)
}

func (s *Store) updateManagedFileFieldComment(ctx context.Context, p Principal, prior Binding, tableName, fieldName, comment string) error {
	if s == nil || ctx == nil || prior.Workspace != p.Workspace || prior.User != p.User.ID ||
		!prior.Datasource.Valid() || prior.Type != "excel" || prior.Dialect != "pg" ||
		!managedFileOperationPattern.MatchString(prior.ManagedFileID) ||
		!managedFileNamePattern.MatchString(tableName) || fieldName == "" || len(fieldName) > 256 ||
		strings.ContainsRune(fieldName, '\x00') || !validManagedFileComment(comment) {
		return ErrNotFound
	}
	_, sheets, err := decodeSQLBotExcelConfig(prior.Configuration)
	if err != nil {
		return ErrUnavailable
	}
	imported := false
	for _, sheet := range sheets {
		if sheet.TableName == tableName {
			imported = true
			break
		}
	}
	if !imported {
		return ErrNotFound
	}
	comments, err := managedFileFieldComments(prior)
	if err != nil {
		return err
	}
	if comments == nil {
		comments = make(map[string]string)
	}
	key := managedFileFieldCommentKey(tableName, fieldName)
	unchanged := comments[key] == comment
	if comment == "" {
		delete(comments, key)
	} else {
		comments[key] = comment
	}
	if len(comments) > 256 {
		return ErrUnavailable
	}
	encoded := ""
	if len(comments) > 0 {
		body, err := json.Marshal(comments)
		if err != nil {
			return ErrUnavailable
		}
		encoded = string(body)
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
	if unchanged {
		return nil
	}
	current.FieldComments = encoded
	sealed, err := s.seal(object, current)
	if err != nil {
		return err
	}
	auditID, err := newID()
	if err != nil {
		return err
	}
	auditKey := objectKey{"management-audit", "system", "system", string(auditID)}
	audit, err := s.seal(auditKey, map[string]any{"action": "file-import.field-comment", "actor": p.User.ID,
		"workspace": p.Workspace, "datasource": prior.Datasource, "table": tableName, "field": fieldName,
		"comment_length": utf8.RuneCountInString(comment), "time": time.Now().UTC()})
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO chatbi_objects(kind,scope,owner,id,revision,body) VALUES(?,?,?,?,1,?)`,
		auditKey.Kind, auditKey.Scope, auditKey.Owner, auditKey.ID, audit); err != nil {
		return ErrUnavailable
	}
	if tx.Commit() != nil {
		return ErrUnavailable
	}
	return nil
}
