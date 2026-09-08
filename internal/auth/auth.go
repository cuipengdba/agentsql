package auth

import (
	"context"
	"crypto/hmac"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
)

var (
	// ErrInvalidCredentials hides whether an API key or Agent record exists.
	ErrInvalidCredentials = errors.New("invalid credentials")
	// ErrAgentDisabled indicates that a valid key belongs to a non-active Agent.
	ErrAgentDisabled = errors.New("agent is disabled")
	// ErrKeyExpired indicates that a valid API key has reached its expiry time.
	ErrKeyExpired = errors.New("API key is expired")
)

// AgentKeyReader is the minimum read dependency required for authentication.
type AgentKeyReader interface {
	GetByAPIKeyHash(ctx context.Context, hash string) (model.Agent, error)
}

// Clock provides deterministic expiry checks.
type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time {
	return time.Now()
}

// Authenticator validates extracted raw API keys against Agent records.
type Authenticator struct {
	reader AgentKeyReader
	clock  Clock
}

// NewAuthenticator constructs an Authenticator using the system clock.
func NewAuthenticator(reader AgentKeyReader) *Authenticator {
	return &Authenticator{reader: reader, clock: systemClock{}}
}

// NewAuthenticatorWithClock constructs an Authenticator with an injected clock.
func NewAuthenticatorWithClock(reader AgentKeyReader, clock Clock) *Authenticator {
	return &Authenticator{reader: reader, clock: clock}
}

// Authenticate returns an active, unexpired Agent only after constant-time hash
// verification. Repository failures are intentionally hidden as invalid credentials.
func (authenticator *Authenticator) Authenticate(
	ctx context.Context,
	rawKey string,
) (model.Agent, error) {
	if authenticator == nil || ctx == nil {
		return model.Agent{}, invalidCredentialsError()
	}
	if rawKey == "" || !strings.HasPrefix(rawKey, store.APIKeyPrefix) {
		return model.Agent{}, invalidCredentialsError()
	}
	if isNilInterface(authenticator.reader) || isNilInterface(authenticator.clock) {
		return model.Agent{}, invalidCredentialsError()
	}

	actual := store.HashAPIKey(rawKey)
	agent, err := authenticator.reader.GetByAPIKeyHash(ctx, actual)
	if err != nil {
		return model.Agent{}, invalidCredentialsError()
	}
	if !hmac.Equal([]byte(agent.APIKeyHash), []byte(actual)) {
		return model.Agent{}, invalidCredentialsError()
	}
	if strings.TrimSpace(agent.ID) == "" {
		return model.Agent{}, invalidCredentialsError()
	}
	if agent.Status != "active" {
		return model.Agent{}, fmt.Errorf("authenticate Agent %q: %w", agent.ID, ErrAgentDisabled)
	}
	now := authenticator.clock.Now()
	if now.IsZero() {
		return model.Agent{}, invalidCredentialsError()
	}
	if agent.ExpiresAt != nil && !now.Before(*agent.ExpiresAt) {
		return model.Agent{}, fmt.Errorf("authenticate Agent %q: %w", agent.ID, ErrKeyExpired)
	}
	return agent, nil
}

func invalidCredentialsError() error {
	return fmt.Errorf("authenticate API key: %w", ErrInvalidCredentials)
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

var _ AgentKeyReader = (*store.AgentRepository)(nil)
