package engine

import (
	"errors"

	"github.com/cuipengdba/agentsql/internal/model"
)

const (
	// DialectAll makes a rule applicable to both supported SQL dialects.
	DialectAll model.DBDialect = "all"
	guardStage                 = "guard"
)

var (
	// ErrInvalidInput indicates that an evaluation input is incomplete or invalid.
	ErrInvalidInput = errors.New("invalid engine input")
	// ErrInvalidRule indicates that a rule contract is incomplete or inconsistent.
	ErrInvalidRule = errors.New("invalid rule")
	// ErrInvalidRuleConfig indicates that layered rule configuration is invalid.
	ErrInvalidRuleConfig = errors.New("invalid rule configuration")
	// ErrRuleEvaluation indicates that a rule could not produce a trustworthy result.
	ErrRuleEvaluation = errors.New("rule evaluation failed")
)

// Rule evaluates one AST and returns an explainable four-state conclusion.
// Implementations must be deterministic and safe for concurrent calls.
type Rule interface {
	ID() string
	Dialect() model.DBDialect
	Level() model.RiskLevel
	Enabled() bool
	Eval(EvalContext) (RuleResult, error)
}

// RuleResult is one rule's four-state conclusion.
type RuleResult struct {
	Decision   model.Decision
	Message    string
	Suggestion string
}

// EvalContext contains the immutable inputs available to a rule evaluation.
// MetadataProvider intentionally remains an unconstrained slot until T06 defines
// its domain methods; the engine never invokes or mutates it.
type EvalContext struct {
	AST              *model.AST
	AgentLevel       string
	Agent            *model.Agent
	Datasource       *model.Datasource
	Policy           *model.PolicyDecision
	MetadataProvider any
	Thresholds       map[string]float64
}

// RuleConfig overrides one rule at a single configuration layer.
// ExplicitDeny is sticky across all later layers.
type RuleConfig struct {
	Enabled      *bool
	Thresholds   map[string]float64
	ExplicitDeny bool
}

// RuleLayer maps rule IDs to their settings at one ownership level.
type RuleLayer map[string]RuleConfig

// RuleLayers are merged from Global to Datasource to Agent. Callers must not
// mutate the maps while an evaluation is in progress.
type RuleLayers struct {
	Global     RuleLayer
	Datasource RuleLayer
	Agent      RuleLayer
}
