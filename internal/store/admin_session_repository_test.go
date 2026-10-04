package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAdminSessionRotationReplayAndExpiry(t *testing.T) {
	opened := openTestStore(t)
	repository := opened.AdminSessions()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	session := AdminRefreshSession{
		FamilyID: strings.Repeat("a", 32), TokenHash: strings.Repeat("b", 64),
		TenantID: "tenant", UserID: "user", Username: "admin",
		CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour),
	}
	require.NoError(t, repository.CreateRefreshSession(ctx, session))
	valid, err := repository.ValidateAccessToken(ctx, "access-jti", session.FamilyID, now)
	require.NoError(t, err)
	require.True(t, valid)

	rotated, err := repository.RotateRefreshToken(ctx, session.TokenHash, strings.Repeat("c", 64), now.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, session.FamilyID, rotated.FamilyID)
	require.Equal(t, session.ExpiresAt, rotated.ExpiresAt)

	_, err = repository.RotateRefreshToken(ctx, session.TokenHash, strings.Repeat("d", 64), now.Add(2*time.Minute))
	require.ErrorIs(t, err, ErrAdminRefreshTokenReplay)
	valid, err = repository.ValidateAccessToken(ctx, "access-jti", session.FamilyID, now.Add(2*time.Minute))
	require.NoError(t, err)
	require.False(t, valid)
	_, err = repository.RotateRefreshToken(ctx, rotated.TokenHash, strings.Repeat("e", 64), now.Add(3*time.Minute))
	require.ErrorIs(t, err, ErrAdminRefreshTokenReplay)

	expiring := AdminRefreshSession{
		FamilyID: strings.Repeat("1", 32), TokenHash: strings.Repeat("2", 64),
		TenantID: "tenant", UserID: "user", Username: "admin",
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	require.NoError(t, repository.CreateRefreshSession(ctx, expiring))
	_, err = repository.RotateRefreshToken(ctx, expiring.TokenHash, strings.Repeat("3", 64), expiring.ExpiresAt)
	require.ErrorIs(t, err, ErrAdminRefreshTokenInvalid)
}

func TestAdminSessionRevocationPersistsAcrossReopen(t *testing.T) {
	ctx := context.Background()
	t.Setenv(secretEnvironmentVariable, testSecret)
	path := filepath.Join(t.TempDir(), "sessions.db")
	opened, err := Open(ctx, path)
	require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Microsecond)
	session := AdminRefreshSession{
		FamilyID: strings.Repeat("4", 32), TokenHash: strings.Repeat("5", 64),
		TenantID: "tenant", UserID: "user", Username: "admin",
		CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour),
	}
	require.NoError(t, opened.AdminSessions().CreateRefreshSession(ctx, session))
	require.NoError(t, opened.AdminSessions().RevokeAccessAndRefreshFamily(ctx, "persistent-jti", session.FamilyID, now.Add(time.Hour), now.Add(time.Minute)))
	require.NoError(t, opened.Close())

	reopened, err := Open(ctx, path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	valid, err := reopened.AdminSessions().ValidateAccessToken(ctx, "persistent-jti", session.FamilyID, now.Add(2*time.Minute))
	require.NoError(t, err)
	require.False(t, valid)
	_, err = reopened.AdminSessions().RotateRefreshToken(ctx, session.TokenHash, strings.Repeat("6", 64), now.Add(2*time.Minute))
	require.ErrorIs(t, err, ErrAdminRefreshTokenReplay)
}

func TestAdminSessionMalformedAndStoreFailureFailClosed(t *testing.T) {
	opened := openTestStore(t)
	repository := opened.AdminSessions()
	ctx := context.Background()
	now := time.Now().UTC()
	_, err := repository.RotateRefreshToken(ctx, "not-a-hash", strings.Repeat("a", 64), now)
	require.ErrorIs(t, err, ErrAdminRefreshTokenInvalid)
	require.Error(t, repository.CreateRefreshSession(ctx, AdminRefreshSession{}))
	require.NoError(t, opened.Close())
	valid, err := repository.ValidateAccessToken(ctx, "jti", "", now)
	require.False(t, valid)
	require.Error(t, err)
	_, err = repository.RotateRefreshToken(ctx, strings.Repeat("a", 64), strings.Repeat("b", 64), now)
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrAdminRefreshTokenInvalid), "database failures must remain distinguishable from malformed input")
}
