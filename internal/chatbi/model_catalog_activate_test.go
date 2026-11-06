package chatbi

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestActivateStagedModelCatalogRevokesLegacyLeaseAndWrites(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	admin := stageCatalogAdmin(t, store)
	first := ModelConfig{ID: "20", Model: "first", Endpoint: "https://example.invalid/v1"}
	second := ModelConfig{ID: "21", Model: "second", Endpoint: "https://example.invalid/v1"}
	for _, item := range []struct {
		workspace ID
		model     ModelConfig
	}{{"1", first}, {"2", second}} {
		if err := store.put(ctx, objectKey{"model", string(item.workspace), "workspace", "default"}, item.model, 0); err != nil {
			t.Fatal(err)
		}
	}
	old, oldLease, err := store.modelForWorkspace(ctx, "2")
	if err != nil || old.ID != second.ID {
		t.Fatalf("old workspace default: %+v %v", old, err)
	}
	if err := store.StageModelCatalogMigration(ctx, admin, first.ID); err != nil {
		t.Fatal(err)
	}
	var stage stagedModelCatalog
	if _, err := store.get(ctx, modelCatalogStageKey(), &stage); err != nil {
		t.Fatal(err)
	}
	if err := store.ActivateModelCatalogMigration(ctx, admin, strings.Repeat("0", 64)); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong source digest activated catalog: %v", err)
	}
	viewer := testUser(t, "41", "catalog-viewer")
	if err := store.CreateUser(ctx, viewer); err != nil {
		t.Fatal(err)
	}
	if err := store.ActivateModelCatalogMigration(ctx, Principal{User: viewer, Workspace: "1"}, stage.SourceDigest); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("member activated catalog: %v", err)
	}
	if err := store.ActivateModelCatalogMigration(ctx, admin, stage.SourceDigest); err != nil {
		t.Fatal(err)
	}
	if active, err := store.modelCatalogIsActive(ctx); err != nil || !active {
		t.Fatalf("staged catalog was not activated: %t %v", active, err)
	}
	selected, lease, err := store.modelForWorkspace(ctx, "2")
	if err != nil || selected.ID != first.ID || store.modelLeaseUnchanged(ctx, "2", selected, lease) != nil {
		t.Fatalf("global default did not replace old workspace default: %+v %v", selected, err)
	}
	if err := store.modelLeaseUnchanged(ctx, "2", old, oldLease); !errors.Is(err, ErrModel) {
		t.Fatalf("old request survived cutover: %v", err)
	}
	selected, lease, err = store.modelForWorkspaceSelection(ctx, "2", &second.ID)
	if err != nil || selected.ID != second.ID || store.modelLeaseUnchanged(ctx, "2", selected, lease) != nil {
		t.Fatalf("staged mapping unavailable: %+v %v", selected, err)
	}
	if err := store.UpdateWorkspaceModel(ctx, admin, WorkspaceModelUpdate{Revision: 1, Model: "changed", Endpoint: "https://example.invalid/v1"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("legacy writer crossed active marker: %v", err)
	}
	if err := store.StageModelCatalogMigration(ctx, admin, first.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("catalog restaged after activation: %v", err)
	}
	if err := store.ActivateModelCatalogMigration(ctx, admin, stage.SourceDigest); !errors.Is(err, ErrConflict) {
		t.Fatalf("catalog activated twice: %v", err)
	}
}

func TestActivateModelCatalogRejectsStaleSourceAndAuditFailure(t *testing.T) {
	for _, mode := range []string{"source-changed", "target-changed", "audit-failed"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			store := testStore(t)
			admin := stageCatalogAdmin(t, store)
			model := ModelConfig{ID: "20", Model: "first", Endpoint: "https://example.invalid/v1"}
			localKey := objectKey{"model", "1", "workspace", "default"}
			if err := store.put(ctx, localKey, model, 0); err != nil {
				t.Fatal(err)
			}
			if err := store.StageModelCatalogMigration(ctx, admin, ""); err != nil {
				t.Fatal(err)
			}
			var stage stagedModelCatalog
			if _, err := store.get(ctx, modelCatalogStageKey(), &stage); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "source-changed":
				model.APIKey = "rotated"
				if err := store.put(ctx, localKey, model, 1); err != nil {
					t.Fatal(err)
				}
			case "target-changed":
				model.APIKey = "tampered"
				if err := store.put(ctx, modelCatalogKey(model.ID), model, 1); err != nil {
					t.Fatal(err)
				}
			case "audit-failed":
				if _, err := store.db.ExecContext(ctx, `CREATE TRIGGER deny_activate_audit BEFORE INSERT ON chatbi_objects
					WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(ABORT, 'audit unavailable'); END`); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.ActivateModelCatalogMigration(ctx, admin, stage.SourceDigest); !errors.Is(err, map[string]error{"source-changed": ErrConflict, "target-changed": ErrConflict, "audit-failed": ErrUnavailable}[mode]) {
				t.Fatalf("unsafe activation succeeded or returned wrong error: %v", err)
			}
			if active, err := store.modelCatalogIsActive(ctx); err != nil || active {
				t.Fatalf("failed activation left marker: %t %v", active, err)
			}
		})
	}
}
