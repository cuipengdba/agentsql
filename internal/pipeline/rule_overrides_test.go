package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/cuipengdba/agentsql/internal/engine"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestRuleOverridesToLayerFiltersAndOverwrites(t *testing.T) {
	layer := ruleOverridesToLayer([]model.Rule{
		{ID: "R001", DBType: "all", Enabled: false},
		{ID: "R101", DBType: "postgres", Enabled: false},
		{ID: "R203", DBType: "mysql", Enabled: false},
		{ID: "R001", DBType: "all", Enabled: true},
		{ID: "", DBType: "all", Enabled: false},
		{ID: " padded ", DBType: "all", Enabled: false},
	}, "postgres")
	require.Len(t, layer, 2)
	require.NotNil(t, layer["R001"].Enabled)
	require.True(t, *layer["R001"].Enabled)
	require.NotNil(t, layer["R101"].Enabled)
	require.False(t, *layer["R101"].Enabled)
	require.NotContains(t, layer, "R203")
	mysqlLayer := ruleOverridesToLayer([]model.Rule{
		{ID: "R001", DBType: "all", Enabled: false},
		{ID: "R101", DBType: "postgres", Enabled: false},
	}, "mysql")
	require.Contains(t, mysqlLayer, "R001")
	require.NotContains(t, mysqlLayer, "R101")
}

func TestPipelineRuleOverrideDisableAndRestoreTakesEffectPerRequest(t *testing.T) {
	fixture := newPipelineFixture(t)
	overrides := &fakeRuleOverrideReader{}
	fixture.ruleOverrides = overrides
	constructed, err := New(fixture.ports(), testPipelineSecret)
	require.NoError(t, err)
	fixture.pipeline = constructed
	request := requestWithSQL("UPDATE public.orders SET status = 'closed'")

	overrides.setEnabled("R002", "all", false)
	response, err := fixture.pipeline.Process(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, model.DecisionAllow, response.Decision)
	require.NotContains(t, staticHitIDs(response.Assessment.Hits), "R002")

	overrides.setEnabled("R002", "all", true)
	response, err = fixture.pipeline.Process(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, model.DecisionDeny, response.Decision)
	require.Contains(t, staticHitIDs(response.Assessment.Hits), "R002")
}

func TestPipelineRuleOverrideDialectIsolationAndNilRegression(t *testing.T) {
	t.Run("mysql override does not affect postgres", func(t *testing.T) {
		fixture := newPipelineFixture(t)
		fixture.ruleOverrides = &fakeRuleOverrideReader{rules: []model.Rule{{ID: "R002", DBType: "mysql", Enabled: false}}}
		constructed, err := New(fixture.ports(), testPipelineSecret)
		require.NoError(t, err)
		response, err := constructed.Process(context.Background(), requestWithSQL("UPDATE public.orders SET status = 'closed'"))
		require.NoError(t, err)
		require.Equal(t, model.DecisionDeny, response.Decision)
		require.Contains(t, staticHitIDs(response.Assessment.Hits), "R002")
	})
	t.Run("nil reader preserves default rules", func(t *testing.T) {
		fixture := newPipelineFixture(t)
		require.Nil(t, fixture.ruleOverrides)
		response, err := fixture.pipeline.Process(context.Background(), requestWithSQL("UPDATE public.orders SET status = 'closed'"))
		require.NoError(t, err)
		require.Equal(t, model.DecisionDeny, response.Decision)
		require.Contains(t, staticHitIDs(response.Assessment.Hits), "R002")
	})
}

func TestPipelineRuleOverrideReadFailureFailsClosedDuringLoad(t *testing.T) {
	fixture := newPipelineFixture(t)
	fixture.ruleOverrides = &fakeRuleOverrideReader{err: errors.New("override unavailable")}
	constructed, err := New(fixture.ports(), testPipelineSecret)
	require.NoError(t, err)
	_, err = constructed.Process(context.Background(), defaultRequest())
	require.ErrorContains(t, err, "override unavailable")
	require.Zero(t, fixture.executors.calls())
}

func TestRuleOverridesToLayerEmptyInputIsNonNil(t *testing.T) {
	require.NotNil(t, ruleOverridesToLayer(nil, "postgres"))
	require.Empty(t, ruleOverridesToLayer([]model.Rule{}, "mysql"))
}

func TestMergeGlobalLayersExtraWinsWithoutMutatingInputs(t *testing.T) {
	baseEnabled, extraEnabled, datasourceEnabled := true, false, true
	base := engine.RuleLayers{
		Global:     engine.RuleLayer{"R002": {Enabled: &baseEnabled, Thresholds: map[string]float64{"limit": 10}, ExplicitDeny: true}},
		Datasource: engine.RuleLayer{"R003": {Enabled: &datasourceEnabled}},
		Agent:      engine.RuleLayer{},
	}
	extra := engine.RuleLayer{"R002": {Enabled: &extraEnabled}}
	merged := mergeGlobalLayers(base, extra)

	require.False(t, *merged.Global["R002"].Enabled)
	require.Equal(t, float64(10), merged.Global["R002"].Thresholds["limit"])
	require.True(t, merged.Global["R002"].ExplicitDeny)
	require.True(t, *base.Global["R002"].Enabled)
	require.Equal(t, float64(10), base.Global["R002"].Thresholds["limit"])
	require.False(t, *extra["R002"].Enabled)
	*merged.Datasource["R003"].Enabled = false
	require.True(t, *base.Datasource["R003"].Enabled)
}
