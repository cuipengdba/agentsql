package redaction

import (
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestReconcileNormativeMatrixAndOrder(t *testing.T) {
	observed := testObserved()

	t.Run("2 active commitment mismatch is hard and secret free", func(t *testing.T) {
		marker := "S3-SECRET-MARKER"
		result, err := Reconcile(observed, []model.RedactionKeyVersion{{ID: "1", State: model.RedactionKeyStateActive, Commitment: marker}})
		require.ErrorIs(t, err, ErrActiveCommitmentMismatch)
		require.False(t, result.Ready)
		require.Contains(t, err.Error(), "version 1")
		require.NotContains(t, err.Error(), marker)
		require.NotContains(t, err.Error(), observed.Keys[0].Commitment)
	})

	t.Run("3 active not registered gates readiness", func(t *testing.T) {
		result, err := Reconcile(observed, nil)
		require.NoError(t, err)
		require.False(t, result.Ready)
		require.Equal(t, []int{3}, result.Unsatisfied)
		require.Equal(t, []int{3, 5}, detailNumbers(result.Warnings))
	})

	t.Run("4 other configured mismatch gates readiness", func(t *testing.T) {
		result, err := Reconcile(observed, []model.RedactionKeyVersion{
			{ID: "1", State: model.RedactionKeyStateActive, Commitment: strings.Repeat("a", 64), ConfigRevision: "rev-1"},
			{ID: "2", State: model.RedactionKeyStateStandby, Commitment: strings.Repeat("x", 64), ConfigRevision: "rev-1"},
		})
		require.NoError(t, err)
		require.False(t, result.Ready)
		require.Equal(t, []int{4}, result.Unsatisfied)
		require.Equal(t, []int{4}, detailNumbers(result.Warnings))
	})

	t.Run("5 standby absent only warns", func(t *testing.T) {
		result, err := Reconcile(observed, []model.RedactionKeyVersion{{ID: "1", State: model.RedactionKeyStateActive, Commitment: strings.Repeat("a", 64), ConfigRevision: "rev-1"}})
		require.NoError(t, err)
		require.True(t, result.Ready)
		require.Equal(t, []int{5}, detailNumbers(result.Warnings))
	})

	t.Run("6 extra registry id is information", func(t *testing.T) {
		result, err := Reconcile(observed, []model.RedactionKeyVersion{
			{ID: "1", State: model.RedactionKeyStateActive, Commitment: strings.Repeat("a", 64), ConfigRevision: "rev-1"},
			{ID: "2", State: model.RedactionKeyStateStandby, Commitment: strings.Repeat("b", 64), ConfigRevision: "rev-1"},
			{ID: "9", State: model.RedactionKeyStateRetired, Commitment: strings.Repeat("c", 64), ConfigRevision: "old"},
		})
		require.NoError(t, err)
		require.True(t, result.Ready)
		require.Empty(t, result.Warnings)
		require.Equal(t, []int{6}, detailNumbers(result.Information))
	})

	t.Run("7 revision mismatch only warns", func(t *testing.T) {
		result, err := Reconcile(observed, []model.RedactionKeyVersion{
			{ID: "1", State: model.RedactionKeyStateActive, Commitment: strings.Repeat("a", 64), ConfigRevision: "other"},
			{ID: "2", State: model.RedactionKeyStateStandby, Commitment: strings.Repeat("b", 64), ConfigRevision: "other"},
		})
		require.NoError(t, err)
		require.True(t, result.Ready)
		require.Equal(t, []int{7}, detailNumbers(result.Warnings))
	})

	t.Run("3 and 4 precede 5 6 7", func(t *testing.T) {
		result, err := Reconcile(observed, []model.RedactionKeyVersion{
			{ID: "1", State: model.RedactionKeyStateStandby, Commitment: strings.Repeat("a", 64), ConfigRevision: "other"},
			{ID: "2", State: model.RedactionKeyStateLegacy, Commitment: strings.Repeat("x", 64), ConfigRevision: "other"},
			{ID: "9", State: model.RedactionKeyStateRetired, Commitment: strings.Repeat("c", 64)},
		})
		require.NoError(t, err)
		require.Equal(t, []int{3, 4}, result.Unsatisfied)
		require.Equal(t, []int{3, 4}, detailNumbers(result.Warnings))
		require.Equal(t, []int{6}, detailNumbers(result.Information))
	})
}

func TestReconcileRejectsRetiredReuseAndRegistryIntegrity(t *testing.T) {
	observed := testObserved()
	_, err := Reconcile(observed, []model.RedactionKeyVersion{{ID: "2", State: model.RedactionKeyStateRetired, Commitment: strings.Repeat("b", 64)}})
	require.ErrorIs(t, err, ErrRetiredKeyIDReuse)

	_, err = Reconcile(observed, []model.RedactionKeyVersion{{ID: "not-canonical", State: model.RedactionKeyStateStandby}})
	require.ErrorIs(t, err, ErrInvalidRegistryState)
	_, err = Reconcile(observed, []model.RedactionKeyVersion{
		{ID: "8", State: model.RedactionKeyStateActive}, {ID: "9", State: model.RedactionKeyStateActive},
	})
	require.ErrorIs(t, err, ErrInvalidRegistryState)
}

func testObserved() Observed {
	return Observed{
		Status: "available", Mode: "manifest", ActiveVersion: 1, Revision: "rev-1",
		Keys: []Key{{ID: 1, Commitment: strings.Repeat("a", 64)}, {ID: 2, Commitment: strings.Repeat("b", 64)}},
	}
}

func detailNumbers(details []Detail) []int {
	numbers := make([]int, len(details))
	for index, detail := range details {
		numbers[index] = detail.Number
	}
	return numbers
}
