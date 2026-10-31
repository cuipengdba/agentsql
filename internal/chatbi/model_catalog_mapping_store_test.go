package chatbi

import (
	"context"
	"errors"
	"testing"
)

func TestActiveCatalogMappingsShareOneModelAndRevokeOldLease(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	admin := stageCatalogAdmin(t, store)
	model := ModelConfig{ID: "30", Name: "shared", Model: "fixture", Endpoint: "https://example.invalid/v1", APIKey: "secret"}
	activateModelCatalogFixture(t, store, model)
	if err := store.put(ctx, objectKey{"workspace", "system", "system", "2"},
		workspaceOption{ID: "2", Name: "Two"}, 0); err != nil {
		t.Fatal(err)
	}
	resolved, lease, err := store.modelForWorkspace(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	initial, revision, err := store.catalogModelMappings(ctx, admin, model.ID)
	if err != nil || len(initial) != 0 || revision != 1 {
		t.Fatalf("global default must start with no explicit mapping: %v rev=%d err=%v", initial, revision, err)
	}
	mapped, err := store.changeCatalogModelMappings(ctx, admin, model.ID, []ID{"2", "1"}, catalogMappingsAdd, revision)
	if err != nil || len(mapped) != 2 || mapped[0] != "1" || mapped[1] != "2" {
		t.Fatalf("cross-workspace add: %v %v", mapped, err)
	}
	if err := store.modelLeaseUnchanged(ctx, "1", resolved, lease); !errors.Is(err, ErrModel) {
		t.Fatalf("mapping change kept old model lease alive: %v", err)
	}
	current, revision, err := store.catalogModelMappings(ctx, admin, model.ID)
	if err != nil || len(current) != 2 || revision != 2 {
		t.Fatalf("mapping revision or list incorrect: %v rev=%d err=%v", current, revision, err)
	}
	if _, err := store.changeCatalogModelMappings(ctx, admin, model.ID, []ID{"2"}, catalogMappingsReplace, 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale mapping revision accepted: %v", err)
	}
	mapped, err = store.changeCatalogModelMappings(ctx, admin, model.ID, []ID{"2"}, catalogMappingsReplace, revision)
	if err != nil || len(mapped) != 1 || mapped[0] != "2" {
		t.Fatalf("replace did not remove workspace 1: %v %v", mapped, err)
	}
	_, revision, err = store.catalogModelMappings(ctx, admin, model.ID)
	if err != nil || revision != 3 {
		t.Fatalf("replace revision: %d %v", revision, err)
	}
	mapped, err = store.changeCatalogModelMappings(ctx, admin, model.ID, []ID{"2"}, catalogMappingsRemove, revision)
	if err != nil || len(mapped) != 0 {
		t.Fatalf("remove last explicit mapping: %v %v", mapped, err)
	}
	defaultModel, _, err := store.modelForWorkspace(ctx, "1")
	if err != nil || defaultModel.ID != model.ID {
		t.Fatalf("removing mappings deleted global default: %+v %v", defaultModel, err)
	}
	var auditCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chatbi_objects WHERE kind='management-audit'`).Scan(&auditCount); err != nil || auditCount != 3 {
		t.Fatalf("expected three mapping audits: %d %v", auditCount, err)
	}
}

func TestActiveCatalogMappingValidationAndAuditFailureRollBack(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	admin := stageCatalogAdmin(t, store)
	model := ModelConfig{ID: "30", Model: "fixture", Endpoint: "https://example.invalid/v1"}
	activateModelCatalogFixture(t, store, model)
	user := testUser(t, "41", "catalog-member")
	if err := store.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if _, err := store.changeCatalogModelMappings(ctx, admin, model.ID, []ID{"999"}, catalogMappingsAdd, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("phantom workspace accepted: %v", err)
	}
	if _, err := store.changeCatalogModelMappings(ctx, admin, model.ID, []ID{"1", "1"}, catalogMappingsAdd, 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate workspace accepted: %v", err)
	}
	if _, err := store.changeCatalogModelMappings(ctx, admin, "999", []ID{"1"}, catalogMappingsAdd, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown model accepted: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `CREATE TRIGGER deny_model_mapping_audit BEFORE INSERT ON chatbi_objects
		WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(ABORT, 'audit unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.changeCatalogModelMappings(ctx, admin, model.ID, []ID{"1"}, catalogMappingsAdd, 1); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("audit failure allowed mapping write: %v", err)
	}
	list, revision, err := store.catalogModelMappings(ctx, admin, model.ID)
	if err != nil || len(list) != 0 || revision != 1 {
		t.Fatalf("audit failure left partial mapping or revision: %v rev=%d %v", list, revision, err)
	}
	if _, err := store.changeCatalogModelMappings(ctx, Principal{User: user, Workspace: "1"}, model.ID,
		[]ID{"1"}, catalogMappingsAdd, 1); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("nonadministrator changed global mapping: %v", err)
	}
}

func TestActiveCatalogCanRemoveMappingAfterWorkspaceDeletion(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	admin := stageCatalogAdmin(t, store)
	model := ModelConfig{ID: "30", Model: "fixture", Endpoint: "https://example.invalid/v1"}
	activateModelCatalogFixture(t, store, model)
	key := objectKey{"workspace", "system", "system", "2"}
	if err := store.put(ctx, key, workspaceOption{ID: "2", Name: "Two"}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := store.changeCatalogModelMappings(ctx, admin, model.ID, []ID{"2"}, catalogMappingsAdd, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.delete(ctx, key, 1); err != nil {
		t.Fatal(err)
	}
	remaining, err := store.changeCatalogModelMappings(ctx, admin, model.ID, []ID{"2"}, catalogMappingsRemove, 2)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("stale mapping could not be revoked after workspace deletion: %v %v", remaining, err)
	}
}

func TestActiveCatalogRejectsLegacyModelWrites(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	admin := stageCatalogAdmin(t, store)
	local := ModelConfig{ID: "20", Name: "Local", Model: "fixture", Endpoint: "https://example.invalid/v1"}
	secondary := ModelConfig{ID: "21", Name: "Secondary", Model: "fixture", Endpoint: "https://example.invalid/v1"}
	if err := store.put(ctx, objectKey{"model", "1", "workspace", "default"}, local, 0); err != nil {
		t.Fatal(err)
	}
	if err := store.put(ctx, additionalModelKey("1", secondary.ID), secondary, 0); err != nil {
		t.Fatal(err)
	}
	activateModelCatalogFixture(t, store, ModelConfig{ID: "30", Model: "global", Endpoint: "https://example.invalid/v1"})
	update := WorkspaceModelUpdate{Revision: 1, Name: "Changed", Model: "fixture", Endpoint: "https://example.invalid/v1"}
	for name, write := range map[string]func() error{
		"create default": func() error {
			return store.CreateWorkspaceModel(ctx, admin, WorkspaceModelUpdate{Model: "fixture", Endpoint: "https://example.invalid/v1"})
		},
		"update default": func() error { return store.UpdateWorkspaceModel(ctx, admin, update) },
		"create secondary": func() error {
			return store.CreateAdditionalWorkspaceModel(ctx, admin, WorkspaceModelUpdate{Model: "fixture", Endpoint: "https://example.invalid/v1"})
		},
		"update secondary": func() error { return store.UpdateAdditionalWorkspaceModel(ctx, admin, secondary.ID, update) },
		"delete secondary": func() error { return store.DeleteAdditionalWorkspaceModel(ctx, admin, secondary.ID) },
		"switch default":   func() error { return store.SetDefaultWorkspaceModel(ctx, admin, secondary.ID) },
	} {
		if err := write(); !errors.Is(err, ErrConflict) {
			t.Errorf("%s changed the legacy model store after cutover: %v", name, err)
		}
	}
	var retained ModelConfig
	if _, err := store.get(ctx, objectKey{"model", "1", "workspace", "default"}, &retained); err != nil || retained.ID != local.ID {
		t.Fatalf("legacy default changed: %+v %v", retained, err)
	}
	if _, err := store.get(ctx, additionalModelKey("1", secondary.ID), &retained); err != nil || retained.ID != secondary.ID {
		t.Fatalf("legacy secondary changed: %+v %v", retained, err)
	}
}

func TestActiveCatalogDefaultSwitchRevokesLeaseAndAuditFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	admin := stageCatalogAdmin(t, store)
	first := ModelConfig{ID: "30", Model: "first", Endpoint: "https://example.invalid/v1"}
	second := ModelConfig{ID: "31", Model: "second", Endpoint: "https://example.invalid/v1"}
	activateModelCatalogFixture(t, store, first)
	if err := store.put(ctx, modelCatalogKey(second.ID), second, 0); err != nil {
		t.Fatal(err)
	}
	old, lease, err := store.modelForWorkspace(ctx, "2")
	if err != nil || old.ID != first.ID {
		t.Fatalf("initial global default: %+v %v", old, err)
	}
	if err := store.SetGlobalDefaultModel(ctx, admin, second.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.modelLeaseUnchanged(ctx, "2", old, lease); !errors.Is(err, ErrModel) {
		t.Fatalf("old request survived default switch: %v", err)
	}
	current, _, err := store.modelForWorkspace(ctx, "2")
	if err != nil || current.ID != second.ID {
		t.Fatalf("new default was not global: %+v %v", current, err)
	}
	if err := store.SetGlobalDefaultModel(ctx, admin, "999"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown model became default: %v", err)
	}
	member := testUser(t, "41", "member")
	if err := store.CreateUser(ctx, member); err != nil {
		t.Fatal(err)
	}
	if err := store.SetGlobalDefaultModel(ctx, Principal{User: member, Workspace: "1"}, first.ID); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("member changed global default: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `CREATE TRIGGER deny_default_audit BEFORE INSERT ON chatbi_objects
		WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(ABORT, 'audit unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.SetGlobalDefaultModel(ctx, admin, first.ID); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("audit failure allowed default switch: %v", err)
	}
	current, _, err = store.modelForWorkspace(ctx, "2")
	if err != nil || current.ID != second.ID {
		t.Fatalf("audit failure changed global default: %+v %v", current, err)
	}
}
