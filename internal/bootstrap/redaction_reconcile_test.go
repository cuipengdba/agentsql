package bootstrap

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/cuipengdba/agentsql/internal/metrics"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/redaction"
	"github.com/stretchr/testify/require"
)

func TestStartupReconciliationRegistryFailureIsHard(t *testing.T) {
	reader := &fakeRedactionRegistry{err: errors.New("registry sentinel")}
	state, err := newRedactionRuntime(context.Background(), bootstrapObserved(), reader)
	require.Nil(t, state)
	require.ErrorContains(t, err, "startup reconciliation")
	require.NotContains(t, err.Error(), strings.Repeat("a", 64))
}

func TestReadinessRecoversOnlyAfterNewStrongRound(t *testing.T) {
	reader := &fakeRedactionRegistry{}
	state, err := newRedactionRuntime(context.Background(), bootstrapObserved(), reader)
	require.NoError(t, err)
	runtime := &Runtime{redaction: state}

	ready, unmet := runtime.RedactionReady()
	require.False(t, ready)
	require.Equal(t, []int{3}, unmet)

	reader.set([]model.RedactionKeyVersion{
		{ID: "1", State: model.RedactionKeyStateActive, Commitment: strings.Repeat("a", 64), ConfigRevision: "rev-1"},
		{ID: "2", State: model.RedactionKeyStateStandby, Commitment: strings.Repeat("b", 64), ConfigRevision: "rev-1"},
	}, nil)
	ready, unmet = runtime.RedactionReady()
	require.False(t, ready, "registry mutation alone must not recover readiness")
	require.Equal(t, []int{3}, unmet)

	require.NoError(t, runtime.RerunRedactionReconciliation(context.Background()))
	ready, unmet = runtime.RedactionReady()
	require.True(t, ready)
	require.Empty(t, unmet)
}

func TestPeriodicReconciliationIsReadOnlyAndDefensive(t *testing.T) {
	reader := &fakeRedactionRegistry{versions: matchingBootstrapRegistry()}
	state, err := newRedactionRuntime(context.Background(), bootstrapObserved(), reader)
	require.NoError(t, err)
	hub := metrics.New(nil)
	state.attachMetrics(hub)
	runtime := &Runtime{redaction: state}

	reader.set(nil, errors.New("temporarily unavailable"))
	require.Error(t, runtime.RunPeriodicRedactionReconciliationOnce(context.Background()))
	ready, _ := runtime.RedactionReady()
	require.True(t, ready, "periodic read failure must keep the previous serving state")
	require.Equal(t, 2, reader.listCount())
	require.Equal(t, float64(1), redactionMetricValue(t, hub, "registry_unavailable"))

	reader.set([]model.RedactionKeyVersion{
		{ID: "1", State: model.RedactionKeyStateStandby, Commitment: strings.Repeat("a", 64), ConfigRevision: "rev-1"},
		{ID: "2", State: model.RedactionKeyStateActive, Commitment: strings.Repeat("b", 64), ConfigRevision: "rev-1"},
	}, nil)
	require.NoError(t, runtime.RunPeriodicRedactionReconciliationOnce(context.Background()))
	ready, unmet := runtime.RedactionReady()
	require.False(t, ready)
	require.Equal(t, []int{3}, unmet)
	require.Equal(t, 3, reader.listCount(), "periodic reconciliation has no write path")
}

func TestConcurrentReadinessSnapshotsAndReconciliation(t *testing.T) {
	reader := &fakeRedactionRegistry{versions: matchingBootstrapRegistry()}
	state, err := newRedactionRuntime(context.Background(), bootstrapObserved(), reader)
	require.NoError(t, err)
	runtime := &Runtime{redaction: state}

	var wait sync.WaitGroup
	for index := 0; index < 8; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for round := 0; round < 100; round++ {
				_, _ = runtime.RedactionReady()
				_, _ = runtime.RedactionReconciliation()
			}
		}()
	}
	for round := 0; round < 25; round++ {
		require.NoError(t, runtime.RerunRedactionReconciliation(context.Background()))
	}
	wait.Wait()
}

type fakeRedactionRegistry struct {
	mu       sync.Mutex
	versions []model.RedactionKeyVersion
	err      error
	lists    int
}

func (reader *fakeRedactionRegistry) List(context.Context) ([]model.RedactionKeyVersion, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	reader.lists++
	return append([]model.RedactionKeyVersion(nil), reader.versions...), reader.err
}

func (reader *fakeRedactionRegistry) set(versions []model.RedactionKeyVersion, err error) {
	reader.mu.Lock()
	reader.versions, reader.err = append([]model.RedactionKeyVersion(nil), versions...), err
	reader.mu.Unlock()
}

func (reader *fakeRedactionRegistry) listCount() int {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return reader.lists
}

func bootstrapObserved() redaction.Observed {
	return redaction.Observed{
		Status: "available", Mode: "manifest", ActiveVersion: 1, Revision: "rev-1",
		Keys: []redaction.Key{{ID: 1, Commitment: strings.Repeat("a", 64)}, {ID: 2, Commitment: strings.Repeat("b", 64)}},
	}
}

func matchingBootstrapRegistry() []model.RedactionKeyVersion {
	return []model.RedactionKeyVersion{
		{ID: "1", State: model.RedactionKeyStateActive, Commitment: strings.Repeat("a", 64), ConfigRevision: "rev-1"},
		{ID: "2", State: model.RedactionKeyStateStandby, Commitment: strings.Repeat("b", 64), ConfigRevision: "rev-1"},
	}
}

func redactionMetricValue(t *testing.T, hub *metrics.Metrics, kind string) float64 {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	recorder := httptest.NewRecorder()
	hub.Handler().ServeHTTP(recorder, request)
	needle := `redaction_key_drift{kind="` + kind + `"} `
	for _, line := range strings.Split(recorder.Body.String(), "\n") {
		if strings.HasPrefix(line, needle) {
			value, err := strconv.ParseFloat(strings.TrimPrefix(line, needle), 64)
			require.NoError(t, err)
			return value
		}
	}
	t.Fatalf("metric %s not found in %s", kind, recorder.Body.String())
	return 0
}
