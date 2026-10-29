package chatbi

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"
)

// Database remarks are descriptive, owner-scoped UI data. The current gateway
// schema, not these saved strings, decides which relations and columns exist.
func databaseRemarkName(name string) bool {
	return name != "" && len(name) <= 256 && utf8.ValidString(name) && !strings.ContainsRune(name, 0)
}

func databaseTableCommentKey(schema, table string) string { return schema + "\x00" + table }
func databaseFieldCommentKey(schema, table, field string) string {
	return schema + "\x00" + table + "\x00" + field
}

func databaseRemarks(binding Binding, field bool) (map[string]string, error) {
	var encoded string
	parts, limit := 2, 256
	if field {
		encoded, parts, limit = binding.FieldComments, 3, 4096
	} else {
		encoded = binding.TableComments
	}
	if encoded == "" {
		return nil, nil
	}
	if !databaseBinding(binding) || len(encoded) > 2<<20 {
		return nil, ErrUnavailable
	}
	var comments map[string]string
	if json.Unmarshal([]byte(encoded), &comments) != nil || len(comments) > limit {
		return nil, ErrUnavailable
	}
	for key, comment := range comments {
		segments := strings.Split(key, "\x00")
		if len(segments) != parts || comment == "" || !validManagedFileComment(comment) {
			return nil, ErrUnavailable
		}
		for _, segment := range segments {
			if !databaseRemarkName(segment) {
				return nil, ErrUnavailable
			}
		}
	}
	return comments, nil
}

func (s *Store) updateDatabaseRemark(ctx context.Context, p Principal, prior Binding,
	schema, table, field, comment string) error {
	if s == nil || ctx == nil || prior.Workspace != p.Workspace || prior.User != p.User.ID ||
		!prior.Datasource.Valid() || !databaseBinding(prior) ||
		!databaseRemarkName(schema) || !databaseRemarkName(table) ||
		(field != "" && !databaseRemarkName(field)) || !validManagedFileComment(comment) {
		return ErrNotFound
	}
	isField := field != ""
	comments, err := databaseRemarks(prior, isField)
	if err != nil {
		return err
	}
	if comments == nil {
		comments = make(map[string]string)
	}
	key := databaseTableCommentKey(schema, table)
	if isField {
		key = databaseFieldCommentKey(schema, table, field)
	}
	if comments[key] == comment {
		return nil
	}
	if comment == "" {
		delete(comments, key)
	} else {
		comments[key] = comment
	}
	if (!isField && len(comments) > 256) || (isField && len(comments) > 4096) {
		return ErrUnavailable
	}
	encoded := ""
	if len(comments) > 0 {
		body, err := json.Marshal(comments)
		if err != nil || len(body) > 2<<20 {
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
	var body []byte
	var revision int64
	if err := tx.QueryRowContext(ctx,
		`SELECT revision,body FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=?`,
		object.Kind, object.Scope, object.Owner, object.ID).Scan(&revision, &body); controlNoRows(err) {
		return ErrNotFound
	} else if err != nil {
		return ErrUnavailable
	}
	var current Binding
	if s.decode(object, body, &current) != nil || current != prior {
		return ErrConflict
	}
	action := "database.table-comment"
	if isField {
		action = "database.field-comment"
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
	audit, err := s.seal(auditKey, map[string]any{"action": action, "actor": p.User.ID,
		"workspace": p.Workspace, "datasource": prior.Datasource, "schema": schema,
		"table": table, "field": field, "comment_length": utf8.RuneCountInString(comment),
		"time": time.Now().UTC()})
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx,
		`UPDATE chatbi_objects SET revision=revision+1,body=? WHERE kind=? AND scope=? AND owner=? AND id=? AND revision=?`,
		sealed, object.Kind, object.Scope, object.Owner, object.ID, revision)
	if err != nil {
		return ErrUnavailable
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return ErrConflict
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
