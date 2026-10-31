package chatbi

import (
	"context"
	"encoding/json"
	"time"
)

func additionalModelKey(workspace, model ID) objectKey {
	return objectKey{"model-secondary", string(workspace), "workspace", string(model)}
}

// Additional models are workspace-local and cannot affect query execution
// until an administrator atomically selects one as the workspace default.
func (s *Store) AdditionalWorkspaceModels(ctx context.Context, workspace ID) ([]ModelConfig, error) {
	if !workspace.Valid() {
		return nil, ErrInvalidAccountChange
	}
	raw, err := s.list(ctx, "model-secondary", string(workspace), "workspace")
	if err != nil {
		return nil, err
	}
	if len(raw) > 100 {
		return nil, ErrUnavailable
	}
	models := make([]ModelConfig, 0, len(raw))
	for _, item := range raw {
		var model ModelConfig
		if json.Unmarshal(item, &model) != nil || validateModelConfig(model) != nil {
			return nil, ErrUnavailable
		}
		models = append(models, model)
	}
	return models, nil
}

func (s *Store) CreateAdditionalWorkspaceModel(ctx context.Context, p Principal, update WorkspaceModelUpdate) error {
	if !p.Workspace.Valid() || !p.User.ID.Valid() || p.EmbeddedApp != "" || update.Revision != 0 {
		return ErrInvalidAccountChange
	}
	id, err := newID()
	if err != nil {
		return err
	}
	model := ModelConfig{ID: id, Name: update.Name, Model: update.Model, Endpoint: update.Endpoint, MaxTokens: update.MaxTokens, Temperature: update.Temperature, Supplier: update.Supplier}
	if update.APIKey != nil {
		model.APIKey = *update.APIKey
	}
	if validateModelConfig(model) != nil {
		return ErrInvalidAccountChange
	}
	key := additionalModelKey(p.Workspace, model.ID)
	auditID, err := newID()
	if err != nil {
		return err
	}
	auditKey := objectKey{"management-audit", "system", "system", string(auditID)}
	sealed, err := s.seal(key, model)
	if err != nil {
		return err
	}
	audit, err := s.seal(auditKey, map[string]any{"action": "workspace-model.create-secondary", "actor": p.User.ID,
		"workspace": p.Workspace, "model": model.ID, "time": time.Now().UTC()})
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	defer tx.Rollback()
	if err := s.verifyManagedWorkspaceActor(ctx, tx, p); err != nil {
		return err
	}
	var defaultBody []byte
	err = tx.QueryRowContext(ctx, `SELECT body FROM chatbi_objects WHERE kind='model' AND scope=? AND owner='workspace' AND id='default'`, string(p.Workspace)).Scan(&defaultBody)
	if controlNoRows(err) {
		return ErrNotFound
	}
	var current ModelConfig
	if err != nil || s.decode(objectKey{"model", string(p.Workspace), "workspace", "default"}, defaultBody, &current) != nil || validateModelConfig(current) != nil {
		return ErrUnavailable
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM chatbi_objects WHERE kind='model-secondary' AND scope=? AND owner='workspace'`, string(p.Workspace)).Scan(&count); err != nil {
		return ErrUnavailable
	}
	if count >= 100 {
		return ErrConflict
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO chatbi_objects(kind,scope,owner,id,revision,body) VALUES(?,?,?,?,1,?) ON CONFLICT DO NOTHING`,
		key.Kind, key.Scope, key.Owner, key.ID, sealed)
	if err != nil {
		return ErrUnavailable
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
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

func (s *Store) UpdateAdditionalWorkspaceModel(ctx context.Context, p Principal, id ID, update WorkspaceModelUpdate) error {
	if !p.Workspace.Valid() || !p.User.ID.Valid() || !id.Valid() || p.EmbeddedApp != "" {
		return ErrInvalidAccountChange
	}
	key := additionalModelKey(p.Workspace, id)
	var old ModelConfig
	revision, err := s.get(ctx, key, &old)
	if err != nil {
		return err
	}
	if old.ID != id || validateModelConfig(old) != nil || update.Revision != revision {
		return ErrConflict
	}
	next := old
	next.Name, next.Model, next.Endpoint, next.MaxTokens = update.Name, update.Model, update.Endpoint, update.MaxTokens
	if update.ReplaceTemperature || update.Temperature != nil {
		next.Temperature = update.Temperature
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
	auditID, err := newID()
	if err != nil {
		return err
	}
	auditKey := objectKey{"management-audit", "system", "system", string(auditID)}
	sealed, err := s.seal(key, next)
	if err != nil {
		return err
	}
	audit, err := s.seal(auditKey, map[string]any{"action": "workspace-model.update-secondary", "actor": p.User.ID,
		"workspace": p.Workspace, "model": id, "time": time.Now().UTC()})
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	defer tx.Rollback()
	if err := s.verifyManagedWorkspaceActor(ctx, tx, p); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE chatbi_objects SET revision=revision+1,body=? WHERE kind=? AND scope=? AND owner=? AND id=? AND revision=?`,
		sealed, key.Kind, key.Scope, key.Owner, key.ID, revision)
	if err != nil {
		return ErrUnavailable
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
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

func (s *Store) DeleteAdditionalWorkspaceModel(ctx context.Context, p Principal, id ID) error {
	if !p.Workspace.Valid() || !p.User.ID.Valid() || !id.Valid() || p.EmbeddedApp != "" {
		return ErrInvalidAccountChange
	}
	key := additionalModelKey(p.Workspace, id)
	var current ModelConfig
	revision, err := s.get(ctx, key, &current)
	if err != nil {
		return err
	}
	if current.ID != id || validateModelConfig(current) != nil {
		return ErrUnavailable
	}
	auditID, err := newID()
	if err != nil {
		return err
	}
	auditKey := objectKey{"management-audit", "system", "system", string(auditID)}
	audit, err := s.seal(auditKey, map[string]any{"action": "workspace-model.delete-secondary", "actor": p.User.ID,
		"workspace": p.Workspace, "model": id, "time": time.Now().UTC()})
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	defer tx.Rollback()
	if err := s.verifyManagedWorkspaceActor(ctx, tx, p); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=? AND revision=?`,
		key.Kind, key.Scope, key.Owner, key.ID, revision)
	if err != nil {
		return ErrUnavailable
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
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

// SetDefaultWorkspaceModel atomically swaps the active model with a secondary
// entry. Every Q&A read continues to use the single encrypted default object.
func (s *Store) SetDefaultWorkspaceModel(ctx context.Context, p Principal, id ID) error {
	if !p.Workspace.Valid() || !p.User.ID.Valid() || !id.Valid() || p.EmbeddedApp != "" {
		return ErrInvalidAccountChange
	}
	defaultKey := objectKey{"model", string(p.Workspace), "workspace", "default"}
	candidateKey := additionalModelKey(p.Workspace, id)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	defer tx.Rollback()
	if err := s.verifyManagedWorkspaceActor(ctx, tx, p); err != nil {
		return err
	}
	var defaultBody []byte
	var defaultRevision int64
	err = tx.QueryRowContext(ctx, `SELECT revision,body FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=?`,
		defaultKey.Kind, defaultKey.Scope, defaultKey.Owner, defaultKey.ID).Scan(&defaultRevision, &defaultBody)
	if controlNoRows(err) {
		return ErrNotFound
	}
	var previous ModelConfig
	if err != nil || s.decode(defaultKey, defaultBody, &previous) != nil || validateModelConfig(previous) != nil {
		return ErrUnavailable
	}
	if previous.ID == id {
		return nil
	}
	var candidateBody []byte
	var candidateRevision int64
	err = tx.QueryRowContext(ctx, `SELECT revision,body FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=?`,
		candidateKey.Kind, candidateKey.Scope, candidateKey.Owner, candidateKey.ID).Scan(&candidateRevision, &candidateBody)
	if controlNoRows(err) {
		return ErrNotFound
	}
	var candidate ModelConfig
	if err != nil || s.decode(candidateKey, candidateBody, &candidate) != nil || validateModelConfig(candidate) != nil || candidate.ID != id {
		return ErrUnavailable
	}
	newDefault, err := s.seal(defaultKey, candidate)
	if err != nil {
		return err
	}
	previousKey := additionalModelKey(p.Workspace, previous.ID)
	oldDefault, err := s.seal(previousKey, previous)
	if err != nil {
		return err
	}
	auditID, err := newID()
	if err != nil {
		return err
	}
	auditKey := objectKey{"management-audit", "system", "system", string(auditID)}
	audit, err := s.seal(auditKey, map[string]any{"action": "workspace-model.set-default", "actor": p.User.ID,
		"workspace": p.Workspace, "model": id, "previous_model": previous.ID, "time": time.Now().UTC()})
	if err != nil {
		return err
	}
	updated, err := tx.ExecContext(ctx, `UPDATE chatbi_objects SET revision=revision+1,body=? WHERE kind=? AND scope=? AND owner=? AND id=? AND revision=?`,
		newDefault, defaultKey.Kind, defaultKey.Scope, defaultKey.Owner, defaultKey.ID, defaultRevision)
	if err != nil {
		return ErrUnavailable
	}
	changed, err := updated.RowsAffected()
	if err != nil || changed != 1 {
		return ErrConflict
	}
	deleted, err := tx.ExecContext(ctx, `DELETE FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=? AND revision=?`,
		candidateKey.Kind, candidateKey.Scope, candidateKey.Owner, candidateKey.ID, candidateRevision)
	if err != nil {
		return ErrUnavailable
	}
	changed, err = deleted.RowsAffected()
	if err != nil || changed != 1 {
		return ErrConflict
	}
	inserted, err := tx.ExecContext(ctx, `INSERT INTO chatbi_objects(kind,scope,owner,id,revision,body) VALUES(?,?,?,?,1,?) ON CONFLICT DO NOTHING`,
		previousKey.Kind, previousKey.Scope, previousKey.Owner, previousKey.ID, oldDefault)
	if err != nil {
		return ErrUnavailable
	}
	changed, err = inserted.RowsAffected()
	if err != nil || changed != 1 {
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
