package chatbi

import (
	"math"
	"strconv"
)

// EmbeddingSettings contains controls for knowledge and table preselection.
// Datasource candidate preselection is implemented separately.
type EmbeddingSettings struct {
	StrategyVersion       int     `json:"strategy_version,omitempty"`
	Enabled               bool    `json:"enabled"`
	TerminologySimilarity float64 `json:"terminology_similarity"`
	TerminologyTopCount   int     `json:"terminology_top_count"`
	TrainingSimilarity    float64 `json:"data_training_similarity"`
	TrainingTopCount      int     `json:"data_training_top_count"`
	TableEnabled          bool    `json:"table_enabled"`
	TableCount            int     `json:"table_count"`
	TableAlpha            float64 `json:"table_alpha"`
	KeywordEnabled        bool    `json:"keyword_enabled"`
	DatasourceCount       int     `json:"datasource_count"`
}

func defaultEmbeddingSettings() EmbeddingSettings {
	return EmbeddingSettings{StrategyVersion: 1, Enabled: true, TerminologySimilarity: 0.4, TerminologyTopCount: 5,
		TrainingSimilarity: 0.4, TrainingTopCount: 5, TableEnabled: true, TableCount: 10, TableAlpha: 0.4, KeywordEnabled: true, DatasourceCount: 10}
}

func validEmbeddingSettings(value EmbeddingSettings) bool {
	return value.StrategyVersion == 1 && validSimilarity(value.TerminologySimilarity) && validSimilarity(value.TrainingSimilarity) &&
		value.TerminologyTopCount > 0 && value.TerminologyTopCount <= 100 &&
		value.TrainingTopCount > 0 && value.TrainingTopCount <= 100 &&
		value.TableCount > 0 && value.TableCount <= 100 && validSimilarity(value.TableAlpha) &&
		value.DatasourceCount > 0 && value.DatasourceCount <= 100
}

func upgradeEmbeddingSettings(value *EmbeddingSettings) {
	if value == nil {
		return
	}
	defaults := defaultEmbeddingSettings()
	if value.StrategyVersion == 0 {
		value.StrategyVersion = defaults.StrategyVersion
		value.TableEnabled = defaults.TableEnabled
		value.TableCount = defaults.TableCount
		value.TableAlpha = defaults.TableAlpha
		value.KeywordEnabled = defaults.KeywordEnabled
	}
	if value.StrategyVersion == defaults.StrategyVersion && value.DatasourceCount == 0 {
		value.DatasourceCount = defaults.DatasourceCount
	}
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
	case "embedding.table_enabled", "embedding.keyword_enabled":
		if raw != "true" && raw != "false" {
			return false
		}
		if key == "embedding.table_enabled" {
			settings.TableEnabled = raw == "true"
		} else {
			settings.KeywordEnabled = raw == "true"
		}
	case "embedding.table_alpha":
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil || !validSimilarity(value) {
			return false
		}
		settings.TableAlpha = value
	case "embedding.table_count", "embedding.datasource_count":
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 || value > 100 || strconv.Itoa(value) != raw {
			return false
		}
		if key == "embedding.table_count" {
			settings.TableCount = value
		} else {
			settings.DatasourceCount = value
		}
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
