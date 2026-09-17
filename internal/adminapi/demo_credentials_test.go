package adminapi

import (
	"context"
	"errors"
	"testing"

	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

type fakeDemoAuthenticator struct {
	agents map[string]model.Agent
	errors map[string]error
	calls  []string
}

func (authenticator *fakeDemoAuthenticator) Authenticate(
	_ context.Context,
	rawKey string,
) (model.Agent, error) {
	authenticator.calls = append(authenticator.calls, rawKey)
	if err := authenticator.errors[rawKey]; err != nil {
		return model.Agent{}, err
	}
	agent, ok := authenticator.agents[rawKey]
	if !ok {
		return model.Agent{}, errors.New("invalid credentials containing " + rawKey)
	}
	return agent, nil
}

func TestValidateDemoProfileKeysFailFastMatrix(t *testing.T) {
	validRO := model.Agent{ID: config.DemoAgentRO, Level: "readonly", Status: "active"}
	validDML := model.Agent{ID: config.DemoAgentDML, Level: "dml", Status: "active"}
	tests := []struct {
		name      string
		env       map[string]string
		agents    map[string]model.Agent
		errors    map[string]error
		wantError string
	}{
		{
			name:      "missing readonly key",
			env:       map[string]string{demoDMLKeyEnvironment: demoDMLKeyCanary},
			wantError: demoROKeyEnvironment,
		},
		{
			name: "empty readonly key",
			env: map[string]string{
				demoROKeyEnvironment: "   ", demoDMLKeyEnvironment: demoDMLKeyCanary,
			},
			wantError: demoROKeyEnvironment,
		},
		{
			name:      "missing dml key",
			env:       map[string]string{demoROKeyEnvironment: demoROKeyCanary},
			agents:    map[string]model.Agent{demoROKeyCanary: validRO},
			wantError: demoDMLKeyEnvironment,
		},
		{
			name: "readonly authentication failure",
			env: map[string]string{
				demoROKeyEnvironment: demoROKeyCanary, demoDMLKeyEnvironment: demoDMLKeyCanary,
			},
			errors:    map[string]error{demoROKeyCanary: errors.New(demoDSNCanary)},
			wantError: "authentication failed",
		},
		{
			name: "readonly key bound to dml agent",
			env: map[string]string{
				demoROKeyEnvironment: demoDMLKeyCanary, demoDMLKeyEnvironment: demoROKeyCanary,
			},
			agents:    map[string]model.Agent{demoDMLKeyCanary: validDML, demoROKeyCanary: validRO},
			wantError: "must authenticate as active agent",
		},
		{
			name: "readonly level mismatch",
			env: map[string]string{
				demoROKeyEnvironment: demoROKeyCanary, demoDMLKeyEnvironment: demoDMLKeyCanary,
			},
			agents: map[string]model.Agent{
				demoROKeyCanary:  {ID: config.DemoAgentRO, Level: "dml", Status: "active"},
				demoDMLKeyCanary: validDML,
			},
			wantError: "must authenticate as active agent",
		},
		{
			name: "readonly status mismatch",
			env: map[string]string{
				demoROKeyEnvironment: demoROKeyCanary, demoDMLKeyEnvironment: demoDMLKeyCanary,
			},
			agents: map[string]model.Agent{
				demoROKeyCanary:  {ID: config.DemoAgentRO, Level: "readonly", Status: "disabled"},
				demoDMLKeyCanary: validDML,
			},
			wantError: "must authenticate as active agent",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authenticator := &fakeDemoAuthenticator{agents: test.agents, errors: test.errors}
			_, err := validateDemoProfileKeys(context.Background(), authenticator, mapLookup(test.env))
			require.ErrorContains(t, err, test.wantError)
			require.NotContains(t, err.Error(), demoROKeyCanary)
			require.NotContains(t, err.Error(), demoDMLKeyCanary)
			require.NotContains(t, err.Error(), "asql_")
			require.NotContains(t, err.Error(), demoDSNCanary)
		})
	}
}

func TestPrepareDemoDepsDoesNotRequireKeysWhenDemoIsDisabled(t *testing.T) {
	t.Setenv(demoROKeyEnvironment, "")
	t.Setenv(demoDMLKeyEnvironment, "")
	deps := Deps{Config: config.Config{Demo: config.DemoConfig{Enabled: false}}}

	prepared, err := PrepareDemoDeps(context.Background(), deps)
	require.NoError(t, err)
	require.False(t, prepared.demoKeys.valid())
}

func TestValidateDemoProfileKeysReturnsPrivateMapping(t *testing.T) {
	authenticator := &fakeDemoAuthenticator{agents: map[string]model.Agent{
		demoROKeyCanary: {
			ID: config.DemoAgentRO, Level: "readonly", Status: "active",
		},
		demoDMLKeyCanary: {
			ID: config.DemoAgentDML, Level: "dml", Status: "active",
		},
	}}
	keys, err := validateDemoProfileKeys(context.Background(), authenticator, mapLookup(map[string]string{
		demoROKeyEnvironment: demoROKeyCanary, demoDMLKeyEnvironment: demoDMLKeyCanary,
	}))
	require.NoError(t, err)
	require.Equal(t, demoProfileKeys{ro: demoROKeyCanary, dml: demoDMLKeyCanary}, keys)
	require.Equal(t, []string{demoROKeyCanary, demoDMLKeyCanary}, authenticator.calls)
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
