package rules

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTokenBucketInvalidAdmissionNeverConsumesCapacity(t *testing.T) {
	limiter := mustLimiter(t, newFakeClock())
	for _, test := range []struct {
		name          string
		key           string
		qps           float64
		maxConcurrent int
	}{
		{name: "empty key", key: "", qps: 1, maxConcurrent: 1},
		{name: "spaced key", key: " agent ", qps: 1, maxConcurrent: 1},
		{name: "zero qps", key: "agent", qps: 0, maxConcurrent: 1},
		{name: "negative qps", key: "agent", qps: -1, maxConcurrent: 1},
		{name: "nan qps", key: "agent", qps: math.NaN(), maxConcurrent: 1},
		{name: "infinite qps", key: "agent", qps: math.Inf(1), maxConcurrent: 1},
		{name: "zero concurrency", key: "agent", qps: 1, maxConcurrent: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := limiter.Allow(test.key, test.qps, test.maxConcurrent)
			require.Error(t, err)
			require.False(t, result.Allowed)
		})
	}

	result, err := limiter.Allow("agent", 1, 1)
	require.NoError(t, err)
	require.True(t, result.Allowed)
	require.NoError(t, limiter.Release("agent"))
	// The only valid request spent the single token; invalid requests did not.
	result, err = limiter.Allow("agent", 1, 1)
	require.NoError(t, err)
	require.False(t, result.Allowed)
	require.Equal(t, RateLimitQPS, result.Reason)
	require.Error(t, limiter.Release("agent"))
}
