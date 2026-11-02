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
)

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
}
