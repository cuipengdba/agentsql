package adminapi

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLoginAttemptLimiterBoundsAccountAndResetsAfterWindow(t *testing.T) {
	limiter := newLoginAttemptLimiter()
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	limiter.now = func() time.Time { return now }
	for range loginMaxAttempts {
		require.True(t, limiter.allow("tenant-1", "Alice"))
	}
	require.False(t, limiter.allow("tenant-1", "alice"), "case changes must not bypass the account limit")
	require.True(t, limiter.allow("tenant-1", "bob"))
	require.True(t, limiter.allow("tenant-2", "alice"))
	now = now.Add(loginWindow)
	require.True(t, limiter.allow("tenant-1", "alice"))
}

func TestSharedLoginAttemptDigestNormalizesScopeAndHidesNames(t *testing.T) {
	secret := []byte("shared-token-secret")
	first := loginAttemptDigest(secret, " Tenant-1 ", " Alice ")
	require.Equal(t, first, loginAttemptDigest(secret, "tenant-1", "alice"))
	require.NotEqual(t, first, loginAttemptDigest(secret, "tenant-2", "alice"))
	require.NotEqual(t, first, loginAttemptDigest(secret, "tenant-1", "bob"))
	require.NotEqual(t, first, loginAttemptDigest([]byte("rotated-token-secret"), "tenant-1", "alice"))
}
