package chatbi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"time"
)

type stagedModelCatalog struct {
	SourceDigest string `json:"source_digest"`
	Default      ID     `json:"default"`
	Models       int    `json:"models"`
	Mappings     int    `json:"mappings"`
}

func modelCatalogKey(id ID) objectKey {
	return objectKey{"model-catalog", "system", "system", string(id)}
}

func modelMappingKey(model, workspace ID) objectKey {
	return objectKey{"model-mapping", string(model), "system", string(workspace)}
}

func modelGlobalDefaultKey() objectKey {
	return objectKey{"model-global-default", "system", "system", "global"}
}

func modelCatalogStageKey() objectKey {
	return objectKey{"model-catalog-stage", "system", "system", "v1"}
}

// StageModelCatalogMigration copies old workspace-local models to one encrypted
// catalog and explicit mappings in a single transaction. The stage is not
// active: old readers and writers continue to use their original objects.
// Activation must first compare SourceDigest to a fresh source snapshot.
func (s *Store) StageModelCatalogMigration(ctx context.Context, p Principal, selectedDefault ID) error {
	if !p.Workspace.Valid() || !p.User.ID.Valid() || p.EmbeddedApp != "" {
		return ErrUnauthenticated
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	defer tx.Rollback()
	if err := s.verifyManagedWorkspaceActor(ctx, tx, p); err != nil {
		return err
	}
	if err := requireInactiveModelCatalogTx(ctx, tx); err != nil {
		return err
	}
	snapshot, err := s.readModelMigrationSnapshot(ctx, tx)
	if err != nil {
		return err
	}
	if snapshot.preview.Models == 0 {
		return ErrNotFound
	} else if snapshot.preview.RequiresDefaultDecision {
		if _, exists := snapshot.models[selectedDefault]; !exists {
			return ErrConflict
		}
	} else if selectedDefault == "" {
		selectedDefault = snapshot.preview.GlobalDefaultCandidate
	} else if selectedDefault != snapshot.preview.GlobalDefaultCandidate {
		return ErrConflict
	}
	marker := stagedModelCatalog{snapshot.digest, selectedDefault, snapshot.preview.Models, snapshot.preview.Mappings}
	var existingBody []byte
	err = tx.QueryRowContext(ctx, `SELECT body FROM chatbi_objects WHERE kind='model-catalog-stage' AND scope='system' AND owner='system' AND id='v1'`).Scan(&existingBody)
	if err == nil {
		var existing stagedModelCatalog
		if s.decode(modelCatalogStageKey(), existingBody, &existing) != nil || existing != marker {
			return ErrConflict
		}
		return s.verifyStagedModelCatalog(ctx, tx, snapshot, selectedDefault)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ErrUnavailable
	}
	var occupied int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM chatbi_objects
		WHERE kind IN ('model-catalog','model-mapping','model-global-default')`).Scan(&occupied); err != nil {
		return ErrUnavailable
	}
	if occupied != 0 {
		return ErrConflict
	}
	for id, model := range snapshot.models {
		if err := s.insertModelMigrationObject(ctx, tx, modelCatalogKey(id), model); err != nil {
			return fmt.Errorf("stage catalog model: %w", err)
		}
		for workspace := range snapshot.mapping[id] {
			if err := s.insertModelMigrationObject(ctx, tx, modelMappingKey(id, workspace), struct {
				Model     ID `json:"model"`
				Workspace ID `json:"workspace"`
			}{id, workspace}); err != nil {
				return fmt.Errorf("stage catalog mapping: %w", err)
			}
		}
	}
	if selectedDefault != "" {
		if err := s.insertModelMigrationObject(ctx, tx, modelGlobalDefaultKey(), selectedDefault); err != nil {
			return fmt.Errorf("stage global default: %w", err)
		}
	}
	if err := s.insertModelMigrationObject(ctx, tx, modelCatalogStageKey(), marker); err != nil {
		return fmt.Errorf("stage marker: %w", err)
	}
	auditID, err := newID()
	if err != nil {
		return err
	}
	auditKey := objectKey{"management-audit", "system", "system", string(auditID)}
	if err := s.insertModelMigrationObject(ctx, tx, auditKey, map[string]any{
		"action": "model-catalog.stage", "actor": p.User.ID, "default": selectedDefault,
		"models": marker.Models, "mappings": marker.Mappings, "time": time.Now().UTC(),
	}); err != nil {
		return fmt.Errorf("stage audit: %w", err)
	}
	if tx.Commit() != nil {
		return ErrUnavailable
	}
	return nil
}

// ActivateModelCatalogMigration makes a staged, unchanged model snapshot live
// in one transaction. The active marker is written only with its audit; old
// workspace-local writers check that marker in their own write transaction.
func (s *Store) ActivateModelCatalogMigration(ctx context.Context, p Principal, expectedDigest string) error {
	if !p.Workspace.Valid() || !p.User.ID.Valid() || p.EmbeddedApp != "" || len(expectedDigest) != 64 {
		return ErrUnauthenticated
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	defer tx.Rollback()
	if err := s.verifyManagedWorkspaceActor(ctx, tx, p); err != nil {
		return err
	}
	if err := requireInactiveModelCatalogTx(ctx, tx); err != nil {
		return err
	}
	key := modelCatalogStageKey()
	var body []byte
	if err := tx.QueryRowContext(ctx, `SELECT body FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=?`,
		key.Kind, key.Scope, key.Owner, key.ID).Scan(&body); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return ErrUnavailable
	}
	var staged stagedModelCatalog
	if s.decode(key, body, &staged) != nil || staged.SourceDigest != expectedDigest || !staged.Default.Valid() ||
		staged.Models < 1 || staged.Mappings < 1 {
		return ErrConflict
	}
	snapshot, err := s.readModelMigrationSnapshot(ctx, tx)
	if err != nil {
		return err
	}
	if snapshot.digest != expectedDigest || snapshot.preview.Models != staged.Models ||
		snapshot.preview.Mappings != staged.Mappings {
		return ErrConflict
	}
	if _, exists := snapshot.models[staged.Default]; !exists {
		return ErrConflict
	}
	if err := s.verifyStagedModelCatalog(ctx, tx, snapshot, staged.Default); err != nil {
		return err
	}
	if err := s.insertModelMigrationObject(ctx, tx, modelCatalogActiveKey(),
		modelCatalogActivation{Version: 1, SourceDigest: staged.SourceDigest}); err != nil {
		return err
	}
	auditID, err := newID()
	if err != nil {
		return err
	}
	if err := s.insertModelMigrationObject(ctx, tx,
		objectKey{"management-audit", "system", "system", string(auditID)}, map[string]any{
			"action": "model-catalog.activate", "actor": p.User.ID, "default": staged.Default,
			"models": staged.Models, "mappings": staged.Mappings, "time": time.Now().UTC(),
		}); err != nil {
		return err
	}
	if tx.Commit() != nil {
		return ErrUnavailable
	}
	return nil
}

func (s *Store) insertModelMigrationObject(ctx context.Context, tx *sql.Tx, key objectKey, value any) error {
	body, err := s.seal(key, value)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO chatbi_objects(kind,scope,owner,id,revision,body)
		VALUES(?,?,?,?,1,?) ON CONFLICT DO NOTHING`, key.Kind, key.Scope, key.Owner, key.ID, body)
	if err != nil {
		return fmt.Errorf("insert control object %s: %w: %v", key.Kind, ErrUnavailable, err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return ErrConflict
	}
	return nil
}

func (s *Store) verifyStagedModelCatalog(ctx context.Context, tx *sql.Tx, snapshot modelMigrationSnapshot, selectedDefault ID) error {
	for id, expected := range snapshot.models {
		key := modelCatalogKey(id)
		var body []byte
		if tx.QueryRowContext(ctx, `SELECT body FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=?`,
			key.Kind, key.Scope, key.Owner, key.ID).Scan(&body) != nil {
			return ErrConflict
		}
		var actual ModelConfig
		if s.decode(key, body, &actual) != nil || !reflect.DeepEqual(actual, expected) {
			return ErrConflict
		}
		for workspace := range snapshot.mapping[id] {
			mappingKey := modelMappingKey(id, workspace)
			if tx.QueryRowContext(ctx, `SELECT body FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=?`,
				mappingKey.Kind, mappingKey.Scope, mappingKey.Owner, mappingKey.ID).Scan(&body) != nil {
				return ErrConflict
			}
			var mapping struct {
				Model     ID `json:"model"`
				Workspace ID `json:"workspace"`
			}
			if s.decode(mappingKey, body, &mapping) != nil || mapping.Model != id || mapping.Workspace != workspace {
				return ErrConflict
			}
		}
	}
	if selectedDefault != "" {
		var body []byte
		key := modelGlobalDefaultKey()
		if tx.QueryRowContext(ctx, `SELECT body FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=?`,
			key.Kind, key.Scope, key.Owner, key.ID).Scan(&body) != nil {
			return ErrConflict
		}
		var actual ID
		if s.decode(key, body, &actual) != nil || actual != selectedDefault {
			return ErrConflict
		}
	}
	var modelCount, mappingCount, defaultCount int
	if tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM chatbi_objects WHERE kind='model-catalog' AND scope='system' AND owner='system'`).Scan(&modelCount) != nil ||
		tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM chatbi_objects WHERE kind='model-mapping' AND owner='system'`).Scan(&mappingCount) != nil ||
		tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM chatbi_objects WHERE kind='model-global-default' AND scope='system' AND owner='system'`).Scan(&defaultCount) != nil {
		return ErrUnavailable
	}
	expectedDefaultCount := 0
	if selectedDefault != "" {
		expectedDefaultCount = 1
	}
	if modelCount != snapshot.preview.Models || mappingCount != snapshot.preview.Mappings || defaultCount != expectedDefaultCount {
		return ErrConflict
	}
	return nil
}
