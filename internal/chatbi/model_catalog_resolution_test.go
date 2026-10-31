package chatbi

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func activateModelCatalogFixture(t *testing.T, store *Store, model ModelConfig) {
	t.Helper()
	ctx := context.Background()
	if err := store.put(ctx, modelCatalogKey(model.ID), model, 0); err != nil {
		t.Fatal(err)
	}
	if err := store.put(ctx, modelGlobalDefaultKey(), model.ID, 0); err != nil {
		t.Fatal(err)
	}
	if err := store.put(ctx, modelCatalogActiveKey(), modelCatalogActivation{Version: 1, SourceDigest: strings.Repeat("a", 64)}, 0); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogGlobalDefaultCrossesWorkspacesButLeaseRevokesOldConfig(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	local := ModelConfig{ID: "20", Model: "local", Endpoint: "https://example.invalid/v1"}
	if err := store.put(ctx, objectKey{"model", "1", "workspace", "default"}, local, 0); err != nil {
		t.Fatal(err)
	}
	old, oldLease, err := store.modelForWorkspace(ctx, "1")
	if err != nil || old.ID != local.ID || store.modelLeaseUnchanged(ctx, "1", old, oldLease) != nil {
		t.Fatalf("legacy model lease: %+v %v", old, err)
	}
	global := ModelConfig{ID: "30", Model: "shared", Endpoint: "https://example.invalid/v1", APIKey: "secret-one"}
	activateModelCatalogFixture(t, store, global)
	if err := store.modelLeaseUnchanged(ctx, "1", old, oldLease); !errors.Is(err, ErrModel) {
		t.Fatalf("legacy request survived catalog cutover: %v", err)
	}
	// Workspace 2 has no local model or explicit mapping. The original
	// Community default remains available; data-source access is checked later.
	model, lease, err := store.modelForWorkspace(ctx, "2")
	if err != nil || model.ID != global.ID || model.APIKey != global.APIKey ||
		store.modelLeaseUnchanged(ctx, "2", model, lease) != nil {
		t.Fatalf("global default did not resolve in another workspace: %+v %v", model, err)
	}
	global.APIKey = "rotated-secret"
	revision, err := store.get(ctx, modelCatalogKey(global.ID), &model)
	if err != nil || store.put(ctx, modelCatalogKey(global.ID), global, revision) != nil {
		t.Fatal("could not rotate global model")
	}
	if err := store.modelLeaseUnchanged(ctx, "2", model, lease); !errors.Is(err, ErrModel) {
		t.Fatalf("old model result survived credential rotation: %v", err)
	}
	newModel, newLease, err := store.modelForWorkspace(ctx, "2")
	if err != nil || newModel.APIKey != "rotated-secret" {
		t.Fatalf("rotated global model unavailable: %+v %v", newModel, err)
	}
	other := ModelConfig{ID: "31", Model: "other", Endpoint: "https://example.invalid/v1"}
	if err := store.put(ctx, modelCatalogKey(other.ID), other, 0); err != nil {
		t.Fatal(err)
	}
	var previous ID
	revision, err = store.get(ctx, modelGlobalDefaultKey(), &previous)
	if err != nil || store.put(ctx, modelGlobalDefaultKey(), other.ID, revision) != nil {
		t.Fatal("could not switch global default")
	}
	if err := store.modelLeaseUnchanged(ctx, "2", newModel, newLease); !errors.Is(err, ErrModel) {
		t.Fatalf("old default result survived pointer switch: %v", err)
	}
}

func TestCatalogCredentialRotationDuringQuestionFailsClosed(t *testing.T) {
	ctx := context.Background()
	service, principal, gateway, model, token := serviceFixture(t)
	var local ModelConfig
	if _, err := service.Store.get(ctx, objectKey{"model", string(principal.Workspace), "workspace", "default"}, &local); err != nil {
		t.Fatal(err)
	}
	activateModelCatalogFixture(t, service.Store, local)
	chat, err := service.Start(ctx, principal, "10", "fixture", 0)
	if err != nil {
		t.Fatal(err)
	}
	model.after = func() {
		key := modelCatalogKey(local.ID)
		var current ModelConfig
		revision, err := service.Store.get(ctx, key, &current)
		if err != nil {
			t.Fatal(err)
		}
		current.APIKey = "rotated-secret"
		if err := service.Store.put(ctx, key, current, revision); err != nil {
			t.Fatal(err)
		}
	}
	check := func(ctx context.Context) error { _, err := service.Identity.Verify(ctx, token); return err }
	record, err := service.Ask(ctx, principal, chat.ID, "show amount", check, nil)
	if !errors.Is(err, ErrModel) || record.SQL != "" || record.Data != nil || gateway.queries != 0 {
		t.Fatalf("revoked global credential reached gateway or returned data: err=%v record=%+v queries=%d", err, record, gateway.queries)
	}
}
