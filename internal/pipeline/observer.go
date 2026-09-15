package pipeline

// DecisionObserver receives bounded, non-sensitive final pipeline signals.
type DecisionObserver interface {
	ObserveDecision(decision, dialect, stmtType string)
	ObserveRuleHit(ruleID, decision, risk string)
	ObserveStage(stage string, latencyMS int64)
}

// WithObserver installs an optional observer. A nil observer disables metrics.
func WithObserver(observer DecisionObserver) Option {
	return func(options *pipelineOptions) error {
		if options == nil {
			return ErrInvalidOption
		}
		options.observer = observer
		return nil
	}
}
