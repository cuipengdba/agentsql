package adminapi

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/cuipengdba/agentsql/internal/auth"
	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/pipeline"
)

const (
	demoROKeyEnvironment  = "AGENTSQL_DEMO_RO_KEY"
	demoDMLKeyEnvironment = "AGENTSQL_DEMO_DML_KEY"
)

type demoProfileKeys struct {
	ro  string
	dml string
}

func (keys demoProfileKeys) valid() bool {
	return keys.ro != "" && keys.dml != ""
}

func (keys demoProfileKeys) forProfile(profile string) (string, bool) {
	switch profile {
	case "ro":
		return keys.ro, keys.ro != ""
	case "dml":
		return keys.dml, keys.dml != ""
	default:
		return "", false
	}
}

// PrepareDemoDeps validates the two server-owned demo credentials during HTTP
// startup. The plaintext values remain in private Deps fields and are never
// added to configuration, logs, errors, or response DTOs.
func PrepareDemoDeps(ctx context.Context, deps Deps) (Deps, error) {
	if !deps.Config.DemoEnabled() {
		return deps, nil
	}
	if ctx == nil {
		return Deps{}, fmt.Errorf("prepare demo credentials: context is required")
	}
	if deps.Runtime == nil || deps.Runtime.Store == nil {
		return Deps{}, fmt.Errorf("prepare demo credentials: runtime store is unavailable")
	}
	if deps.Runtime.Pipeline == nil {
		return Deps{}, fmt.Errorf("prepare demo credentials: demo pipeline is unavailable")
	}
	keys, err := validateDemoProfileKeys(
		ctx,
		auth.NewAuthenticator(deps.Runtime.Store.Agents()),
		os.LookupEnv,
	)
	if err != nil {
		return Deps{}, err
	}
	deps.demoRunner = deps.Runtime.Pipeline
	deps.demoKeys = keys
	return deps, nil
}

type demoCredentialExpectation struct {
	profile     string
	environment string
	agentID     string
	level       string
}

func validateDemoProfileKeys(
	ctx context.Context,
	authenticator pipeline.IdentityAuthenticator,
	lookup func(string) (string, bool),
) (demoProfileKeys, error) {
	if ctx == nil || authenticator == nil || lookup == nil {
		return demoProfileKeys{}, fmt.Errorf("prepare demo credentials: validator is unavailable")
	}
	expectations := []demoCredentialExpectation{
		{profile: "ro", environment: demoROKeyEnvironment, agentID: config.DemoAgentRO, level: "readonly"},
		{profile: "dml", environment: demoDMLKeyEnvironment, agentID: config.DemoAgentDML, level: "dml"},
	}
	validated := demoProfileKeys{}
	for _, expected := range expectations {
		rawKey, present := lookup(expected.environment)
		if !present || strings.TrimSpace(rawKey) == "" {
			return demoProfileKeys{}, fmt.Errorf("prepare demo credentials: %s is required and must be non-empty", expected.environment)
		}
		agent, err := authenticator.Authenticate(ctx, rawKey)
		if err != nil {
			return demoProfileKeys{}, fmt.Errorf("prepare demo credentials: %s authentication failed", expected.environment)
		}
		if err := validateDemoAgent(agent, expected); err != nil {
			return demoProfileKeys{}, err
		}
		switch expected.profile {
		case "ro":
			validated.ro = rawKey
		case "dml":
			validated.dml = rawKey
		}
	}
	return validated, nil
}

func validateDemoAgent(agent model.Agent, expected demoCredentialExpectation) error {
	if agent.ID != expected.agentID || agent.Level != expected.level || agent.Status != "active" {
		return fmt.Errorf(
			"prepare demo credentials: %s must authenticate as active agent %q at level %q",
			expected.environment,
			expected.agentID,
			expected.level,
		)
	}
	return nil
}
