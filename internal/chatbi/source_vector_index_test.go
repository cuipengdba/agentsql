package chatbi

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

type indexedFixtureEmbedding struct {
	identity string
	calls    [][]string
}

func (model *indexedFixtureEmbedding) persistentEmbeddingIdentity() string { return model.identity }

func (model *indexedFixtureEmbedding) Encode(ctx context.Context, texts []string) ([][]float32, error) {
	model.calls = append(model.calls, append([]string(nil), texts...))
	return catalogFixtureEmbedding{}.Encode(ctx, texts)
}

func TestAuthorizedSourceVectorsPersistAcrossRestartAndInvalidateOnChange(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "chat.db")
	secret := []byte(strings.Repeat("s", 32))
	store, err := OpenStore(ctx, path, secret)
	if err != nil {
		t.Fatal(err)
	}
	p := Principal{Workspace: "1", User: User{ID: "1"}}
	entries := []assistantCatalogEntry{
		{CatalogID: "81", Datasource: "10", Name: "Opaque A", selectionText: "public.orders (amount:int)"},
		{CatalogID: "82", Datasource: "11", Name: "Opaque B", selectionText: "public.weather (forecast:text)"},
	}
	firstModel := &indexedFixtureEmbedding{identity: "pinned-test-v1"}
	selected, err := selectAssistantCatalogSourcesIndexed(ctx, store, p, firstModel, "weather forecast", entries, 1)
	if err != nil || len(selected) != 1 || selected[0].CatalogID != "82" || len(firstModel.calls) != 2 {
		t.Fatalf("initial authorized ranking: selected=%v calls=%v err=%v", selected, firstModel.calls, err)
	}
	var ciphertext []byte
	if err := store.db.QueryRowContext(ctx, `SELECT body FROM chatbi_objects WHERE kind='source-vector-v1' LIMIT 1`).Scan(&ciphertext); err != nil ||
		bytes.Contains(ciphertext, []byte("public.orders")) || bytes.Contains(ciphertext, []byte("public.weather")) {
		t.Fatalf("source index was not encrypted: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(ctx, path, secret)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	warmModel := &indexedFixtureEmbedding{identity: "pinned-test-v1"}
	selected, err = selectAssistantCatalogSourcesIndexed(ctx, store, p, warmModel, "weather forecast", entries, 1)
	if err != nil || len(selected) != 1 || selected[0].CatalogID != "82" || len(warmModel.calls) != 1 || len(warmModel.calls[0]) != 1 {
		t.Fatalf("warm restart recomputed source vectors: selected=%v calls=%v err=%v", selected, warmModel.calls, err)
	}
	entries[1].selectionText = "public.inventory (quantity:int)"
	changedModel := &indexedFixtureEmbedding{identity: "pinned-test-v1"}
	selected, err = selectAssistantCatalogSourcesIndexed(ctx, store, p, changedModel, "weather forecast", entries, 1)
	if err != nil || len(selected) != 1 || selected[0].CatalogID != "81" || len(changedModel.calls) != 2 || len(changedModel.calls[1]) != 1 {
		t.Fatalf("changed authorized content reused stale vector: selected=%v calls=%v err=%v", selected, changedModel.calls, err)
	}
	otherUser := Principal{Workspace: "1", User: User{ID: "2"}}
	otherModel := &indexedFixtureEmbedding{identity: "pinned-test-v1"}
	_, err = selectAssistantCatalogSourcesIndexed(ctx, store, otherUser, otherModel, "weather forecast", entries, 1)
	if err != nil || len(otherModel.calls) != 2 || len(otherModel.calls[1]) != 2 {
		t.Fatalf("user scoped index was reused: calls=%v err=%v", otherModel.calls, err)
	}
	newModel := &indexedFixtureEmbedding{identity: "pinned-test-v2"}
	_, err = selectAssistantCatalogSourcesIndexed(ctx, store, p, newModel, "weather forecast", entries, 1)
	if err != nil || len(newModel.calls) != 2 || len(newModel.calls[1]) != 2 {
		t.Fatalf("new model reused old vectors: calls=%v err=%v", newModel.calls, err)
	}
}

func TestCorruptSourceVectorIndexPreservesFullAuthorizedSet(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	p := Principal{Workspace: "1", User: User{ID: "1"}}
	entries := []assistantCatalogEntry{{CatalogID: "81", Datasource: "10", Name: "Sales"},
		{CatalogID: "82", Datasource: "11", Name: "Weather"}}
	model := &indexedFixtureEmbedding{identity: "pinned-test-v1"}
	if _, err := selectAssistantCatalogSourcesIndexed(ctx, store, p, model, "weather", entries, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE chatbi_objects SET body=? WHERE kind='source-vector-v1'`, []byte("corrupt")); err != nil {
		t.Fatal(err)
	}
	selected, err := selectAssistantCatalogSourcesIndexed(ctx, store, p, model, "weather", entries, 1)
	if err != nil || len(selected) != len(entries) {
		t.Fatalf("corrupt index narrowed authorized candidates: %v %v", selected, err)
	}
}
