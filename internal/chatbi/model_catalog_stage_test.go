package chatbi

import (
	"context"
	"errors"
	"testing"
)

func stageCatalogAdmin(t *testing.T, store *Store) Principal {
	t.Helper()
	user := testUser(t, "40", "catalog-admin")
	user.Admin = true
	if err := store.CreateUser(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	return Principal{User: user, Workspace: "1"}
}

func TestStageModelCatalogMigrationRequiresExplicitChoiceAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	admin := stageCatalogAdmin(t, store)
	first := ModelConfig{ID: "20", Name: "first", Model: "fixture", Endpoint: "https://example.invalid/v1", APIKey: "secret-one"}
	second := ModelConfig{ID: "21", Name: "second", Model: "fixture", Endpoint: "https://example.invalid/v1", APIKey: "secret-two"}
	for _, entry := range []struct {
		key   objectKey
		model ModelConfig
	}{
		{objectKey{"model", "1", "workspace", "default"}, first},
		{objectKey{"model", "2", "workspace", "default"}, second},
		{additionalModelKey("2", "20"), first},
	} {
		if err := store.put(ctx, entry.key, entry.model, 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.StageModelCatalogMigration(ctx, admin, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting old defaults accepted without a choice: %v", err)
	}
	var staged stagedModelCatalog
	if _, err := store.get(ctx, modelCatalogStageKey(), &staged); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failed choice left staged state: %v", err)
	}
	if err := store.StageModelCatalogMigration(ctx, admin, first.ID); err != nil {
		t.Fatal(err)
	}
	if stagedRevision, err := store.get(ctx, modelCatalogStageKey(), &staged); err != nil || stagedRevision != 1 ||
		staged.Default != first.ID || staged.Models != 2 || staged.Mappings != 3 || len(staged.SourceDigest) != 64 {
		t.Fatalf("staged marker: %+v rev=%d err=%v", staged, stagedRevision, err)
	}
	var global ID
	if _, err := store.get(ctx, modelGlobalDefaultKey(), &global); err != nil || global != first.ID {
		t.Fatalf("global default: %s %v", global, err)
	}
	for _, model := range []ModelConfig{first, second} {
		var got ModelConfig
		if _, err := store.get(ctx, modelCatalogKey(model.ID), &got); err != nil || got.APIKey != model.APIKey {
			t.Fatalf("catalog lost model credential: %+v %v", got, err)
		}
	}
	if err := store.StageModelCatalogMigration(ctx, admin, first.ID); err != nil {
		t.Fatalf("unchanged retry must be idempotent: %v", err)
	}
	var auditCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chatbi_objects WHERE kind='management-audit'`).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("retry wrote duplicate audit: %d %v", auditCount, err)
	}
	second.APIKey = "rotated-key"
	key := objectKey{"model", "2", "workspace", "default"}
	var old ModelConfig
	revision, err := store.get(ctx, key, &old)
	if err != nil || store.put(ctx, key, second, revision) != nil {
		t.Fatal("could not rotate old local model")
	}
	if err := store.StageModelCatalogMigration(ctx, admin, first.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale stage accepted changed source: %v", err)
	}
}

func TestStageModelCatalogMigrationAuditFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	admin := stageCatalogAdmin(t, store)
	model := ModelConfig{ID: "20", Name: "first", Model: "fixture", Endpoint: "https://example.invalid/v1", APIKey: "secret"}
	if err := store.put(ctx, objectKey{"model", "1", "workspace", "default"}, model, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `CREATE TRIGGER deny_catalog_audit BEFORE INSERT ON chatbi_objects
		WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(ABORT, 'audit unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.StageModelCatalogMigration(ctx, admin, ""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("audit failure did not stop staging: %v", err)
	}
	for _, key := range []objectKey{modelCatalogKey(model.ID), modelMappingKey(model.ID, "1"), modelGlobalDefaultKey(), modelCatalogStageKey()} {
		var raw any
		if _, err := store.get(ctx, key, &raw); !errors.Is(err, ErrNotFound) {
			t.Fatalf("partial staged object survived rollback: %+v %v", key, err)
		}
	}
}

func TestStageModelCatalogMigrationRejectsUnownedTargetState(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	admin := stageCatalogAdmin(t, store)
	model := ModelConfig{ID: "20", Name: "first", Model: "fixture", Endpoint: "https://example.invalid/v1"}
	if err := store.put(ctx, objectKey{"model", "1", "workspace", "default"}, model, 0); err != nil {
		t.Fatal(err)
	}
	if err := store.put(ctx, modelCatalogKey("99"), model, 0); err != nil {
		t.Fatal(err)
	}
	if err := store.StageModelCatalogMigration(ctx, admin, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("preexisting target state was silently mixed with source: %v", err)
	}
	var marker stagedModelCatalog
	if _, err := store.get(ctx, modelCatalogStageKey(), &marker); !errors.Is(err, ErrNotFound) {
		t.Fatalf("conflicting target left migration marker: %v", err)
	}
}
