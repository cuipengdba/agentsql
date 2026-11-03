package chatbi

import (
	"context"
	"math"
	"sort"
)

// selectAssistantCatalogSources ranks only currently registered, authorized
// catalog entries. Missing or invalid vectors preserve the full candidate set
// so the downstream model still makes an explicit, audited choice.
func selectAssistantCatalogSources(ctx context.Context, model EmbeddingModel, question string,
	entries []assistantCatalogEntry, limit int) ([]assistantCatalogEntry, error) {
	if model == nil || limit < 1 || len(entries) <= limit || len(entries) > 32 || len(question) > 4096 {
		return entries, nil
	}
	query, err := model.Encode(ctx, []string{question})
	if err != nil || len(query) != 1 || len(query[0]) != 768 {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return entries, nil
	}
	queryNorm, valid := embeddingVectorNorm(query[0])
	if !valid {
		return entries, nil
	}
	names := make([]string, len(entries))
	for index, entry := range entries {
		if entry.selectionIncomplete {
			return entries, nil
		}
		names[index] = entry.Name
		if entry.selectionText != "" {
			names[index] = entry.selectionText
		}
	}
	vectors, err := model.Encode(ctx, names)
	if err != nil || len(vectors) != len(entries) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return entries, nil
	}
	type rankedSource struct {
		entry assistantCatalogEntry
		score float64
	}
	ranked := make([]rankedSource, len(entries))
	for index, vector := range vectors {
		norm, valid := embeddingVectorNorm(vector)
		if !valid {
			return entries, nil
		}
		var dot float64
		for dimension, value := range vector {
			dot += float64(value) * float64(query[0][dimension])
		}
		score := dot / (norm * queryNorm)
		if math.IsNaN(score) || math.IsInf(score, 0) {
			return entries, nil
		}
		ranked[index] = rankedSource{entry: entries[index], score: score}
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
	selected := make([]assistantCatalogEntry, limit)
	for index := range selected {
		selected[index] = ranked[index].entry
	}
	return selected, nil
}

func embeddingVectorNorm(vector []float32) (float64, bool) {
	if len(vector) != 768 {
		return 0, false
	}
	var sum float64
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return 0, false
		}
		sum += float64(value) * float64(value)
	}
	return math.Sqrt(sum), sum > 0 && !math.IsInf(sum, 0)
}
