package chatbi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestModelCatalogMigrationPreviewDetectsGlobalDefaultDecisionWithoutSecrets(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	empty, err := store.PreviewModelCatalogMigration(ctx)
	if err != nil || empty.Models != 0 || empty.Mappings != 0 || empty.GlobalDefaultCandidate != "" {
		t.Fatalf("empty preview: %+v %v", empty, err)
	}
	first := ModelConfig{ID: "20", Name: "first", Model: "fixture", Endpoint: "https://example.invalid/v1", APIKey: "secret-one"}
	second := ModelConfig{ID: "21", Name: "second", Model: "fixture", Endpoint: "https://example.invalid/v1", APIKey: "secret-two"}
	for _, item := range []struct {
		workspace ID
		model     ModelConfig
	}{{"1", first}, {"2", second}} {
		if err := store.put(ctx, objectKey{"model", string(item.workspace), "workspace", "default"}, item.model, 0); err != nil {
			t.Fatal(err)
		}
	}
	preview, err := store.PreviewModelCatalogMigration(ctx)
	if err != nil || preview.Models != 2 || preview.Mappings != 2 ||
		!preview.RequiresDefaultDecision || preview.GlobalDefaultCandidate != "" ||
		preview.WorkspaceDefaults["1"] != "20" || preview.WorkspaceDefaults["2"] != "21" {
		t.Fatalf("different defaults were silently conflated: %+v %v", preview, err)
	}
	raw, err := json.Marshal(preview)
	if err != nil || strings.Contains(string(raw), "secret-") || strings.Contains(string(raw), "example.invalid") {
		t.Fatalf("migration preview exposed model configuration: %s %v", raw, err)
	}
	key := objectKey{"model", "2", "workspace", "default"}
	var current ModelConfig
	revision, err := store.get(ctx, key, &current)
	if err != nil || store.put(ctx, key, first, revision) != nil {
		t.Fatal("could not prepare same-default case")
	}
	preview, err = store.PreviewModelCatalogMigration(ctx)
	if err != nil || preview.Models != 1 || preview.Mappings != 2 ||
		preview.RequiresDefaultDecision || preview.GlobalDefaultCandidate != "20" {
		t.Fatalf("same default was not recognized: %+v %v", preview, err)
	}
	conflicting := first
	conflicting.APIKey = "different-secret"
	revision, err = store.get(ctx, key, &current)
	if err != nil || store.put(ctx, key, conflicting, revision) != nil {
		t.Fatal("could not prepare duplicate-ID conflict")
	}
	if _, err := store.PreviewModelCatalogMigration(ctx); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate model ID with different credential was accepted: %v", err)
	}
}
