package chatbi

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

type tableRemarkInput struct {
	ID            ID              `json:"id"`
	DatasourceID  ID              `json:"ds_id"`
	TableName     string          `json:"table_name"`
	TableComment  string          `json:"table_comment"`
	CustomComment *string         `json:"custom_comment"`
	Checked       *bool           `json:"checked"`
	Embedding     json.RawMessage `json:"embedding"`
}

type fieldRemarkInput struct {
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

func validDatabaseRemarkName(name string) bool {
	return name != "" && len(name) <= 256 && utf8.ValidString(name) && !strings.ContainsRune(name, 0)
}

func databaseTableRemarkKey(schema, table string) string { return schema + "\x00" + table }
func databaseFieldRemarkKey(schema, table, field string) string {
	return schema + "\x00" + table + "\x00" + field
}

func databaseRemarks(binding Binding, field bool) (map[string]string, error) {
	if !databaseBinding(binding) {
		return nil, ErrUnavailable
	}
	encoded, limit := binding.TableComments, 256
	if field {
		encoded, limit = binding.FieldComments, 4096
	}
	if encoded == "" {
		return nil, nil
	}
	if len(encoded) > 1<<20 {
		return nil, ErrUnavailable
	}
	var comments map[string]string
	if json.Unmarshal([]byte(encoded), &comments) != nil || len(comments) > limit {
		return nil, ErrUnavailable
	}
	wantParts := 2
	if field {
		wantParts = 3
	}
	for key, comment := range comments {
		parts := strings.Split(key, "\x00")
		if len(parts) != wantParts || comment == "" || !validManagedFileComment(comment) {
			return nil, ErrUnavailable
		}
		for _, part := range parts {
			if !validDatabaseRemarkName(part) {
				return nil, ErrUnavailable
			}
		}
	}
	return comments, nil
}

func datasourceTableComments(binding Binding) (map[string]string, error) {
	if databaseBinding(binding) {
		return databaseRemarks(binding, false)
	}
	return managedFileTableComments(binding)
}

func datasourceFieldComments(binding Binding) (map[string]string, error) {
	if databaseBinding(binding) {
		return databaseRemarks(binding, true)
	}
	return managedFileFieldComments(binding)
}

func datasourceTableRemarkKey(binding Binding, schema, table string) string {
	if databaseBinding(binding) {
		return databaseTableRemarkKey(schema, table)
	}
	return table
}

func datasourceFieldRemarkKey(binding Binding, schema, table, field string) string {
	if databaseBinding(binding) {
		return databaseFieldRemarkKey(schema, table, field)
	}
	return managedFileFieldCommentKey(table, field)
}

func (h *Handler) editDatabaseTableComment(w http.ResponseWriter, r *http.Request, p Principal, token string,
	binding Binding, revision int64, digest [32]byte, input tableRemarkInput, tableID int64) {
	columns, err := h.service.schema(r.Context(), p, binding)
	if err != nil {
		failure(w, err)
		return
	}
	tables, valid := visibleBrowserTables(input.DatasourceID, columns)
	if !valid || len(tables) > 256 {
		failure(w, ErrGateway)
		return
	}
	for _, table := range tables {
		if table.id != tableID || table.name != input.TableName ||
			!validDatabaseRemarkName(table.schema) || !validDatabaseRemarkName(table.name) {
			continue
		}
		if err := h.currentIdentity(r.Context(), p, token); err != nil {
			failure(w, err)
			return
		}
		if err := h.service.bindingUnchanged(r.Context(), p, binding, revision, digest); err != nil {
			failure(w, err)
			return
		}
		if err := h.service.Store.updateDatabaseRemark(r.Context(), p, binding, table.schema, table.name, "", *input.CustomComment); err != nil {
			failure(w, err)
			return
		}
		okay(w, nil)
		return
	}
	failure(w, ErrNotFound)
}

func (h *Handler) editDatabaseFieldComment(w http.ResponseWriter, r *http.Request, p Principal, token string,
	binding Binding, revision int64, digest [32]byte, input fieldRemarkInput, fieldID, tableID int64) {
	columns, err := h.service.schema(r.Context(), p, binding)
	if err != nil {
		failure(w, err)
		return
	}
	tables, valid := visibleBrowserTables(input.DatasourceID, columns)
	if !valid || len(tables) > 256 {
		failure(w, ErrGateway)
		return
	}
	for _, table := range tables {
		if table.id != tableID || !validDatabaseRemarkName(table.schema) || !validDatabaseRemarkName(table.name) {
			continue
		}
		for index, column := range table.columns {
			if index != *input.FieldIndex || column.Name != input.FieldName ||
				column.DataType != input.FieldType || browserFieldID(tableID, column.Name) != fieldID ||
				!validDatabaseRemarkName(column.Name) {
				continue
			}
			if err := h.currentIdentity(r.Context(), p, token); err != nil {
				failure(w, err)
				return
			}
			if err := h.service.bindingUnchanged(r.Context(), p, binding, revision, digest); err != nil {
				failure(w, err)
				return
			}
			if err := h.service.Store.updateDatabaseRemark(r.Context(), p, binding, table.schema, table.name, column.Name, *input.CustomComment); err != nil {
				failure(w, err)
				return
			}
			okay(w, nil)
			return
		}
	}
	failure(w, ErrNotFound)
}

func (s *Store) updateDatabaseRemark(ctx context.Context, p Principal, prior Binding, schema, table, field, comment string) error {
	if s == nil || ctx == nil || !databaseBinding(prior) || prior.Workspace != p.Workspace ||
		prior.User != p.User.ID || !prior.Datasource.Valid() || !validDatabaseRemarkName(schema) ||
		!validDatabaseRemarkName(table) || field != "" && !validDatabaseRemarkName(field) ||
		!validManagedFileComment(comment) {
		return ErrNotFound
	}
	fieldRemark := field != ""
	comments, err := databaseRemarks(prior, fieldRemark)
	if err != nil {
		return err
	}
	if comments == nil {
		comments = make(map[string]string)
	}
	key := databaseTableRemarkKey(schema, table)
	if fieldRemark {
		key = databaseFieldRemarkKey(schema, table, field)
	}
	unchanged := comments[key] == comment
	if comment == "" {
		delete(comments, key)
	} else {
		comments[key] = comment
	}
	limit := 256
	if fieldRemark {
		limit = 4096
	}
	if len(comments) > limit {
		return ErrUnavailable
	}
	encoded := ""
	if len(comments) > 0 {
		body, err := json.Marshal(comments)
		if err != nil || len(body) > 1<<20 {
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
	if fieldRemark {
		current.FieldComments = encoded
	} else {
		current.TableComments = encoded
	}
	sealed, err := s.seal(object, current)
	if err != nil {
		return err
	}
	auditID, err := newID()
	if err != nil {
		return err
	}
	auditKey := objectKey{"management-audit", "system", "system", string(auditID)}
	action := "database.table-comment"
	if fieldRemark {
		action = "database.field-comment"
	}
	audit, err := s.seal(auditKey, map[string]any{"action": action, "actor": p.User.ID,
		"workspace": p.Workspace, "datasource": prior.Datasource, "schema": schema,
		"table": table, "field": field, "comment_length": utf8.RuneCountInString(comment), "time": time.Now().UTC()})
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

func datasourceRemarkHints(binding Binding, columns []Column) ([]RemarkHint, error) {
	if !databaseBinding(binding) {
		return managedFileRemarkHints(binding, columns)
	}
	tables, err := databaseRemarks(binding, false)
	if err != nil {
		return nil, err
	}
	fields, err := databaseRemarks(binding, true)
	if err != nil {
		return nil, err
	}
	if len(tables) == 0 && len(fields) == 0 {
		return nil, nil
	}
	visible, valid := visibleBrowserTables(binding.Datasource, columns)
	if !valid {
		return nil, ErrGateway
	}
	sort.Slice(visible, func(i, j int) bool {
		if visible[i].schema == visible[j].schema {
			return visible[i].name < visible[j].name
		}
		return visible[i].schema < visible[j].schema
	})
	hints := make([]RemarkHint, 0, len(tables)+len(fields))
	for _, table := range visible {
		if comment := tables[databaseTableRemarkKey(table.schema, table.name)]; comment != "" {
			hints = append(hints, RemarkHint{Schema: table.schema, Table: table.name, Remark: comment})
		}
		sort.Slice(table.columns, func(i, j int) bool { return table.columns[i].Name < table.columns[j].Name })
		for _, column := range table.columns {
			if comment := fields[databaseFieldRemarkKey(table.schema, table.name, column.Name)]; comment != "" {
				hints = append(hints, RemarkHint{Schema: table.schema, Table: table.name,
					Column: column.Name, Remark: comment})
			}
		}
	}
	for len(hints) > 0 {
		encoded, err := json.Marshal(hints)
		if err != nil {
			return nil, ErrUnavailable
		}
		if len(encoded) <= 256<<10 {
			break
		}
		hints = hints[:len(hints)-1]
	}
	return hints, nil
}
