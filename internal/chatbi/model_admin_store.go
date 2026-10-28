package chatbi

import (
	"context"
	"time"
)

// WorkspaceModelUpdate changes the model used by this workspace. An omitted
// API key preserves the encrypted key; an explicit empty string clears it.
type WorkspaceModelUpdate struct {
	Revision           int64    `json:"revision"`
	Name               string   `json:"name"`
	Model              string   `json:"base_model"`
	Endpoint           string   `json:"api_domain"`
	APIKey             *string  `json:"api_key"`
	MaxTokens          int      `json:"max_tokens"`
	Temperature        *float64 `json:"temperature,omitempty"`
	ReplaceTemperature bool     `json:"-"`
	Supplier           int      `json:"supplier,omitempty"`
}

// CreateWorkspaceModel installs the first default model for a workspace.
// A second create must fail without replacing an existing encrypted key.
func (s *Store) CreateWorkspaceModel(ctx context.Context, p Principal, update WorkspaceModelUpdate) error {
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
	key := objectKey{"model", string(p.Workspace), "workspace", "default"}
	auditID, err := newID()
	if err != nil {
		return err
	}
	auditKey := objectKey{"management-audit", "system", "system", string(auditID)}
	sealed, err := s.seal(key, model)
	if err != nil {
		return err
	}
	audit, err := s.seal(auditKey, map[string]any{"action": "workspace-model.create", "actor": p.User.ID,
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
	result, err := tx.ExecContext(ctx, `INSERT INTO chatbi_objects(kind,scope,owner,id,revision,body) VALUES(?,?,?,?,1,?) ON CONFLICT DO NOTHING`,
		key.Kind, key.Scope, key.Owner, key.ID, sealed)
	if err != nil {
		return ErrUnavailable
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
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

func (s *Store) UpdateWorkspaceModel(ctx context.Context, p Principal, update WorkspaceModelUpdate) error {
	if !p.Workspace.Valid() || !p.User.ID.Valid() || p.EmbeddedApp != "" || update.Revision < 1 {
		return ErrInvalidAccountChange
	}
	key := objectKey{"model", string(p.Workspace), "workspace", "default"}
	auditID, err := newID()
	if err != nil {
		return err
	}
	auditKey := objectKey{"management-audit", "system", "system", string(auditID)}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	defer tx.Rollback()
	if err := s.verifyManagedWorkspaceActor(ctx, tx, p); err != nil {
		return err
	}
	var oldBody []byte
	var oldRevision int64
	err = tx.QueryRowContext(ctx, `SELECT revision,body FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=?`,
		key.Kind, key.Scope, key.Owner, key.ID).Scan(&oldRevision, &oldBody)
	if controlNoRows(err) {
		return ErrNotFound
	}
	if err != nil {
		return ErrUnavailable
	}
	if oldRevision != update.Revision {
		return ErrConflict
	}
	var old ModelConfig
	if s.decode(key, oldBody, &old) != nil || validateModelConfig(old) != nil {
		return ErrUnavailable
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
	sealed, err := s.seal(key, next)
	if err != nil {
		return err
	}
	audit, err := s.seal(auditKey, map[string]any{"action": "workspace-model.update", "actor": p.User.ID,
		"workspace": p.Workspace, "model": next.ID, "time": time.Now().UTC()})
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE chatbi_objects SET revision=revision+1,body=? WHERE kind=? AND scope=? AND owner=? AND id=? AND revision=?`,
		sealed, key.Kind, key.Scope, key.Owner, key.ID, oldRevision)
	if err != nil {
		return ErrUnavailable
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
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
