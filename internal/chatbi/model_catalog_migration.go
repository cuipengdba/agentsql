package chatbi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"reflect"
)

// ModelCatalogMigrationPreview contains no model endpoint or credential. It
// reports whether existing workspace-local defaults can name one global
// default without silently changing a workspace's model selection.
type ModelCatalogMigrationPreview struct {
	Models                  int       `json:"models"`
	Mappings                int       `json:"mappings"`
	WorkspaceDefaults       map[ID]ID `json:"workspace_defaults"`
	GlobalDefaultCandidate  ID        `json:"global_default_candidate,omitempty"`
	RequiresDefaultDecision bool      `json:"requires_default_decision"`
}

type modelMigrationSnapshot struct {
	preview ModelCatalogMigrationPreview
	models  map[ID]ModelConfig
	mapping map[ID]map[ID]struct{}
	digest  string
}

type modelMigrationQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (s *Store) readModelMigrationSnapshot(ctx context.Context, query modelMigrationQueryer) (modelMigrationSnapshot, error) {
	snapshot := modelMigrationSnapshot{
		preview: ModelCatalogMigrationPreview{WorkspaceDefaults: make(map[ID]ID)},
		models:  make(map[ID]ModelConfig),
		mapping: make(map[ID]map[ID]struct{}),
	}
	rows, err := query.QueryContext(ctx, `SELECT kind,scope,id,revision,body FROM chatbi_objects
		WHERE owner='workspace' AND ((kind='model' AND id='default') OR kind='model-secondary')
		ORDER BY kind,scope,id`)
	if err != nil {
		return modelMigrationSnapshot{}, ErrUnavailable
	}
	defer rows.Close()
	hash := sha256.New()
	for rows.Next() {
		var kind, scope, id string
		var revision int64
		var body []byte
		if rows.Scan(&kind, &scope, &id, &revision, &body) != nil || !ID(scope).Valid() || revision < 1 {
			return modelMigrationSnapshot{}, ErrUnavailable
		}
		if json.NewEncoder(hash).Encode(struct {
			Kind     string
			Scope    string
			ID       string
			Revision int64
			Body     []byte
		}{kind, scope, id, revision, body}) != nil {
			return modelMigrationSnapshot{}, ErrUnavailable
		}
		key := objectKey{kind, scope, "workspace", id}
		var model ModelConfig
		if s.decode(key, body, &model) != nil || validateModelConfig(model) != nil ||
			(kind == "model-secondary" && id != string(model.ID)) {
			return modelMigrationSnapshot{}, ErrUnavailable
		}
		if previous, exists := snapshot.models[model.ID]; exists && !reflect.DeepEqual(previous, model) {
			return modelMigrationSnapshot{}, ErrConflict
		}
		snapshot.models[model.ID] = model
		workspace := ID(scope)
		if snapshot.mapping[model.ID] == nil {
			snapshot.mapping[model.ID] = make(map[ID]struct{})
		}
		if _, exists := snapshot.mapping[model.ID][workspace]; exists {
			return modelMigrationSnapshot{}, ErrConflict
		}
		if snapshot.preview.Mappings >= 100000 || len(snapshot.mapping[model.ID]) >= 10000 {
			return modelMigrationSnapshot{}, ErrUnavailable
		}
		snapshot.mapping[model.ID][workspace] = struct{}{}
		snapshot.preview.Mappings++
		if kind == "model" {
			snapshot.preview.WorkspaceDefaults[workspace] = model.ID
			if snapshot.preview.GlobalDefaultCandidate == "" {
				snapshot.preview.GlobalDefaultCandidate = model.ID
			} else if snapshot.preview.GlobalDefaultCandidate != model.ID {
				snapshot.preview.GlobalDefaultCandidate = ""
				snapshot.preview.RequiresDefaultDecision = true
			}
		}
	}
	if rows.Err() != nil {
		return modelMigrationSnapshot{}, ErrUnavailable
	}
	snapshot.preview.Models = len(snapshot.models)
	for _, workspaces := range snapshot.mapping {
		for workspace := range workspaces {
			if _, hasDefault := snapshot.preview.WorkspaceDefaults[workspace]; !hasDefault {
				snapshot.preview.RequiresDefaultDecision = true
			}
		}
	}
	if snapshot.preview.Models > 0 && snapshot.preview.GlobalDefaultCandidate == "" {
		snapshot.preview.RequiresDefaultDecision = true
	}
	if snapshot.preview.RequiresDefaultDecision {
		snapshot.preview.GlobalDefaultCandidate = ""
	}
	snapshot.digest = hex.EncodeToString(hash.Sum(nil))
	return snapshot, nil
}

// PreviewModelCatalogMigration reads the existing encrypted workspace-local
// model objects without changing the control store.
func (s *Store) PreviewModelCatalogMigration(ctx context.Context) (ModelCatalogMigrationPreview, error) {
	snapshot, err := s.readModelMigrationSnapshot(ctx, s.db)
	return snapshot.preview, err
}
