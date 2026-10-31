package chatbi

import (
	"context"
	"encoding/hex"
	"errors"
)

type modelCatalogActivation struct {
	Version      int    `json:"version"`
	SourceDigest string `json:"source_digest"`
}

func modelCatalogActiveKey() objectKey {
	return objectKey{"model-catalog-active", "system", "system", "v1"}
}

type modelLease struct {
	catalog         bool
	mapping         bool
	activeRevision  int64
	activeDigest    [32]byte
	defaultRevision int64
	defaultDigest   [32]byte
	mappingRevision int64
	mappingDigest   [32]byte
	modelRevision   int64
	modelDigest     [32]byte
}

func validModelCatalogActivation(value modelCatalogActivation) bool {
	if value.Version != 1 || len(value.SourceDigest) != 64 {
		return false
	}
	_, err := hex.DecodeString(value.SourceDigest)
	return err == nil
}

func (s *Store) modelCatalogIsActive(ctx context.Context) (bool, error) {
	var active modelCatalogActivation
	_, err := s.get(ctx, modelCatalogActiveKey(), &active)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil || !validModelCatalogActivation(active) {
		return false, ErrUnavailable
	}
	return true, nil
}

// modelForWorkspace reads the global default only after an atomic catalog
// activation. A staged catalog never changes legacy model selection. The
// default is available across workspaces, as in SQLBot Community v1.10.3;
// database and gateway permissions remain separate checks in Service.
func (s *Store) modelForWorkspace(ctx context.Context, workspace ID) (ModelConfig, modelLease, error) {
	return s.modelForWorkspaceSelection(ctx, workspace, nil)
}

// An explicit selection may use the global default or an active mapping for
// this workspace. The selection carries a lease through the entire question.
func (s *Store) modelForWorkspaceSelection(ctx context.Context, workspace ID, selected *ID) (ModelConfig, modelLease, error) {
	if !workspace.Valid() {
		return ModelConfig{}, modelLease{}, ErrModel
	}
	if selected != nil && !selected.Valid() {
		return ModelConfig{}, modelLease{}, ErrModel
	}
	var active modelCatalogActivation
	activeRevision, activeDigest, err := s.getWithDigest(ctx, modelCatalogActiveKey(), &active)
	if errors.Is(err, ErrNotFound) {
		var model ModelConfig
		revision, digest, err := s.getWithDigest(ctx, objectKey{"model", string(workspace), "workspace", "default"}, &model)
		if err != nil {
			return ModelConfig{}, modelLease{}, err
		}
		if validateModelConfig(model) != nil {
			return ModelConfig{}, modelLease{}, ErrUnavailable
		}
		if selected != nil && model.ID != *selected {
			return ModelConfig{}, modelLease{}, ErrModel
		}
		return model, modelLease{modelRevision: revision, modelDigest: digest}, nil
	}
	if err != nil {
		return ModelConfig{}, modelLease{}, err
	}
	if !validModelCatalogActivation(active) {
		return ModelConfig{}, modelLease{}, ErrUnavailable
	}
	var defaultID ID
	defaultRevision, defaultDigest, err := s.getWithDigest(ctx, modelGlobalDefaultKey(), &defaultID)
	if err != nil || !defaultID.Valid() {
		return ModelConfig{}, modelLease{}, ErrModel
	}
	modelID := defaultID
	lease := modelLease{catalog: true, activeRevision: activeRevision, activeDigest: activeDigest}
	if selected != nil {
		modelID = *selected
	}
	if modelID == defaultID {
		lease.defaultRevision, lease.defaultDigest = defaultRevision, defaultDigest
	} else {
		var mapping catalogModelMapping
		mappingRevision, mappingDigest, err := s.getWithDigest(ctx, modelMappingKey(modelID, workspace), &mapping)
		if err != nil || mapping.Model != modelID || mapping.Workspace != workspace {
			return ModelConfig{}, modelLease{}, ErrModel
		}
		lease.mapping, lease.mappingRevision, lease.mappingDigest = true, mappingRevision, mappingDigest
	}
	var model ModelConfig
	modelRevision, modelDigest, err := s.getWithDigest(ctx, modelCatalogKey(modelID), &model)
	if err != nil || validateModelConfig(model) != nil || model.ID != modelID {
		return ModelConfig{}, modelLease{}, ErrModel
	}
	lease.modelRevision, lease.modelDigest = modelRevision, modelDigest
	return model, lease, nil
}

// modelLeaseUnchanged denies a result after catalog cutover, default switch,
// model edit or key rotation. Revision plus ciphertext digest rejects deletion
// and recreation of an object at the same revision.
func (s *Store) modelLeaseUnchanged(ctx context.Context, workspace ID, model ModelConfig, lease modelLease) error {
	var active modelCatalogActivation
	activeRevision, activeDigest, err := s.getWithDigest(ctx, modelCatalogActiveKey(), &active)
	if !lease.catalog {
		if !errors.Is(err, ErrNotFound) {
			return ErrModel
		}
		var current ModelConfig
		revision, digest, err := s.getWithDigest(ctx, objectKey{"model", string(workspace), "workspace", "default"}, &current)
		if err != nil || revision != lease.modelRevision || digest != lease.modelDigest || current.ID != model.ID {
			return ErrModel
		}
		return nil
	}
	if err != nil || !validModelCatalogActivation(active) ||
		activeRevision != lease.activeRevision || activeDigest != lease.activeDigest {
		return ErrModel
	}
	if lease.mapping {
		var mapping catalogModelMapping
		mappingRevision, mappingDigest, err := s.getWithDigest(ctx, modelMappingKey(model.ID, workspace), &mapping)
		if err != nil || mapping.Model != model.ID || mapping.Workspace != workspace ||
			mappingRevision != lease.mappingRevision || mappingDigest != lease.mappingDigest {
			return ErrModel
		}
	} else {
		var defaultID ID
		defaultRevision, defaultDigest, err := s.getWithDigest(ctx, modelGlobalDefaultKey(), &defaultID)
		if err != nil || defaultID != model.ID || defaultRevision != lease.defaultRevision || defaultDigest != lease.defaultDigest {
			return ErrModel
		}
	}
	var current ModelConfig
	modelRevision, modelDigest, err := s.getWithDigest(ctx, modelCatalogKey(model.ID), &current)
	if err != nil || modelRevision != lease.modelRevision || modelDigest != lease.modelDigest || current.ID != model.ID {
		return ErrModel
	}
	return nil
}
