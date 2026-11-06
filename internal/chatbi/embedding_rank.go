package chatbi

import (
	"context"
	"math"
	"sort"
)

type embeddingCandidate struct {
	index int
	score float64
}

// rankEmbeddingCandidates returns only candidates above the threshold. It
// receives caller-filtered knowledge; ranking cannot add authority or objects.
func rankEmbeddingCandidates(ctx context.Context, model EmbeddingModel, question string, candidates []string, threshold float64, top int) ([]int, error) {
	if model == nil || len(candidates) == 0 {
		return nil, nil
	}
	if len(candidates) > 500 || top < 1 || threshold < 0 || threshold > 1 || math.IsNaN(threshold) {
		return nil, ErrUnavailable
	}
	query, err := model.Encode(ctx, []string{question})
	if err != nil || len(query) != 1 || len(query[0]) != 768 {
		return nil, ErrUnavailable
	}
	scored := make([]embeddingCandidate, 0, len(candidates))
	for start := 0; start < len(candidates); start += 32 {
		end := min(start+32, len(candidates))
		vectors, err := model.Encode(ctx, candidates[start:end])
		if err != nil || len(vectors) != end-start {
			return nil, ErrUnavailable
		}
		for offset, vector := range vectors {
			if len(vector) != len(query[0]) {
				return nil, ErrUnavailable
			}
			var score float64
			for dimension, component := range vector {
				score += float64(component) * float64(query[0][dimension])
			}
			if math.IsNaN(score) || math.IsInf(score, 0) {
				return nil, ErrUnavailable
			}
			if score > threshold {
				scored = append(scored, embeddingCandidate{index: start + offset, score: score})
			}
		}
	}
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].score == scored[j].score {
			return scored[i].index < scored[j].index
		}
		return scored[i].score > scored[j].score
	})
	if len(scored) > top {
		scored = scored[:top]
	}
	indices := make([]int, len(scored))
	for i, item := range scored {
		indices[i] = item.index
	}
	return indices, nil
}
