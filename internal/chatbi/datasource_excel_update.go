package chatbi

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"time"
)

// The original browser resubmits the public AES Excel configuration when
// editing metadata. It is a wire format, not authority to change the imported
// file, gateway binding, selected tables, or database credentials.
func (h *Handler) updateExcelDatasource(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	var input struct {
		ID            ID     `json:"id"`
		Name          string `json:"name"`
		Description   string `json:"description"`
		Type          string `json:"type"`
		Configuration string `json:"configuration"`
	}
	if !readBody(w, r, &input) {
		return
	}
	if !input.ID.Valid() || input.Type != "excel" || input.Name == "" ||
		strings.TrimSpace(input.Name) != input.Name || len(input.Name) > 50 ||
		len(input.Description) > 512 || strings.ContainsAny(input.Name, "/\\\r\n\x00") ||
		strings.ContainsAny(input.Description, "\x00") {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid Excel datasource metadata")
		return
	}
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	prior, _, _, err := h.service.bindingSnapshot(r.Context(), p, input.ID)
	if err != nil || prior.Type != "excel" || prior.Dialect != "pg" ||
		!managedFileOperationPattern.MatchString(prior.ManagedFileID) {
		failure(w, ErrNotFound)
		return
	}
	oldFile, oldSheets, oldErr := decodeSQLBotExcelConfig(prior.Configuration)
	newFile, newSheets, newErr := decodeSQLBotExcelConfig(input.Configuration)
	if oldErr != nil || newErr != nil || oldFile != newFile || !reflect.DeepEqual(oldSheets, newSheets) {
		writeJSON(w, http.StatusUnprocessableEntity, "Excel datasource file cannot be changed")
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	if err := h.service.Store.updateManagedFileMetadata(r.Context(), p, prior, input.Name, input.Description); err != nil {
		failure(w, err)
		return
	}
	updated := prior
	updated.Name, updated.Description = input.Name, input.Description
	payload, err := legacyExcelDatasource(updated)
	if err != nil {
		failure(w, err)
		return
	}
	okay(w, payload)
}

func (s *Store) updateManagedFileMetadata(ctx context.Context, p Principal, prior Binding, name, description string) error {
	if s == nil || ctx == nil || prior.Workspace != p.Workspace || prior.User != p.User.ID ||
		!prior.Datasource.Valid() || prior.Type != "excel" || prior.Dialect != "pg" ||
		!managedFileOperationPattern.MatchString(prior.ManagedFileID) {
		return ErrNotFound
	}
	key := bindingKey(p, prior.Datasource)
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
	if err := tx.QueryRowContext(ctx, `SELECT revision,body FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=?`,
		key.Kind, key.Scope, key.Owner, key.ID).Scan(&revision, &body); controlNoRows(err) {
		return ErrNotFound
	} else if err != nil {
		return ErrUnavailable
	}
	var current Binding
	if s.decode(key, body, &current) != nil || current != prior {
		return ErrConflict
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,body FROM chatbi_objects WHERE kind='binding' AND scope=? AND owner=? AND id!=?`,
		string(p.Workspace), string(p.User.ID), string(prior.Datasource))
	if err != nil {
		return ErrUnavailable
	}
	for rows.Next() {
		var id string
		var encrypted []byte
		if err := rows.Scan(&id, &encrypted); err != nil {
			rows.Close()
			return ErrUnavailable
		}
		var other Binding
		if s.decode(bindingKey(p, ID(id)), encrypted, &other) != nil ||
			other.Workspace != p.Workspace || other.User != p.User.ID {
			rows.Close()
			return ErrUnavailable
		}
		if strings.EqualFold(other.Name, name) {
			rows.Close()
			return ErrConflict
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return ErrUnavailable
	}
	rows.Close()
	if current.Name == name && current.Description == description {
		return nil
	}
	current.Name, current.Description = name, description
	sealed, err := s.seal(key, current)
	if err != nil {
		return err
	}
	auditID, err := newID()
	if err != nil {
		return err
	}
	auditKey := objectKey{"management-audit", "system", "system", string(auditID)}
	audit, err := s.seal(auditKey, map[string]any{"action": "file-import.datasource-update", "actor": p.User.ID,
		"workspace": p.Workspace, "datasource": prior.Datasource, "name": name,
		"description_length": len(description), "time": time.Now().UTC()})
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE chatbi_objects SET revision=revision+1,body=? WHERE kind=? AND scope=? AND owner=? AND id=? AND revision=?`,
		sealed, key.Kind, key.Scope, key.Owner, key.ID, revision)
	if err != nil {
		return ErrUnavailable
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO chatbi_objects(kind,scope,owner,id,revision,body) VALUES(?,?,?,?,1,?)`,
		auditKey.Kind, auditKey.Scope, auditKey.Owner, auditKey.ID, audit); err != nil {
		return ErrUnavailable
	}
	if err := tx.Commit(); err != nil {
		return ErrUnavailable
	}
	return nil
}
