package chatbi

import (
	"context"
	"errors"
	"sort"
	"strings"
)

// Recommendations are operator-owned text suggestions. They never carry SQL
// execution authority, and reading them requires a current personal binding.
type RecommendedProblem struct {
	ID         ID     `json:"id"`
	Datasource ID     `json:"datasource_id"`
	Question   string `json:"question"`
	Remark     string `json:"remark"`
	Sort       int    `json:"sort"`
}

type RecommendationConfig struct {
	Workspace  ID                   `json:"workspace"`
	Datasource ID                   `json:"datasource_id"`
	Mode       int                  `json:"recommended_config"`
	Questions  []RecommendedProblem `json:"problemInfo"`
}

func recommendationKey(workspace, datasource ID) objectKey {
	return objectKey{"recommendations", string(workspace), "datasource", string(datasource)}
}

func validateRecommendations(config RecommendationConfig) error {
	if !config.Workspace.Valid() || !config.Datasource.Valid() || (config.Mode != 0 && config.Mode != 2) || len(config.Questions) > 20 || config.Mode == 0 && len(config.Questions) != 0 {
		return errors.New("invalid recommendation configuration")
	}
	seen := map[ID]bool{}
	for _, item := range config.Questions {
		if !item.ID.Valid() || seen[item.ID] || item.Datasource != config.Datasource || len(strings.TrimSpace(item.Question)) == 0 || len(item.Question) > 500 || len(item.Remark) > 1000 || item.Sort < 0 || item.Sort > 100000 {
			return errors.New("invalid recommended question")
		}
		seen[item.ID] = true
	}
	return nil
}

func (service *Service) Recommendations(ctx context.Context, p Principal, datasource ID) (RecommendationConfig, error) {
	if _, err := service.binding(ctx, p, datasource); err != nil {
		return RecommendationConfig{}, err
	}
	var config RecommendationConfig
	if _, err := service.Store.get(ctx, recommendationKey(p.Workspace, datasource), &config); err != nil {
		if errors.Is(err, ErrNotFound) {
			return RecommendationConfig{Workspace: p.Workspace, Datasource: datasource, Mode: 0, Questions: []RecommendedProblem{}}, nil
		}
		return RecommendationConfig{}, err
	}
	if config.Workspace != p.Workspace || config.Datasource != datasource || validateRecommendations(config) != nil {
		return RecommendationConfig{}, ErrUnavailable
	}
	sort.Slice(config.Questions, func(i, j int) bool {
		if config.Questions[i].Sort == config.Questions[j].Sort {
			return config.Questions[i].ID < config.Questions[j].ID
		}
		return config.Questions[i].Sort < config.Questions[j].Sort
	})
	return config, nil
}
