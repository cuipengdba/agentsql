package chatbi

import (
	"math"
	"strconv"
)

// EmbeddingSettings contains the SQLBot v1.10.3 controls currently wired to
// terminology and training retrieval. Table and datasource controls remain
// unsupported until those retrieval paths are implemented.
type EmbeddingSettings struct {
	Enabled               bool    `json:"enabled"`
	TerminologySimilarity float64 `json:"terminology_similarity"`
	TerminologyTopCount   int     `json:"terminology_top_count"`
	TrainingSimilarity    float64 `json:"data_training_similarity"`
	TrainingTopCount      int     `json:"data_training_top_count"`
}

func defaultEmbeddingSettings() EmbeddingSettings {
	return EmbeddingSettings{Enabled: true, TerminologySimilarity: 0.4, TerminologyTopCount: 5,
		TrainingSimilarity: 0.4, TrainingTopCount: 5}
}

func validEmbeddingSettings(value EmbeddingSettings) bool {
	return validSimilarity(value.TerminologySimilarity) && validSimilarity(value.TrainingSimilarity) &&
		value.TerminologyTopCount > 0 && value.TerminologyTopCount <= 100 &&
		value.TrainingTopCount > 0 && value.TrainingTopCount <= 100
}

func validSimilarity(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}

func applyEmbeddingSetting(settings *EmbeddingSettings, key, raw string) bool {
	switch key {
	case "embedding.enabled":
		if raw != "true" && raw != "false" {
			return false
		}
		settings.Enabled = raw == "true"
	case "embedding.terminology_similarity", "embedding.data_training_similarity":
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil || !validSimilarity(value) {
			return false
		}
		if key == "embedding.terminology_similarity" {
			settings.TerminologySimilarity = value
		} else {
			settings.TrainingSimilarity = value
		}
	case "embedding.terminology_top_count", "embedding.data_training_top_count":
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 || value > 100 || strconv.Itoa(value) != raw {
			return false
		}
		if key == "embedding.terminology_top_count" {
			settings.TerminologyTopCount = value
		} else {
			settings.TrainingTopCount = value
		}
	default:
		return false
	}
	return true
}
