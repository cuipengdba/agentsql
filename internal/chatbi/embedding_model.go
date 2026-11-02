package chatbi

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/knights-analytics/hugot"
	"github.com/knights-analytics/hugot/pipelines"
)

// EmbeddingModel computes local, normalized sentence vectors. The caller
// selects authorized text first; a model never grants access to knowledge.
type EmbeddingModel interface {
	Encode(context.Context, []string) ([][]float32, error)
}

type LocalEmbeddingModel struct {
	mu       sync.Mutex
	session  *hugot.Session
	pipeline *pipelines.FeatureExtractionPipeline
	cache    map[string][]float32
}

// NewLocalEmbeddingModel loads an operator-provided ONNX sentence model from
// disk. The formal offline installer will verify the signed model asset before
// configuring this path. No model files are fetched at runtime.
func NewLocalEmbeddingModel(path string) (*LocalEmbeddingModel, error) {
	if !filepath.IsAbs(path) || strings.TrimSpace(path) != path {
		return nil, ErrUnavailable
	}
	if info, err := os.Stat(filepath.Join(path, "model.onnx")); err != nil || !info.Mode().IsRegular() {
		return nil, ErrUnavailable
	}
	session, err := hugot.NewGoSession()
	if err != nil {
		return nil, ErrUnavailable
	}
	pipeline, err := hugot.NewPipeline(session, hugot.FeatureExtractionConfig{
		ModelPath: path, Name: "agentsql-text2vec", OnnxFilename: "model.onnx",
		Options: []hugot.FeatureExtractionOption{pipelines.WithNormalization()},
	})
	if err != nil {
		_ = session.Destroy()
		return nil, ErrUnavailable
	}
	return &LocalEmbeddingModel{session: session, pipeline: pipeline, cache: make(map[string][]float32)}, nil
}

func (model *LocalEmbeddingModel) Encode(ctx context.Context, texts []string) ([][]float32, error) {
	if model == nil || len(texts) == 0 || len(texts) > 32 {
		return nil, ErrUnavailable
	}
	for _, value := range texts {
		if len(value) == 0 || len(value) > 4096 {
			return nil, ErrUnavailable
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	model.mu.Lock()
	defer model.mu.Unlock()
	if model.pipeline == nil {
		return nil, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := make([][]float32, len(texts))
	var missing []string
	var missingAt []int
	for index, value := range texts {
		if cached, found := model.cache[value]; found {
			result[index] = append([]float32(nil), cached...)
		} else {
			missing = append(missing, value)
			missingAt = append(missingAt, index)
		}
	}
	if len(missing) == 0 {
		return result, nil
	}
	output, err := model.pipeline.RunPipeline(missing)
	if err != nil || len(output.Embeddings) != len(missing) {
		return nil, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, vector := range output.Embeddings {
		if len(vector) != 768 {
			return nil, ErrUnavailable
		}
		var length float64
		for _, component := range vector {
			if math.IsNaN(float64(component)) || math.IsInf(float64(component), 0) {
				return nil, ErrUnavailable
			}
			length += float64(component) * float64(component)
		}
		if math.Abs(length-1) > 0.01 {
			return nil, ErrUnavailable
		}
	}
	if len(model.cache)+len(missing) > 1024 {
		model.cache = make(map[string][]float32)
	}
	for index, vector := range output.Embeddings {
		copyOfVector := append([]float32(nil), vector...)
		model.cache[missing[index]] = copyOfVector
		result[missingAt[index]] = append([]float32(nil), copyOfVector...)
	}
	return result, nil
}

func (model *LocalEmbeddingModel) Close() error {
	if model == nil {
		return nil
	}
	model.mu.Lock()
	defer model.mu.Unlock()
	if model.session == nil {
		return nil
	}
	err := model.session.Destroy()
	model.session = nil
	model.pipeline = nil
	model.cache = nil
	if err != nil {
		return errors.New("cannot close embedding model")
	}
	return nil
}
