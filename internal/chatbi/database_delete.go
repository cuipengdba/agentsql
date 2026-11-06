package chatbi

import (
	"context"
	"net/http"
	"time"
)

// Database deletion removes this Q&A alias only. The gateway binding and
// database account belong to the operator and are not modified by a browser.
func (h *Handler) deleteDatasource(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	id := ID(r.PathValue("id"))
	if !id.Valid() {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid datasource ID")
		return
	}
	binding, revision, digest, err := h.service.bindingSnapshot(r.Context(), p, id)
	if err != nil {
		failure(w, err)
		return
	}
	if binding.Name != r.PathValue("name") {
		failure(w, ErrNotFound)
		return
	}
	if binding.Type == "excel" {
		h.deleteExcelDatasource(w, r, p, token)
		return
	}
	if !databaseBinding(binding) {
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
	if err := h.service.Store.retireDatabaseDatasource(r.Context(), p, binding); err != nil {
		failure(w, err)
		return
	}
	okay(w, map[string]any{"message": "Datasource with ID " + string(id) + " deleted successfully."})
}

func (s *Store) retireDatabaseDatasource(ctx context.Context, p Principal, prior Binding) error {
	if s == nil || ctx == nil || !databaseBinding(prior) || prior.Workspace != p.Workspace ||
		prior.User != p.User.ID || !prior.Datasource.Valid() {
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
	if err := tx.QueryRowContext(ctx,
		`SELECT revision,body FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=?`,
		key.Kind, key.Scope, key.Owner, key.ID).Scan(&revision, &body); controlNoRows(err) {
		return ErrNotFound
	} else if err != nil {
		return ErrUnavailable
	}
	var current Binding
	if s.decode(key, body, &current) != nil || current != prior {
		return ErrConflict
	}
	retiredKey := objectKey{"retired-datasource", string(p.Workspace), string(p.User.ID), string(prior.Datasource)}
	retired, err := s.seal(retiredKey, map[string]any{"retired_at": time.Now().UTC(),
		"datasource": prior.Datasource, "kind": "database-alias"})
	if err != nil {
		return err
	}
	auditID, err := newID()
	if err != nil {
		return err
	}
	auditKey := objectKey{"management-audit", "system", "system", string(auditID)}
	audit, err := s.seal(auditKey, map[string]any{"action": "database.datasource-delete",
		"actor": p.User.ID, "workspace": p.Workspace, "datasource": prior.Datasource,
		"gateway_unchanged": true, "time": time.Now().UTC()})
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx,
		`DELETE FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=? AND revision=?`,
		key.Kind, key.Scope, key.Owner, key.ID, revision)
	if err != nil {
		return ErrUnavailable
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return ErrConflict
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO chatbi_objects(kind,scope,owner,id,revision,body) VALUES(?,?,?,?,1,?)`,
		retiredKey.Kind, retiredKey.Scope, retiredKey.Owner, retiredKey.ID, retired); err != nil {
		return ErrUnavailable
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
