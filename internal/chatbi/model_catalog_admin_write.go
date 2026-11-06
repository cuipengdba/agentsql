package chatbi

import (
	"context"
	"errors"
	"time"
)

// CreateGlobalModel adds one shared model without implicit workspace mappings.
// The first model becomes the global default in the same audited transaction.
func (s *Store) CreateGlobalModel(ctx context.Context, p Principal, update WorkspaceModelUpdate) (ID, error) {
	if !p.Workspace.Valid() || !p.User.ID.Valid() || p.EmbeddedApp != "" || update.Revision != 0 {
		return "", ErrInvalidAccountChange
	}
	id, err := newID()
	if err != nil {
		return "", err
	}
	model := ModelConfig{ID: id, Name: update.Name, Model: update.Model, Endpoint: update.Endpoint,
		MaxTokens: update.MaxTokens, Temperature: update.Temperature, Supplier: update.Supplier, EnableThinking: update.EnableThinking}
	if update.APIKey != nil {
		model.APIKey = *update.APIKey
	}
	if validateModelConfig(model) != nil {
		return "", ErrInvalidAccountChange
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", ErrUnavailable
	}
	defer tx.Rollback()
	if err := s.verifyManagedWorkspaceActor(ctx, tx, p); err != nil {
		return "", err
	}
	if err := s.requireActiveModelCatalogTx(ctx, tx); err != nil {
		return "", err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM chatbi_objects
		WHERE kind='model-catalog' AND scope='system' AND owner='system'`).Scan(&count); err != nil || count >= 10000 {
		return "", ErrUnavailable
	}
	defaultID, defaultErr := s.catalogDefaultTx(ctx, tx)
	if count == 0 {
		if !errors.Is(defaultErr, ErrModel) {
			return "", ErrUnavailable
		}
	} else {
		if defaultErr != nil {
			return "", ErrUnavailable
		}
		if _, _, _, err := s.catalogModelTx(ctx, tx, defaultID); err != nil {
			return "", ErrUnavailable
		}
	}
	if err := s.insertModelMigrationObject(ctx, tx, modelCatalogKey(id), model); err != nil {
		return "", err
	}
	if count == 0 {
		if err := s.insertModelMigrationObject(ctx, tx, modelGlobalDefaultKey(), id); err != nil {
			return "", err
		}
	}
	auditID, err := newID()
	if err != nil {
		return "", err
	}
	if err := s.insertModelMigrationObject(ctx, tx,
		objectKey{"management-audit", "system", "system", string(auditID)}, map[string]any{
			"action": "model-catalog.create", "actor": p.User.ID,
			"model": id, "default": count == 0, "time": time.Now().UTC(),
		}); err != nil {
		return "", err
	}
	if tx.Commit() != nil {
		return "", ErrUnavailable
	}
	return id, nil
}

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

// UpdateGlobalModel edits one shared model ID. An omitted key preserves the
// existing encrypted credential; an explicit empty key clears it.
func (s *Store) UpdateGlobalModel(ctx context.Context, p Principal, id ID, update WorkspaceModelUpdate) error {
	if !p.Workspace.Valid() || !p.User.ID.Valid() || !id.Valid() || p.EmbeddedApp != "" || update.Revision < 1 {
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
	old, revision, oldBody, err := s.catalogModelTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if revision != update.Revision {
		return ErrConflict
	}
	next := old
	next.Name, next.Model, next.Endpoint, next.MaxTokens = update.Name, update.Model, update.Endpoint, update.MaxTokens
	if update.ReplaceTemperature || update.Temperature != nil {
		next.Temperature = update.Temperature
	}
	if update.ReplaceThinking || update.EnableThinking != nil {
		next.EnableThinking = update.EnableThinking
	}
	if update.Supplier != 0 {
		next.Supplier = update.Supplier
	}
	if update.APIKey != nil {
		next.APIKey = *update.APIKey
	}
	if validateModelConfig(next) != nil {
		return ErrInvalidAccountChange
	}
	key := modelCatalogKey(id)
	newBody, err := s.seal(key, next)
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
			"action": "model-catalog.update", "actor": p.User.ID,
			"model": id, "time": time.Now().UTC(),
		}); err != nil {
		return err
	}
	if tx.Commit() != nil {
		return ErrUnavailable
	}
	return nil
}

// DeleteGlobalModel removes a non-default model and its explicit mappings in
// one audited transaction. The default pointer cannot be left dangling.
func (s *Store) DeleteGlobalModel(ctx context.Context, p Principal, id ID, expectedRevision int64) error {
	if !p.Workspace.Valid() || !p.User.ID.Valid() || !id.Valid() || p.EmbeddedApp != "" || expectedRevision < 1 {
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
	_, revision, body, err := s.catalogModelTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if revision != expectedRevision {
		return ErrConflict
	}
	defaultID, err := s.catalogDefaultTx(ctx, tx)
	if err != nil {
		return err
	}
	if id == defaultID {
		return ErrConflict
	}
	mappings, err := s.catalogMappingSetTx(ctx, tx, id)
	if err != nil {
		return err
	}
	for workspace := range mappings {
		key := modelMappingKey(id, workspace)
		result, err := tx.ExecContext(ctx, `DELETE FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=?`,
			key.Kind, key.Scope, key.Owner, key.ID)
		if err != nil {
			return ErrUnavailable
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return ErrConflict
		}
	}
	key := modelCatalogKey(id)
	result, err := tx.ExecContext(ctx, `DELETE FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=? AND revision=? AND body=?`,
		key.Kind, key.Scope, key.Owner, key.ID, revision, body)
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
			"action": "model-catalog.delete", "actor": p.User.ID,
			"model": id, "mappings": len(mappings), "time": time.Now().UTC(),
		}); err != nil {
		return err
	}
	if tx.Commit() != nil {
		return ErrUnavailable
	}
	return nil
}
