package chatbi

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"time"
)

type catalogModelMapping struct {
	Model     ID `json:"model"`
	Workspace ID `json:"workspace"`
}

type catalogMappingChange string

const (
	catalogMappingsReplace catalogMappingChange = "replace"
	catalogMappingsAdd     catalogMappingChange = "add"
	catalogMappingsRemove  catalogMappingChange = "remove"
)

func (s *Store) requireActiveModelCatalogTx(ctx context.Context, tx *sql.Tx) error {
	key := modelCatalogActiveKey()
	var body []byte
	err := tx.QueryRowContext(ctx, `SELECT body FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=?`,
		key.Kind, key.Scope, key.Owner, key.ID).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return ErrUnavailable
	}
	var active modelCatalogActivation
	if s.decode(key, body, &active) != nil || !validModelCatalogActivation(active) {
		return ErrUnavailable
	}
	return nil
}

func (s *Store) catalogModelTx(ctx context.Context, tx *sql.Tx, id ID) (ModelConfig, int64, []byte, error) {
	key := modelCatalogKey(id)
	var body []byte
	var revision int64
	err := tx.QueryRowContext(ctx, `SELECT revision,body FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=?`,
		key.Kind, key.Scope, key.Owner, key.ID).Scan(&revision, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return ModelConfig{}, 0, nil, ErrNotFound
	}
	if err != nil {
		return ModelConfig{}, 0, nil, ErrUnavailable
	}
	var model ModelConfig
	if s.decode(key, body, &model) != nil || validateModelConfig(model) != nil || model.ID != id {
		return ModelConfig{}, 0, nil, ErrUnavailable
	}
	return model, revision, body, nil
}

func (s *Store) catalogMappingSetTx(ctx context.Context, tx *sql.Tx, model ID) (map[ID]struct{}, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,body FROM chatbi_objects
		WHERE kind='model-mapping' AND scope=? AND owner='system' ORDER BY id LIMIT 10001`, string(model))
	if err != nil {
		return nil, ErrUnavailable
	}
	defer rows.Close()
	result := make(map[ID]struct{})
	for rows.Next() {
		var workspace string
		var body []byte
		if rows.Scan(&workspace, &body) != nil || !ID(workspace).Valid() || len(result) >= 10000 {
			return nil, ErrUnavailable
		}
		var mapping catalogModelMapping
		if s.decode(modelMappingKey(model, ID(workspace)), body, &mapping) != nil ||
			mapping.Model != model || mapping.Workspace != ID(workspace) {
			return nil, ErrUnavailable
		}
		result[ID(workspace)] = struct{}{}
	}
	if rows.Err() != nil {
		return nil, ErrUnavailable
	}
	return result, nil
}

func sortedCatalogMappings(set map[ID]struct{}) []ID {
	result := make([]ID, 0, len(set))
	for id := range set {
		result = append(result, id)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

// catalogModelMappings reads the explicit mapping list. The global default
// remains available independently and need not appear in this list.
func (s *Store) catalogModelMappings(ctx context.Context, p Principal, id ID) ([]ID, int64, error) {
	if !p.Workspace.Valid() || !p.User.ID.Valid() || !id.Valid() || p.EmbeddedApp != "" {
		return nil, 0, ErrUnauthenticated
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, 0, ErrUnavailable
	}
	defer tx.Rollback()
	if err := s.verifyManagedWorkspaceActor(ctx, tx, p); err != nil {
		return nil, 0, err
	}
	if err := s.requireActiveModelCatalogTx(ctx, tx); err != nil {
		return nil, 0, err
	}
	_, revision, _, err := s.catalogModelTx(ctx, tx, id)
	if err != nil {
		return nil, 0, err
	}
	set, err := s.catalogMappingSetTx(ctx, tx, id)
	if err != nil {
		return nil, 0, err
	}
	return sortedCatalogMappings(set), revision, nil
}

func (s *Store) knownModelWorkspacesTx(ctx context.Context, tx *sql.Tx) (map[ID]struct{}, error) {
	rows, err := tx.QueryContext(ctx, `SELECT kind,id,body FROM chatbi_objects
		WHERE scope='system' AND owner='system' AND kind IN ('workspace','user')
		ORDER BY kind,id LIMIT 100001`)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer rows.Close()
	known := make(map[ID]struct{})
	count := 0
	for rows.Next() {
		var kind, id string
		var body []byte
		if rows.Scan(&kind, &id, &body) != nil || !ID(id).Valid() {
			return nil, ErrUnavailable
		}
		count++
		if count > 100000 {
			return nil, ErrUnavailable
		}
		if kind == "workspace" {
			var workspace workspaceOption
			if s.decode(objectKey{kind, "system", "system", id}, body, &workspace) != nil ||
				workspace.ID != ID(id) || !validWorkspaceName(workspace.Name) {
				return nil, ErrUnavailable
			}
			known[workspace.ID] = struct{}{}
		} else {
			var user User
			if s.decode(userKey(ID(id)), body, &user) != nil || user.ID != ID(id) {
				return nil, ErrUnavailable
			}
			for _, membership := range user.Memberships {
				if !membership.Workspace.Valid() {
					return nil, ErrUnavailable
				}
				known[membership.Workspace] = struct{}{}
			}
		}
	}
	if rows.Err() != nil {
		return nil, ErrUnavailable
	}
	return known, nil
}

// changeCatalogModelMappings serializes an active catalog model's mapping
// change with its revision and a durable audit. The model revision also
// invalidates in-flight leases. No database or gateway grant is changed.
func (s *Store) changeCatalogModelMappings(ctx context.Context, p Principal, id ID, requested []ID,
	mode catalogMappingChange, expectedRevision int64) ([]ID, error) {
	if !p.Workspace.Valid() || !p.User.ID.Valid() || !id.Valid() || p.EmbeddedApp != "" ||
		expectedRevision < 1 || len(requested) > 10000 ||
		(mode != catalogMappingsReplace && mode != catalogMappingsAdd && mode != catalogMappingsRemove) {
		return nil, ErrInvalidAccountChange
	}
	input := make(map[ID]struct{}, len(requested))
	for _, workspace := range requested {
		if !workspace.Valid() {
			return nil, ErrInvalidAccountChange
		}
		if _, duplicate := input[workspace]; duplicate {
			return nil, ErrConflict
		}
		input[workspace] = struct{}{}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer tx.Rollback()
	if err := s.verifyManagedWorkspaceActor(ctx, tx, p); err != nil {
		return nil, err
	}
	if err := s.requireActiveModelCatalogTx(ctx, tx); err != nil {
		return nil, err
	}
	_, revision, body, err := s.catalogModelTx(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if revision != expectedRevision {
		return nil, ErrConflict
	}
	current, err := s.catalogMappingSetTx(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if mode != catalogMappingsRemove {
		known, err := s.knownModelWorkspacesTx(ctx, tx)
		if err != nil {
			return nil, err
		}
		for workspace := range input {
			if _, exists := known[workspace]; !exists {
				return nil, ErrNotFound
			}
		}
	}
	desired := make(map[ID]struct{}, len(current)+len(input))
	if mode != catalogMappingsReplace {
		for workspace := range current {
			desired[workspace] = struct{}{}
		}
	}
	for workspace := range input {
		if mode == catalogMappingsRemove {
			delete(desired, workspace)
		} else {
			desired[workspace] = struct{}{}
		}
	}
	if len(desired) > 10000 {
		return nil, ErrConflict
	}
	changed := len(desired) != len(current)
	if !changed {
		for workspace := range desired {
			if _, exists := current[workspace]; !exists {
				changed = true
				break
			}
		}
	}
	if !changed {
		return sortedCatalogMappings(current), nil
	}
	for workspace := range current {
		if _, keep := desired[workspace]; keep {
			continue
		}
		key := modelMappingKey(id, workspace)
		result, err := tx.ExecContext(ctx, `DELETE FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=?`,
			key.Kind, key.Scope, key.Owner, key.ID)
		if err != nil {
			return nil, ErrUnavailable
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return nil, ErrConflict
		}
	}
	for workspace := range desired {
		if _, exists := current[workspace]; exists {
			continue
		}
		if err := s.insertModelMigrationObject(ctx, tx, modelMappingKey(id, workspace),
			catalogModelMapping{id, workspace}); err != nil {
			return nil, err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE chatbi_objects SET revision=revision+1 WHERE
		kind='model-catalog' AND scope='system' AND owner='system' AND id=? AND revision=? AND body=?`,
		string(id), revision, body)
	if err != nil {
		return nil, ErrUnavailable
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return nil, ErrConflict
	}
	auditID, err := newID()
	if err != nil {
		return nil, err
	}
	auditKey := objectKey{"management-audit", "system", "system", string(auditID)}
	if err := s.insertModelMigrationObject(ctx, tx, auditKey, map[string]any{
		"action": "model-catalog.mapping." + string(mode), "actor": p.User.ID, "model": id,
		"requested": requested, "result": sortedCatalogMappings(desired), "time": time.Now().UTC(),
	}); err != nil {
		return nil, err
	}
	if tx.Commit() != nil {
		return nil, ErrUnavailable
	}
	return sortedCatalogMappings(desired), nil
}
