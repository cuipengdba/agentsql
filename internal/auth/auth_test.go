package auth

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/stretchr/testify/require"
)

func TestAuthenticateValidKeyReturnsAgent(t *testing.T) {
	now := time.Date(2026, time.September, 9, 10, 0, 0, 0, time.UTC)
	rawKey := store.APIKeyPrefix + "valid"
	agent := model.Agent{
		ID:         "ag_valid",
		Name:       "Valid Agent",
		Status:     "active",
		APIKeyHash: store.HashAPIKey(rawKey),
		Level:      "readonly",
	}
	reader := &fakeAgentKeyReader{agent: agent}
	authenticator := NewAuthenticatorWithClock(reader, fixedClock{now: now})

	authenticated, err := authenticator.Authenticate(context.Background(), rawKey)

	require.NoError(t, err)
	require.Equal(t, agent, authenticated)
	require.Equal(t, 1, reader.calls)
	require.Equal(t, store.HashAPIKey(rawKey), reader.lastHash)
}

func TestAuthenticateInvalidCredentials(t *testing.T) {
	now := time.Date(2026, time.September, 9, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		rawKey    string
		reader    AgentKeyReader
		wantCalls int
	}{
		{name: "empty key", reader: &fakeAgentKeyReader{}, wantCalls: 0},
		{name: "wrong prefix", rawKey: "token_wrong", reader: &fakeAgentKeyReader{}, wantCalls: 0},
		{name: "missing key", rawKey: store.APIKeyPrefix + "missing", reader: &fakeAgentKeyReader{err: store.ErrAgentNotFound}, wantCalls: 1},
		{name: "repository failure", rawKey: store.APIKeyPrefix + "failure", reader: &fakeAgentKeyReader{err: errors.New("store unavailable")}, wantCalls: 1},
		{
			name:   "stored hash mismatch",
			rawKey: store.APIKeyPrefix + "mismatch",
			reader: &fakeAgentKeyReader{agent: model.Agent{
				ID:         "ag_mismatch",
				Status:     "active",
				APIKeyHash: strings.Repeat("0", 64),
			}},
			wantCalls: 1,
		},
		{
			name:   "stored hash wrong length",
			rawKey: store.APIKeyPrefix + "wrong-length",
			reader: &fakeAgentKeyReader{agent: model.Agent{
				ID:         "ag_wrong_length",
				Status:     "active",
				APIKeyHash: "short",
			}},
			wantCalls: 1,
		},
		{
			name:   "stored agent identity missing",
			rawKey: store.APIKeyPrefix + "missing-identity",
			reader: &fakeAgentKeyReader{agent: model.Agent{
				Status:     "active",
				APIKeyHash: store.HashAPIKey(store.APIKeyPrefix + "missing-identity"),
			}},
			wantCalls: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authenticator := NewAuthenticatorWithClock(test.reader, fixedClock{now: now})
			_, err := authenticator.Authenticate(context.Background(), test.rawKey)
			require.Error(t, err)
			require.True(t, errors.Is(err, ErrInvalidCredentials))
			fake, ok := test.reader.(*fakeAgentKeyReader)
			require.True(t, ok)
			require.Equal(t, test.wantCalls, fake.calls)
		})
	}
}

func TestAuthenticateRejectsNonActiveStatus(t *testing.T) {
	rawKey := store.APIKeyPrefix + "status"
	for _, status := range []string{"disabled", "", "pending", "ACTIVE"} {
		t.Run(status, func(t *testing.T) {
			reader := &fakeAgentKeyReader{agent: model.Agent{
				ID:         "ag_status",
				Status:     status,
				APIKeyHash: store.HashAPIKey(rawKey),
			}}
			authenticator := NewAuthenticatorWithClock(reader, fixedClock{
				now: time.Date(2026, time.September, 9, 10, 0, 0, 0, time.UTC),
			})

			_, err := authenticator.Authenticate(context.Background(), rawKey)
			require.Error(t, err)
			require.True(t, errors.Is(err, ErrAgentDisabled))
		})
	}
}

func TestAuthenticateExpiryBoundaries(t *testing.T) {
	now := time.Date(2026, time.September, 9, 10, 0, 0, 0, time.UTC)
	rawKey := store.APIKeyPrefix + "expiry"
	tests := []struct {
		name    string
		expires time.Time
		wantErr bool
	}{
		{name: "not expired", expires: now.Add(time.Nanosecond)},
		{name: "expired", expires: now.Add(-time.Nanosecond), wantErr: true},
		{name: "exactly expired", expires: now, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := &fakeAgentKeyReader{agent: model.Agent{
				ID:         "ag_expiry",
				Status:     "active",
				APIKeyHash: store.HashAPIKey(rawKey),
				ExpiresAt:  &test.expires,
			}}
			authenticator := NewAuthenticatorWithClock(reader, fixedClock{now: now})
			_, err := authenticator.Authenticate(context.Background(), rawKey)
			if test.wantErr {
				require.Error(t, err)
				require.True(t, errors.Is(err, ErrKeyExpired))
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestAuthenticateFailClosedDependencies(t *testing.T) {
	rawKey := store.APIKeyPrefix + "dependency"
	validAgent := model.Agent{
		ID:         "ag_dependency",
		Status:     "active",
		APIKeyHash: store.HashAPIKey(rawKey),
	}
	var typedNilReader *fakeAgentKeyReader
	var typedNilClock *fixedClock
	tests := []struct {
		name          string
		authenticator *Authenticator
		ctx           context.Context
	}{
		{name: "nil authenticator", ctx: context.Background()},
		{name: "nil context", authenticator: NewAuthenticator(&fakeAgentKeyReader{agent: validAgent})},
		{name: "nil reader", authenticator: NewAuthenticator(nil), ctx: context.Background()},
		{name: "typed nil reader", authenticator: NewAuthenticator(typedNilReader), ctx: context.Background()},
		{name: "nil clock", authenticator: NewAuthenticatorWithClock(&fakeAgentKeyReader{agent: validAgent}, nil), ctx: context.Background()},
		{name: "typed nil clock", authenticator: NewAuthenticatorWithClock(&fakeAgentKeyReader{agent: validAgent}, typedNilClock), ctx: context.Background()},
		{name: "zero clock", authenticator: NewAuthenticatorWithClock(&fakeAgentKeyReader{agent: validAgent}, fixedClock{}), ctx: context.Background()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.authenticator.Authenticate(test.ctx, rawKey)
			require.Error(t, err)
			require.True(t, errors.Is(err, ErrInvalidCredentials))
		})
	}
}

func TestAuthenticateKeyRotation(t *testing.T) {
	t.Setenv("AGENTSQL_SECRET", "0123456789abcdef0123456789abcdef")
	opened, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "agentsql.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, opened.Close())
	})
	oldKey, oldHash, err := store.GenerateAPIKey()
	require.NoError(t, err)
	agent, err := opened.Agents().Create(context.Background(), model.Agent{
		ID:         "ag_rotate",
		Name:       "Rotating Agent",
		Status:     "active",
		APIKeyHash: oldHash,
		Level:      "readonly",
	})
	require.NoError(t, err)
	authenticator := NewAuthenticatorWithClock(
		opened.Agents(),
		fixedClock{now: time.Date(2026, time.September, 9, 10, 0, 0, 0, time.UTC)},
	)
	_, err = authenticator.Authenticate(context.Background(), oldKey)
	require.NoError(t, err)

	newKey, newHash, err := store.GenerateAPIKey()
	require.NoError(t, err)
	agent.APIKeyHash = newHash
	_, err = opened.Agents().Update(context.Background(), agent)
	require.NoError(t, err)

	_, err = authenticator.Authenticate(context.Background(), oldKey)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrInvalidCredentials))
	authenticated, err := authenticator.Authenticate(context.Background(), newKey)
	require.NoError(t, err)
	require.Equal(t, agent.ID, authenticated.ID)
}

type fixedClock struct {
	now time.Time
}

func (clock fixedClock) Now() time.Time {
	return clock.now
}

type fakeAgentKeyReader struct {
	agent    model.Agent
	err      error
	calls    int
	lastHash string
}

func (reader *fakeAgentKeyReader) GetByAPIKeyHash(
	_ context.Context,
	hash string,
) (model.Agent, error) {
	reader.calls++
	reader.lastHash = hash
	if reader.err != nil {
		return model.Agent{}, reader.err
	}
	return reader.agent, nil
}

var _ AgentKeyReader = (*fakeAgentKeyReader)(nil)
