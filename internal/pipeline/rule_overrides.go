package pipeline

import (
	"strings"

	"github.com/cuipengdba/agentsql/internal/engine"
	"github.com/cuipengdba/agentsql/internal/model"
)

func ruleOverridesToLayer(stored []model.Rule, dialect string) engine.RuleLayer {
	layer := make(engine.RuleLayer)
	for _, rule := range stored {
		id := strings.TrimSpace(rule.ID)
		if id == "" || id != rule.ID {
			continue
		}
		if rule.DBType != "all" && rule.DBType != dialect {
			continue
		}
		enabled := rule.Enabled
		layer[id] = engine.RuleConfig{Enabled: &enabled}
	}
	return layer
}

func mergeGlobalLayers(base engine.RuleLayers, extra engine.RuleLayer) engine.RuleLayers {
	global := cloneRuleLayer(base.Global)
	if global == nil {
		global = make(engine.RuleLayer)
	}
	for id, override := range extra {
		merged := cloneRuleConfig(global[id])
		if override.Enabled != nil {
			enabled := *override.Enabled
			merged.Enabled = &enabled
		}
		if len(override.Thresholds) > 0 {
			if merged.Thresholds == nil {
				merged.Thresholds = make(map[string]float64)
			}
			for name, value := range override.Thresholds {
				merged.Thresholds[name] = value
			}
		}
		merged.ExplicitDeny = merged.ExplicitDeny || override.ExplicitDeny
		global[id] = merged
	}
	return engine.RuleLayers{
		Global:     global,
		Datasource: cloneRuleLayer(base.Datasource),
		Agent:      cloneRuleLayer(base.Agent),
	}
}
