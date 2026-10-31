package chatbi

import (
	"context"
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

// PreviewModelCatalogMigration reads the existing encrypted workspace-local
// model objects without changing the control store. The future catalog
// migration must reject duplicate IDs with different configurations and
// require an explicit choice if old workspaces had different defaults.
func (s *Store) PreviewModelCatalogMigration(ctx context.Context) (ModelCatalogMigrationPreview, error) {
	preview := ModelCatalogMigrationPreview{WorkspaceDefaults: make(map[ID]ID)}
	rows, err := s.db.QueryContext(ctx, `SELECT kind,scope,id,body FROM chatbi_objects
		WHERE owner='workspace' AND ((kind='model' AND id='default') OR kind='model-secondary')
		ORDER BY kind,scope,id`)
	if err != nil {
		return ModelCatalogMigrationPreview{}, ErrUnavailable
	}
	defer rows.Close()
	models := make(map[ID]ModelConfig)
	mappings := make(map[ID]map[ID]struct{})
	for rows.Next() {
		var kind, scope, id string
		var body []byte
		if rows.Scan(&kind, &scope, &id, &body) != nil || !ID(scope).Valid() {
			return ModelCatalogMigrationPreview{}, ErrUnavailable
		}
		key := objectKey{kind, scope, "workspace", id}
		var model ModelConfig
		if s.decode(key, body, &model) != nil || validateModelConfig(model) != nil ||
			(kind == "model-secondary" && id != string(model.ID)) {
			return ModelCatalogMigrationPreview{}, ErrUnavailable
		}
		if previous, exists := models[model.ID]; exists && !reflect.DeepEqual(previous, model) {
			return ModelCatalogMigrationPreview{}, ErrConflict
		}
		models[model.ID] = model
		workspace := ID(scope)
		if mappings[model.ID] == nil {
			mappings[model.ID] = make(map[ID]struct{})
		}
		if _, exists := mappings[model.ID][workspace]; exists {
			return ModelCatalogMigrationPreview{}, ErrConflict
		}
		mappings[model.ID][workspace] = struct{}{}
		preview.Mappings++
		if kind == "model" {
			preview.WorkspaceDefaults[workspace] = model.ID
			if preview.GlobalDefaultCandidate == "" {
				preview.GlobalDefaultCandidate = model.ID
			} else if preview.GlobalDefaultCandidate != model.ID {
				preview.GlobalDefaultCandidate = ""
				preview.RequiresDefaultDecision = true
			}
		}
	}
	if rows.Err() != nil {
		return ModelCatalogMigrationPreview{}, ErrUnavailable
	}
	preview.Models = len(models)
	if preview.RequiresDefaultDecision {
		preview.GlobalDefaultCandidate = ""
	}
	return preview, nil
}
