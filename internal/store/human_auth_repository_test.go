package store

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestHumanAuthStateTransitionsAndReplayRejection(t *testing.T) {
	ctx := context.Background()
	opened, err := OpenWithSecret(ctx, filepath.Join(t.TempDir(), "auth.db"), []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, opened.Close()) })
	_, err = opened.RBAC().CreateTenant(ctx, model.Tenant{ID: "tenant_a", Name: "A", Status: "active"})
	require.NoError(t, err)
	_, err = opened.RBAC().CreateUser(ctx, model.User{ID: "user_a", TenantID: "tenant_a", Username: "alice", DisplayName: "Alice", PasswordHash: "!", Status: "active", AuthProvider: "local"})
	require.NoError(t, err)

	recovery := sha256.Sum256([]byte("recovery"))
	repository := opened.HumanAuth()
	require.NoError(t, repository.PutPendingMFA(ctx, MFARecord{TenantID: "tenant_a", UserID: "user_a", SecretCiphertext: "ciphertext"}, []string{fmt.Sprintf("%x", recovery[:])}))
	require.NoError(t, repository.EnableMFA(ctx, "tenant_a", "user_a", 100))
	require.Error(t, repository.AdvanceMFACounter(ctx, "tenant_a", "user_a", 100))
	require.NoError(t, repository.AdvanceMFACounter(ctx, "tenant_a", "user_a", 101))
	require.NoError(t, repository.ConsumeRecoveryCode(ctx, "tenant_a", "user_a", fmt.Sprintf("%x", recovery[:]), time.Now().UTC()))
	require.Error(t, repository.ConsumeRecoveryCode(ctx, "tenant_a", "user_a", fmt.Sprintf("%x", recovery[:]), time.Now().UTC()))

	now := time.Now().UTC()
	challengeHash := fmt.Sprintf("%064x", 1)
	require.NoError(t, repository.CreateLoginChallenge(ctx, LoginChallenge{Hash: challengeHash, TenantID: "tenant_a", UserID: "user_a", Username: "alice", ExpiresAt: now.Add(time.Minute)}))
	_, err = repository.ConsumeLoginChallenge(ctx, challengeHash, now)
	require.NoError(t, err)
	_, err = repository.ConsumeLoginChallenge(ctx, challengeHash, now)
	require.ErrorIs(t, err, ErrNotFound)

	stateHash := fmt.Sprintf("%064x", 2)
	require.NoError(t, repository.CreateOIDCRequest(ctx, OIDCAuthRequest{StateHash: stateHash, Nonce: "nonce", PKCEVerifier: "verifier", ReturnTo: "/", ExpiresAt: now.Add(time.Minute)}))
	_, err = repository.ConsumeOIDCRequest(ctx, stateHash, now)
	require.NoError(t, err)
	_, err = repository.ConsumeOIDCRequest(ctx, stateHash, now)
	require.ErrorIs(t, err, ErrNotFound)
}
