package chatbi

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	pathpkg "path"
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

func (*LocalEmbeddingModel) persistentEmbeddingIdentity() string {
	digest := sha256.Sum256(pinnedEmbeddingManifest)
	return fmt.Sprintf("hugot-go-normalized-v1:%x", digest)
}

// NewLocalEmbeddingModel loads the pinned SQLBot-compatible ONNX sentence model
// from disk. The offline installer must verify its signed archive separately.
// No model files are fetched at runtime.
func NewLocalEmbeddingModel(path string) (*LocalEmbeddingModel, error) {
	if err := VerifyPinnedEmbeddingModel(path); err != nil {
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

//go:embed pinned_embedding_model.json
var pinnedEmbeddingManifest []byte

// VerifyPinnedEmbeddingModel checks the fixed offline model without executing it.
// Installers can call this before and after staging model files.
func VerifyPinnedEmbeddingModel(path string) error {
	if !filepath.IsAbs(path) || strings.TrimSpace(path) != path {
		return ErrUnavailable
	}
	return verifyPinnedEmbeddingModel(path)
}

type pinnedEmbeddingFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func verifyPinnedEmbeddingModel(path string) error {
	var manifest struct {
		Files []pinnedEmbeddingFile `json:"files"`
	}
	if json.Unmarshal(pinnedEmbeddingManifest, &manifest) != nil || len(manifest.Files) != 9 {
		return ErrUnavailable
	}
	allowed := make(map[string]bool, len(manifest.Files))
	for _, pinned := range manifest.Files {
		if pinned.Path == "" || pathpkg.IsAbs(pinned.Path) || pathpkg.Clean(pinned.Path) != pinned.Path ||
			pinned.Path == ".." || strings.HasPrefix(pinned.Path, "../") ||
			strings.ContainsAny(pinned.Path, "\\:") || allowed[pinned.Path] || pinned.Size < 1 || len(pinned.SHA256) != 64 {
			return ErrUnavailable
		}
		allowed[pinned.Path] = true
	}
	for _, directory := range []string{path, filepath.Join(path, "1_Pooling")} {
		info, err := os.Lstat(directory)
		if err != nil || !info.IsDir() {
			return ErrUnavailable
		}
	}
	for _, directory := range []string{"", "1_Pooling"} {
		entries, err := os.ReadDir(filepath.Join(path, directory))
		if err != nil {
			return ErrUnavailable
		}
		for _, entry := range entries {
			if directory == "" && entry.Name() == "1_Pooling" && entry.IsDir() {
				continue
			}
			name := filepath.ToSlash(filepath.Join(directory, entry.Name()))
			if !allowed[name] || !entry.Type().IsRegular() {
				return ErrUnavailable
			}
		}
	}
	for _, pinned := range manifest.Files {
		filePath := filepath.Join(path, filepath.FromSlash(pinned.Path))
		info, err := os.Lstat(filePath)
		if err != nil || !info.Mode().IsRegular() || info.Size() != pinned.Size {
			return ErrUnavailable
		}
		file, err := os.Open(filePath)
		if err != nil {
			return ErrUnavailable
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil || fmt.Sprintf("%x", hash.Sum(nil)) != pinned.SHA256 {
			return ErrUnavailable
		}
	}
	return nil
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
