package rules

import (
	"fmt"

	"github.com/cuipengdba/agentsql/internal/engine"
	"github.com/cuipengdba/agentsql/internal/model"
)

// BuiltinRuleOverrides returns one persisted, enabled override for every rule
// that the runtime can actually execute. The list is derived from the runtime
// constructors so callers cannot accidentally register database-only rules.
func BuiltinRuleOverrides() []model.Rule {
	runtimeRules := make([]engine.Rule, 0)
	runtimeRules = append(runtimeRules, NewGenericRules(nil)...)
	runtimeRules = append(runtimeRules, NewPostgresRules(nil)...)
	runtimeRules = append(runtimeRules, NewMysqlRules(nil)...)

	overrides := make([]model.Rule, 0, len(runtimeRules))
	for _, rule := range runtimeRules {
		overrides = append(overrides, model.Rule{
			ID:          rule.ID(),
			DBType:      string(rule.Dialect()),
			Title:       fmt.Sprintf("Built-in rule %s", rule.ID()),
			RiskLevel:   int(rule.Level()),
			PatternType: "ast",
			Definition:  "{}",
			Enabled:     true,
			Builtin:     true,
		})
	}
	return overrides
}
