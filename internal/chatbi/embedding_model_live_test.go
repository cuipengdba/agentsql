package chatbi

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type recordingPinnedEmbedding struct {
	*LocalEmbeddingModel
	calls []int
}

func (model *recordingPinnedEmbedding) Encode(ctx context.Context, texts []string) ([][]float32, error) {
	model.calls = append(model.calls, len(texts))
	return model.LocalEmbeddingModel.Encode(ctx, texts)
}

func (model *recordingPinnedEmbedding) persistentEmbeddingIdentity() string {
	return model.LocalEmbeddingModel.persistentEmbeddingIdentity()
}

// The real ONNX model is intentionally loaded twice so its process-local cache
// cannot explain a warm result from the encrypted persistent index.
func TestLocalEmbeddingModelPersistentSourceIndexAfterModelRestart(t *testing.T) {
	path := os.Getenv("AGENTSQL_LOCAL_EMBEDDING_MODEL")
	if path == "" {
		t.Skip("local embedding model is not configured")
	}
	ctx := context.Background()
	store := testStore(t)
	p := Principal{Workspace: "1", User: User{ID: "1"}}
	entries := make([]assistantCatalogEntry, 32)
	for index := range entries {
		id := ID(fmt.Sprintf("%d", index+10))
		entries[index] = assistantCatalogEntry{CatalogID: id, Datasource: id,
			Name: fmt.Sprintf("Source %02d", index), selectionText: fmt.Sprintf("public.orders_%02d revenue amount", index)}
	}
	entries[17].selectionText = "public.weather forecast temperature"
	coldLocal, err := NewLocalEmbeddingModel(path)
	if err != nil {
		t.Fatal(err)
	}
	cold := &recordingPinnedEmbedding{LocalEmbeddingModel: coldLocal}
	start := time.Now()
	coldSelected, err := selectAssistantCatalogSourcesIndexed(ctx, store, p, cold, "weather forecast", entries, 1)
	coldElapsed := time.Since(start)
	if closeErr := coldLocal.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil || len(coldSelected) != 1 || len(cold.calls) != 2 || cold.calls[0] != 1 || cold.calls[1] != 32 {
		t.Fatalf("cold local source index: selected=%v calls=%v err=%v", coldSelected, cold.calls, err)
	}
	warmLocal, err := NewLocalEmbeddingModel(path)
	if err != nil {
		t.Fatal(err)
	}
	defer warmLocal.Close()
	warm := &recordingPinnedEmbedding{LocalEmbeddingModel: warmLocal}
	start = time.Now()
	warmSelected, err := selectAssistantCatalogSourcesIndexed(ctx, store, p, warm, "weather forecast", entries, 1)
	warmElapsed := time.Since(start)
	if err != nil || len(warmSelected) != 1 || warmSelected[0].CatalogID != coldSelected[0].CatalogID ||
		len(warm.calls) != 1 || warm.calls[0] != 1 {
		t.Fatalf("model restart recomputed source vectors: selected=%v calls=%v err=%v", warmSelected, warm.calls, err)
	}
	t.Logf("32 authorized sources: cold=%s warm-after-model-restart=%s", coldElapsed, warmElapsed)
}

// Run locally with the pinned model asset; CI has no model weights.
func TestLocalEmbeddingModelMatchesPinnedSQLBotScores(t *testing.T) {
	path := os.Getenv("AGENTSQL_LOCAL_EMBEDDING_MODEL")
	if path == "" {
		t.Skip("local embedding model is not configured")
	}
	weights, err := os.Open(filepath.Join(path, "model.onnx"))
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, weights); err != nil {
		weights.Close()
		t.Fatal(err)
	}
	if err := weights.Close(); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", hash.Sum(nil)); got != "716d380a65efde09842642540749bc0535b05f2c66737fea1106731b8b0d7ffb" {
		t.Fatalf("unexpected local model digest: %s", got)
	}
	model, err := NewLocalEmbeddingModel(path)
	if err != nil {
		t.Fatal(err)
	}
	defer model.Close()
	vectors, err := model.Encode(context.Background(), []string{"营业额是多少", "销售收入是多少", "今天的天气如何"})
	if err != nil || len(vectors) != 3 {
		t.Fatalf("local vectors: %v", err)
	}
	for _, pair := range []struct {
		index int
		want  float64
	}{{1, 0.816024}, {2, 0.313403}} {
		var dot float64
		for index, value := range vectors[0] {
			dot += float64(value) * float64(vectors[pair.index][index])
		}
		if math.Abs(dot-pair.want) > 0.0005 {
			t.Fatalf("similarity 0/%d = %.6f; original = %.6f", pair.index, dot, pair.want)
		}
	}
	sources := []assistantCatalogEntry{
		{CatalogID: "81", Datasource: "10", Name: "销售收入"},
		{CatalogID: "82", Datasource: "11", Name: "天气预报"},
		{CatalogID: "83", Datasource: "12", Name: "库存管理"},
	}
	chosen, err := selectAssistantCatalogSources(context.Background(), model, "天气预报", sources, 1)
	if err != nil || len(chosen) != 1 || chosen[0].CatalogID != "82" {
		t.Fatalf("real local model selected unexpected datasource: %#v %v", chosen, err)
	}
	indexed, err := selectAssistantCatalogSourcesIndexed(context.Background(), testStore(t),
		Principal{Workspace: "1", User: User{ID: "1"}}, model, "天气预报", sources, 1)
	if err != nil || len(indexed) != 1 || indexed[0].CatalogID != "82" {
		t.Fatalf("real local model and persistent index selected unexpected datasource: %#v %v", indexed, err)
	}
	columns := []Column{
		{Schema: "public", Table: "orders", Name: "amount", DataType: "numeric"},
		{Schema: "public", Table: "meteo", Name: "temperature", DataType: "numeric"},
	}
	remarks := []RemarkHint{
		{Schema: "public", Table: "orders", Remark: "销售收入"},
		{Schema: "public", Table: "meteo", Remark: "天气预报"},
	}
	selected, _, err := selectModelTables(context.Background(), model, "天气预报", columns, remarks, 1, 1, false)
	if err != nil || len(selected) != 1 || selected[0].Table != "meteo" {
		t.Fatalf("real local model selected unexpected table: %#v %v", selected, err)
	}
}

func TestLocalEmbeddingModelConcurrentCacheAndClose(t *testing.T) {
	path := os.Getenv("AGENTSQL_LOCAL_EMBEDDING_MODEL")
	if path == "" {
		t.Skip("local embedding model is not configured")
	}
	model, err := NewLocalEmbeddingModel(path)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 8)
	for i := 0; i < cap(results); i++ {
		go func() {
			vectors, err := model.Encode(context.Background(), []string{"营业额是多少", "销售收入是多少"})
			if err == nil && len(vectors) != 2 {
				err = ErrUnavailable
			}
			results <- err
		}()
	}
	for i := 0; i < cap(results); i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if err := model.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := model.Encode(context.Background(), []string{"营业额是多少"}); err != ErrUnavailable {
		t.Fatalf("closed model accepted inference: %v", err)
	}
}

func TestLocalEmbeddingModelRejectsIncompleteOrChangedAssets(t *testing.T) {
	path := t.TempDir()
	if _, err := NewLocalEmbeddingModel(path); err != ErrUnavailable {
		t.Fatalf("empty model directory: %v", err)
	}
	if err := os.Mkdir(filepath.Join(path, "1_Pooling"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("changed model card"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLocalEmbeddingModel(path); err != ErrUnavailable {
		t.Fatalf("changed model asset: %v", err)
	}
	if err := os.Remove(filepath.Join(path, "README.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(path, "1_Pooling"), filepath.Join(path, "README.md")); err == nil {
		if _, err := NewLocalEmbeddingModel(path); err != ErrUnavailable {
			t.Fatalf("symlinked model asset: %v", err)
		}
	}
}
