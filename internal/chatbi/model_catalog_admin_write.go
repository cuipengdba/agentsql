package chatbi

import (
	"context"
	"time"
)

// SetGlobalDefaultModel changes the catalog's one default pointer. Every
// in-flight question holding the previous pointer revision fails its lease.
func (s *Store) SetGlobalDefaultModel(ctx context.Context, p Principal, id ID) error {
	if !p.Workspace.Valid() || !p.User.ID.Valid() || !id.Valid() || p.EmbeddedApp != "" {
		return ErrInvalidAccountChange
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	defer tx.Rollback()
	if err := s.verifyManagedWorkspaceActor(ctx, tx, p); err != nil {
		return err
	}
	if err := s.requireActiveModelCatalogTx(ctx, tx); err != nil {
		return err
	}
	if _, _, _, err := s.catalogModelTx(ctx, tx, id); err != nil {
		return err
	}
	key := modelGlobalDefaultKey()
	var oldBody []byte
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT revision,body FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=?`,
		key.Kind, key.Scope, key.Owner, key.ID).Scan(&revision, &oldBody); err != nil {
		return ErrUnavailable
	}
	var previous ID
	if s.decode(key, oldBody, &previous) != nil || !previous.Valid() {
		return ErrUnavailable
	}
	if _, _, _, err := s.catalogModelTx(ctx, tx, previous); err != nil {
		return ErrUnavailable
	}
	if previous == id {
		return nil
	}
	newBody, err := s.seal(key, id)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE chatbi_objects SET revision=revision+1,body=?
		WHERE kind=? AND scope=? AND owner=? AND id=? AND revision=? AND body=?`,
		newBody, key.Kind, key.Scope, key.Owner, key.ID, revision, oldBody)
	if err != nil {
		return ErrUnavailable
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return ErrConflict
	}
	auditID, err := newID()
	if err != nil {
		return err
	}
	if err := s.insertModelMigrationObject(ctx, tx,
		objectKey{"management-audit", "system", "system", string(auditID)}, map[string]any{
			"action": "model-catalog.set-default", "actor": p.User.ID,
			"previous": previous, "model": id, "time": time.Now().UTC(),
		}); err != nil {
		return err
	}
	if tx.Commit() != nil {
		return ErrUnavailable
	}
	return nil
}
