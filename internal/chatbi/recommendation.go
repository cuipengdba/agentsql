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
	config, _, err := service.recommendationSnapshot(ctx, p, datasource)
	return config, err
}

func (service *Service) recommendationSnapshot(ctx context.Context, p Principal, datasource ID) (RecommendationConfig, int64, error) {
	if _, err := service.binding(ctx, p, datasource); err != nil {
		return RecommendationConfig{}, 0, err
	}
	var config RecommendationConfig
	revision, err := service.Store.get(ctx, recommendationKey(p.Workspace, datasource), &config)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return RecommendationConfig{Workspace: p.Workspace, Datasource: datasource, Mode: 0, Questions: []RecommendedProblem{}}, 0, nil
		}
		return RecommendationConfig{}, 0, err
	}
	if config.Workspace != p.Workspace || config.Datasource != datasource || validateRecommendations(config) != nil {
		return RecommendationConfig{}, 0, ErrUnavailable
	}
	sort.Slice(config.Questions, func(i, j int) bool {
		if config.Questions[i].Sort == config.Questions[j].Sort {
			return config.Questions[i].ID < config.Questions[j].ID
		}
		return config.Questions[i].Sort < config.Questions[j].Sort
	})
	return config, revision, nil
}

// RecommendationSettings exposes the current revision only to a workspace
// administrator who also has a current personal binding for the datasource.
func (service *Service) RecommendationSettings(ctx context.Context, p Principal, datasource ID) (RecommendationConfig, int64, error) {
	if err := service.semanticAdmin(ctx, p); err != nil {
		return RecommendationConfig{}, 0, err
	}
	return service.recommendationSnapshot(ctx, p, datasource)
}

// SaveRecommendations uses compare-and-swap and a management audit in the
// same transaction. New IDs are assigned server-side; an existing ID may only
// be reused from the current datasource configuration.
func (service *Service) SaveRecommendations(ctx context.Context, p Principal, datasource ID, expected int64, questions []RecommendedProblem) (RecommendationConfig, int64, error) {
	if err := service.semanticAdmin(ctx, p); err != nil {
		return RecommendationConfig{}, 0, err
	}
	if expected < 0 || len(questions) > 20 {
		return RecommendationConfig{}, 0, ErrInvalidSemantic
	}
	before, revision, err := service.recommendationSnapshot(ctx, p, datasource)
	if err != nil {
		return RecommendationConfig{}, 0, err
	}
	if revision != expected {
		return RecommendationConfig{}, 0, ErrConflict
	}
	oldIDs := make(map[ID]bool, len(before.Questions))
	for _, question := range before.Questions {
		oldIDs[question.ID] = true
	}
	configured := make([]RecommendedProblem, 0, len(questions))
	for index, question := range questions {
		if question.ID == "" {
			question.ID, err = newID()
			if err != nil {
				return RecommendationConfig{}, 0, err
			}
		} else if !oldIDs[question.ID] {
			return RecommendationConfig{}, 0, ErrInvalidSemantic
		}
		question.Datasource = datasource
		question.Sort = index + 1
		question.Question = strings.TrimSpace(question.Question)
		configured = append(configured, question)
	}
	mode := 0
	if len(configured) != 0 {
		mode = 2
	}
	config := RecommendationConfig{Workspace: p.Workspace, Datasource: datasource, Mode: mode, Questions: configured}
	if validateRecommendations(config) != nil {
		return RecommendationConfig{}, 0, ErrInvalidSemantic
	}
	if err := service.Store.writeSemanticWithDetails(ctx, recommendationKey(p.Workspace, datasource), config, revision,
		"recommendations.save", map[string]any{"actor": p.User.ID, "count": len(configured)}); err != nil {
		return RecommendationConfig{}, 0, err
	}
	return config, revision + 1, nil
}
