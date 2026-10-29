package chatbi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"
	"unicode"
	"unicode/utf8"
)

func validManagedFileComment(comment string) bool {
	if !utf8.ValidString(comment) || utf8.RuneCountInString(comment) > 500 {
		return false
	}
	for _, char := range comment {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

// Table annotations stay in the encrypted, owner-scoped datasource binding.
// They are UI metadata and cannot create gateway permissions or SQL context.
func managedFileTableComments(binding Binding) (map[string]string, error) {
	if binding.TableComments == "" {
		return nil, nil
	}
	if binding.Type != "excel" || binding.Dialect != "pg" ||
		!managedFileOperationPattern.MatchString(binding.ManagedFileID) {
		return nil, ErrUnavailable
	}
	var comments map[string]string
	if json.Unmarshal([]byte(binding.TableComments), &comments) != nil || len(comments) > 8 {
		return nil, ErrUnavailable
	}
	for name, comment := range comments {
		if !managedFileNamePattern.MatchString(name) || comment == "" || !validManagedFileComment(comment) {
			return nil, ErrUnavailable
		}
	}
	return comments, nil
}

// The original table editor submits the current row. Its checked flag changes
// semantic selection; relation identity and gateway grants stay server-owned.
func (h *Handler) editDatasourceTableComment(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	var input struct {
		ID            ID              `json:"id"`
		DatasourceID  ID              `json:"ds_id"`
		TableName     string          `json:"table_name"`
		TableComment  string          `json:"table_comment"`
		CustomComment *string         `json:"custom_comment"`
		Checked       *bool           `json:"checked"`
		Embedding     json.RawMessage `json:"embedding"`
	}
	if !readBody(w, r, &input) {
		return
	}
	encodedID := string(input.ID)
	tableID, idErr := strconv.ParseInt(encodedID, 10, 64)
	if idErr != nil || tableID < 1 || tableID > maxSQLBotBrowserID ||
		strconv.FormatInt(tableID, 10) != encodedID || !input.DatasourceID.Valid() ||
		!databaseRemarkName(input.TableName) || input.TableComment != "" ||
		input.CustomComment == nil || !validManagedFileComment(*input.CustomComment) ||
		input.Checked == nil ||
		len(input.Embedding) > 0 && string(input.Embedding) != "null" {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid table remark")
		return
	}
	binding, revision, digest, err := h.service.bindingSnapshot(r.Context(), p, input.DatasourceID)
	file := binding.Type == "excel" && binding.Dialect == "pg" &&
		managedFileOperationPattern.MatchString(binding.ManagedFileID)
	if err != nil || (!file && !databaseBinding(binding)) ||
		(file && !managedFileNamePattern.MatchString(input.TableName)) {
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
	found, schema := false, ""
	for _, table := range tables {
		if table.id == tableID && table.name == input.TableName && (!file || table.schema == "agentsql_files") {
			found = true
			schema = table.schema
			break
		}
	}
	if !found {
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
	if file {
		err = h.service.Store.updateManagedFileTableComment(r.Context(), p, binding, input.TableName, *input.CustomComment, *input.Checked)
	} else {
		err = h.service.Store.updateDatabaseRemark(r.Context(), p, binding, schema, input.TableName, "", *input.CustomComment, input.Checked)
	}
	if err != nil {
		failure(w, err)
		return
	}
	okay(w, nil)
}

func (s *Store) updateManagedFileTableComment(ctx context.Context, p Principal, prior Binding, tableName, comment string, checked bool) error {
	if s == nil || ctx == nil || prior.Workspace != p.Workspace || prior.User != p.User.ID ||
		!prior.Datasource.Valid() || prior.Type != "excel" || prior.Dialect != "pg" ||
		!managedFileOperationPattern.MatchString(prior.ManagedFileID) ||
		!managedFileNamePattern.MatchString(tableName) || !validManagedFileComment(comment) {
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
	comments, err := managedFileTableComments(prior)
	if err != nil {
		return err
	}
	if comments == nil {
		comments = make(map[string]string)
	}
	commentUnchanged := comments[tableName] == comment
	if comment == "" {
		delete(comments, tableName)
	} else {
		comments[tableName] = comment
	}
	encoded := ""
	if len(comments) > 0 {
		body, err := json.Marshal(comments)
		if err != nil {
			return ErrUnavailable
		}
		encoded = string(body)
	}
	updated := prior
	if err := setTableStatus(&updated, "agentsql_files", tableName, checked); err != nil {
		return err
	}
	unchanged := commentUnchanged && updated.DisabledTables == prior.DisabledTables
	key := bindingKey(p, prior.Datasource)
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
		key.Kind, key.Scope, key.Owner, key.ID).Scan(&revision, &oldBody)
	if controlNoRows(err) {
		return ErrNotFound
	}
	if err != nil {
		return ErrUnavailable
	}
	var current Binding
	if s.decode(key, oldBody, &current) != nil || current != prior {
		return ErrConflict
	}
	if unchanged {
		return nil
	}
	current.TableComments = encoded
	current.DisabledTables = updated.DisabledTables
	sealed, err := s.seal(key, current)
	if err != nil {
		return err
	}
	auditID, err := newID()
	if err != nil {
		return err
	}
	auditKey := objectKey{"management-audit", "system", "system", string(auditID)}
	action := "file-import.table-comment"
	if commentUnchanged {
		action = "file-import.table-status"
	}
	audit, err := s.seal(auditKey, map[string]any{"action": action, "actor": p.User.ID,
		"workspace": p.Workspace, "datasource": prior.Datasource, "table": tableName,
		"comment_length": utf8.RuneCountInString(comment), "checked": checked, "time": time.Now().UTC()})
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE chatbi_objects SET revision=revision+1,body=? WHERE kind=? AND scope=? AND owner=? AND id=? AND revision=?`,
		sealed, key.Kind, key.Scope, key.Owner, key.ID, revision)
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
