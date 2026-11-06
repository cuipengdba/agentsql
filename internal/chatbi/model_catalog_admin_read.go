package chatbi

import (
	"context"
	"database/sql"
	"errors"
	"sort"
)

type catalogModelView struct {
	Model        ModelConfig
	Revision     int64
	Default      bool
	MappingCount int
}

func (s *Store) catalogDefaultTx(ctx context.Context, tx *sql.Tx) (ID, error) {
	key := modelGlobalDefaultKey()
	var body []byte
	err := tx.QueryRowContext(ctx, `SELECT body FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=?`,
		key.Kind, key.Scope, key.Owner, key.ID).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrModel
	}
	if err != nil {
		return "", ErrUnavailable
	}
	var id ID
	if s.decode(key, body, &id) != nil || !id.Valid() {
		return "", ErrUnavailable
	}
	return id, nil
}

func (s *Store) catalogAdminModel(ctx context.Context, p Principal, id ID) (catalogModelView, error) {
	if !p.Workspace.Valid() || !p.User.ID.Valid() || !id.Valid() || p.EmbeddedApp != "" {
		return catalogModelView{}, ErrUnauthenticated
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return catalogModelView{}, ErrUnavailable
	}
	defer tx.Rollback()
	if err := s.verifyManagedWorkspaceActor(ctx, tx, p); err != nil {
		return catalogModelView{}, err
	}
	if err := s.requireActiveModelCatalogTx(ctx, tx); err != nil {
		return catalogModelView{}, err
	}
	model, revision, _, err := s.catalogModelTx(ctx, tx, id)
	if err != nil {
		return catalogModelView{}, err
	}
	defaultID, err := s.catalogDefaultTx(ctx, tx)
	if err != nil {
		return catalogModelView{}, err
	}
	mappings, err := s.catalogMappingSetTx(ctx, tx, id)
	if err != nil {
		return catalogModelView{}, err
	}
	return catalogModelView{model, revision, id == defaultID, len(mappings)}, nil
}

func (s *Store) catalogAdminModels(ctx context.Context, p Principal) ([]catalogModelView, error) {
	if !p.Workspace.Valid() || !p.User.ID.Valid() || p.EmbeddedApp != "" {
		return nil, ErrUnauthenticated
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
	rows, err := tx.QueryContext(ctx, `SELECT id FROM chatbi_objects WHERE kind='model-catalog' AND scope='system' AND owner='system' ORDER BY id LIMIT 10001`)
	if err != nil {
		return nil, ErrUnavailable
	}
	ids := make([]ID, 0)
	for rows.Next() {
		var id ID
		if rows.Scan(&id) != nil || !id.Valid() || len(ids) >= 10000 {
			rows.Close()
			return nil, ErrUnavailable
		}
		ids = append(ids, id)
	}
	if rows.Err() != nil || rows.Close() != nil {
		return nil, ErrUnavailable
	}
	defaultID, err := s.catalogDefaultTx(ctx, tx)
	if len(ids) == 0 && errors.Is(err, ErrModel) {
		return []catalogModelView{}, nil
	}
	if err != nil {
		return nil, ErrUnavailable
	}
	result := make([]catalogModelView, 0, len(ids))
	defaultFound := false
	for _, id := range ids {
		model, revision, _, err := s.catalogModelTx(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		mappings, err := s.catalogMappingSetTx(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		isDefault := id == defaultID
		defaultFound = defaultFound || isDefault
		result = append(result, catalogModelView{model, revision, isDefault, len(mappings)})
	}
	if !defaultFound {
		return nil, ErrUnavailable
	}
	return result, nil
}

// catalogWorkspaceModels mirrors SQLBot's explicit by-workspace picker. Its
// global default is included only when that model has an explicit mapping.
func (s *Store) catalogWorkspaceModels(ctx context.Context, p Principal) ([]catalogModelView, error) {
	if !p.Workspace.Valid() || !p.User.ID.Valid() || p.EmbeddedApp != "" {
		return nil, ErrUnauthenticated
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer tx.Rollback()
	if err := s.verifyAPIKeyActor(ctx, tx, p); err != nil {
		return nil, err
	}
	if err := s.requireActiveModelCatalogTx(ctx, tx); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT scope,body FROM chatbi_objects
		WHERE kind='model-mapping' AND owner='system' AND id=? ORDER BY scope LIMIT 10001`, string(p.Workspace))
	if err != nil {
		return nil, ErrUnavailable
	}
	ids := make([]ID, 0)
	for rows.Next() {
		var id ID
		var body []byte
		if rows.Scan(&id, &body) != nil || !id.Valid() || len(ids) >= 10000 {
			rows.Close()
			return nil, ErrUnavailable
		}
		var mapping catalogModelMapping
		if s.decode(modelMappingKey(id, p.Workspace), body, &mapping) != nil ||
			mapping.Model != id || mapping.Workspace != p.Workspace {
			rows.Close()
			return nil, ErrUnavailable
		}
		ids = append(ids, id)
	}
	if rows.Err() != nil || rows.Close() != nil {
		return nil, ErrUnavailable
	}
	defaultID, err := s.catalogDefaultTx(ctx, tx)
	if len(ids) == 0 && errors.Is(err, ErrModel) {
		var count int
		if tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM chatbi_objects WHERE kind='model-catalog' AND scope='system' AND owner='system'`).Scan(&count) == nil && count == 0 {
			return []catalogModelView{}, nil
		}
	}
	if err != nil {
		return nil, err
	}
	result := make([]catalogModelView, 0, len(ids))
	for _, id := range ids {
		model, revision, _, err := s.catalogModelTx(ctx, tx, id)
		if err != nil {
			return nil, ErrUnavailable
		}
		result = append(result, catalogModelView{Model: model, Revision: revision, Default: id == defaultID})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Default != result[j].Default {
			return result[i].Default
		}
		return result[i].Model.ID < result[j].Model.ID
	})
	return result, nil
}
