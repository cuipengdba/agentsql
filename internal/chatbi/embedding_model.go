package chatbi

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
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

// NewLocalEmbeddingModel loads the pinned SQLBot-compatible ONNX sentence model
// from disk. The offline installer must verify its signed archive separately.
// No model files are fetched at runtime.
func NewLocalEmbeddingModel(path string) (*LocalEmbeddingModel, error) {
	if !filepath.IsAbs(path) || strings.TrimSpace(path) != path {
		return nil, ErrUnavailable
	}
	if err := verifyPinnedEmbeddingModel(path); err != nil {
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

// These digests are from the pinned v1.10.3 original-image model directory.
// Check every file Hugot may read before opening the inference session.
var pinnedEmbeddingFiles = []struct {
	name   string
	size   int64
	digest string
}{
	{"README.md", 10046, "1bb57998c4abbc05b3c9af97539d0845d118e9cfe4e3d80cad94a7000a594e98"},
	{"config.json", 836, "31fedc61f7a714978f9d9849f4818ef905f216dc7791e469b9eceb9b32350b63"},
	{"modules.json", 230, "0580fad756940809b1bc064ce1e7c1275c8b0dd80869562fe0abeb6f7059cb97"},
	{"special_tokens_map.json", 125, "b6d346be366a7d1d48332dbc9fdf3bf8960b5d879522b7799ddba59e76237ee3"},
	{"tokenizer_config.json", 394, "e1790949631401af1bfb6c9c7aeec7fcf612e274d73579d99f704faea40c8ba7"},
	{"tokenizer.json", 439124, "7dfbf1966ebf99d471c3796e9b457329d2b2182b817e144f1e904b957745c839"},
	{"vocab.txt", 109540, "45bbac6b341c319adc98a532532882e91a9cefc0329aa57bac9ae761c27b291c"},
	{"1_Pooling/config.json", 74, "3307e674cc85cf6d18376a57f1958961be178e9e51e18e39e214285082e4892a"},
	{"model.onnx", 406953148, "716d380a65efde09842642540749bc0535b05f2c66737fea1106731b8b0d7ffb"},
}

func verifyPinnedEmbeddingModel(path string) error {
	for _, directory := range []string{path, filepath.Join(path, "1_Pooling")} {
		info, err := os.Lstat(directory)
		if err != nil || !info.IsDir() {
			return ErrUnavailable
		}
	}
	for _, pinned := range pinnedEmbeddingFiles {
		filePath := filepath.Join(path, pinned.name)
		info, err := os.Lstat(filePath)
		if err != nil || !info.Mode().IsRegular() || info.Size() != pinned.size {
			return ErrUnavailable
		}
		file, err := os.Open(filePath)
		if err != nil {
			return ErrUnavailable
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil || fmt.Sprintf("%x", hash.Sum(nil)) != pinned.digest {
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
