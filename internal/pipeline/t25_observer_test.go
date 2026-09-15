package pipeline

import (
	"context"
	"sync"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestT25ObserverReceivesAllowAndDenyFinalSignals(t *testing.T) {
	tests := []struct {
		name         string
		request      Request
		decision     string
		statement    string
		ruleID       string
		ruleDecision string
		risk         string
	}{
		{name: "allow", request: defaultRequest(), decision: "allow", statement: "SELECT"},
		{
			name:         "deny",
			request:      requestWithSQL("UPDATE public.orders SET total = 1"),
			decision:     "deny",
			statement:    "UPDATE",
			ruleID:       "R002",
			ruleDecision: "deny",
			risk:         "1",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observer := &recordingDecisionObserver{}
			fixture := newPipelineFixture(t, WithObserver(observer))
			response, err := fixture.pipeline.Process(context.Background(), test.request)
			require.NoError(t, err)
			require.Equal(t, model.Decision(test.decision), response.Decision)

			decisions, hits, stages := observer.snapshot()
			require.Equal(t, []observedDecision{{
				decision: test.decision,
				dialect:  "postgres",
				stmtType: test.statement,
			}}, decisions)
			require.Len(t, stages, len(pipelineStageNames()))
			for _, stage := range pipelineStageNames() {
				_, exists := stages[stage]
				require.True(t, exists, stage)
			}
			if test.ruleID == "" {
				require.Empty(t, hits)
				return
			}
			require.Contains(t, hits, observedRuleHit{
				ruleID:   test.ruleID,
				decision: test.ruleDecision,
				risk:     test.risk,
			})
		})
	}
}

func TestT25ObserverPanicDoesNotChangePipelineResult(t *testing.T) {
	baselineFixture := newPipelineFixture(t)
	baseline, baselineErr := baselineFixture.pipeline.Process(context.Background(), defaultRequest())
	require.NoError(t, baselineErr)

	panickingFixture := newPipelineFixture(t, WithObserver(panickingDecisionObserver{}))
	observed, observedErr := panickingFixture.pipeline.Process(context.Background(), defaultRequest())
	require.NoError(t, observedErr)

	baseline.Assessment.StageLatency = nil
	observed.Assessment.StageLatency = nil
	require.Equal(t, baseline, observed)
}

func TestT25WithNilObserverPreservesBehavior(t *testing.T) {
	fixture := newPipelineFixture(t, WithObserver(nil))
	response, err := fixture.pipeline.Process(context.Background(), defaultRequest())
	require.NoError(t, err)
	require.Equal(t, model.DecisionAllow, response.Decision)
}

type observedDecision struct {
	decision string
	dialect  string
	stmtType string
}

type observedRuleHit struct {
	ruleID   string
	decision string
	risk     string
}

type recordingDecisionObserver struct {
	mu        sync.Mutex
	decisions []observedDecision
	hits      []observedRuleHit
	stages    map[string]int64
}

func (observer *recordingDecisionObserver) ObserveDecision(decision, dialect, stmtType string) {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observer.decisions = append(observer.decisions, observedDecision{
		decision: decision,
		dialect:  dialect,
		stmtType: stmtType,
	})
}

func (observer *recordingDecisionObserver) ObserveRuleHit(ruleID, decision, risk string) {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observer.hits = append(observer.hits, observedRuleHit{
		ruleID:   ruleID,
		decision: decision,
		risk:     risk,
	})
}

func (observer *recordingDecisionObserver) ObserveStage(stage string, latencyMS int64) {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if observer.stages == nil {
		observer.stages = make(map[string]int64)
	}
	observer.stages[stage] = latencyMS
}

func (observer *recordingDecisionObserver) snapshot() (
	[]observedDecision,
	[]observedRuleHit,
	map[string]int64,
) {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	decisions := append([]observedDecision(nil), observer.decisions...)
	hits := append([]observedRuleHit(nil), observer.hits...)
	stages := make(map[string]int64, len(observer.stages))
	for stage, latency := range observer.stages {
		stages[stage] = latency
	}
	return decisions, hits, stages
}

type panickingDecisionObserver struct{}

func (panickingDecisionObserver) ObserveDecision(string, string, string) { panic("observer failed") }
func (panickingDecisionObserver) ObserveRuleHit(string, string, string)  { panic("observer failed") }
func (panickingDecisionObserver) ObserveStage(string, int64)             { panic("observer failed") }

var _ DecisionObserver = (*recordingDecisionObserver)(nil)
var _ DecisionObserver = panickingDecisionObserver{}
