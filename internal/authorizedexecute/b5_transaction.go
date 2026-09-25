package authorizedexecute

import (
	"context"
	"errors"

	"github.com/cuipengdba/agentsql/internal/authorizedexecute/internal/businessdb"
	"github.com/cuipengdba/agentsql/internal/b5coordinator"
	"github.com/cuipengdba/agentsql/internal/b5dml"
	"github.com/cuipengdba/agentsql/internal/model"
)

// B5PostgresRuntime is the feature-off capability bridge used by S6. Neither
// side exposes a pool, connection, transaction, prepared statement or raw SQL
// executor. Production protocol handlers intentionally do not reference it.
type B5PostgresRuntime struct {
	Analyzer b5coordinator.Analyzer
	Engine   b5coordinator.Engine
}

type B5DMLAuthorizationConfig struct {
	PrincipalID          string
	DatasourceID         string
	Policies             []b5dml.Policy
	Decision             b5coordinator.Decision
	PreliminaryAllowed   bool
	DatasourceSupported  bool
	ReservedTarget       bool
	PolicySnapshotDigest string
}

// B5DMLPolicyProvider derives the exact action/write/reference grants from an
// enrolled, non-executable statement fact set. It is the production-safe
// bridge for policy resolvers that cannot know PostgreSQL OIDs before binder
// enrollment; it never receives a connection, transaction, prepared object,
// or executor.
type B5DMLPolicyProvider func(context.Context, b5dml.StatementFacts, b5coordinator.StatementRequest, int) (B5DMLAuthorizationConfig, error)

// NewB5PostgresRuntime reuses the enrolled PostgreSQL pool but does not make
// B5 reachable from MCP/HTTP. S8 is responsible for protocol construction and
// S10 for any activation gate.
func (gateway *Gateway) NewB5PostgresRuntime(ctx context.Context, datasource model.Datasource, secret []byte, authorization B5DMLAuthorizationConfig) (B5PostgresRuntime, error) {
	if datasource.DBType != "postgres" || authorization.DatasourceID != datasource.ID || authorization.PrincipalID == "" {
		return B5PostgresRuntime{}, errors.New("authorizedexecute: invalid B5 PostgreSQL authorization")
	}
	opened, err := gateway.open(datasource, secret)
	if err != nil {
		return B5PostgresRuntime{}, fixedExecutionError(err)
	}
	postgres, ok := opened.(*businessdb.PostgresExecutor)
	if !ok {
		return B5PostgresRuntime{}, errors.New("authorizedexecute: PostgreSQL B5 capability unavailable")
	}
	authorizer := &staticB5Authorizer{config: authorization}
	runtime, err := businessdb.NewB5PostgresRuntime(postgres, authorizer)
	if err != nil {
		return B5PostgresRuntime{}, err
	}
	return B5PostgresRuntime{Analyzer: runtime, Engine: runtime}, nil
}

// NewB5PostgresRuntimeWithPolicyProvider is equivalent to
// NewB5PostgresRuntime but resolves exact grants after binder enrollment.
func (gateway *Gateway) NewB5PostgresRuntimeWithPolicyProvider(ctx context.Context, datasource model.Datasource, secret []byte, provider B5DMLPolicyProvider) (B5PostgresRuntime, error) {
	if datasource.DBType != "postgres" || provider == nil {
		return B5PostgresRuntime{}, errors.New("authorizedexecute: invalid B5 PostgreSQL policy provider")
	}
	opened, err := gateway.open(datasource, secret)
	if err != nil {
		return B5PostgresRuntime{}, fixedExecutionError(err)
	}
	postgres, ok := opened.(*businessdb.PostgresExecutor)
	if !ok {
		return B5PostgresRuntime{}, errors.New("authorizedexecute: PostgreSQL B5 capability unavailable")
	}
	runtime, err := businessdb.NewB5PostgresRuntime(postgres, dynamicB5Authorizer{datasourceID: datasource.ID, provider: provider})
	if err != nil {
		return B5PostgresRuntime{}, err
	}
	return B5PostgresRuntime{Analyzer: runtime, Engine: runtime}, nil
}

type dynamicB5Authorizer struct {
	datasourceID string
	provider     B5DMLPolicyProvider
}

func (authorizer dynamicB5Authorizer) DatasourceID() string { return authorizer.datasourceID }
func (authorizer dynamicB5Authorizer) AuthorizeCandidate(ctx context.Context, enrollment businessdb.PostgresDMLEnrollment, statement b5coordinator.StatementRequest, ordinal int) (businessdb.B5PostgresAuthorization, error) {
	configuration, err := authorizer.provider(ctx, enrollment.Facts, statement, ordinal)
	if err != nil {
		return businessdb.B5PostgresAuthorization{}, err
	}
	if configuration.DatasourceID != authorizer.datasourceID || configuration.PrincipalID == "" {
		return businessdb.B5PostgresAuthorization{}, errors.New("authorizedexecute: B5 policy provider identity mismatch")
	}
	return businessdb.B5PostgresAuthorization{Policies: append([]b5dml.Policy(nil), configuration.Policies...), Decision: configuration.Decision, PreliminaryAllowed: configuration.PreliminaryAllowed, DatasourceSupported: configuration.DatasourceSupported, ReservedTarget: configuration.ReservedTarget, PolicySnapshotDigest: configuration.PolicySnapshotDigest}, nil
}

type staticB5Authorizer struct{ config B5DMLAuthorizationConfig }

func (authorizer *staticB5Authorizer) DatasourceID() string { return authorizer.config.DatasourceID }
func (authorizer *staticB5Authorizer) AuthorizeCandidate(_ context.Context, enrollment businessdb.PostgresDMLEnrollment, _ b5coordinator.StatementRequest, _ int) (businessdb.B5PostgresAuthorization, error) {
	if enrollment.Facts.Target.DatasourceID != authorizer.config.DatasourceID {
		return businessdb.B5PostgresAuthorization{}, errors.New("authorizedexecute: B5 datasource identity mismatch")
	}
	policies := append([]b5dml.Policy(nil), authorizer.config.Policies...)
	for index := range policies {
		if policies[index].PrincipalID != authorizer.config.PrincipalID || policies[index].DatasourceID != authorizer.config.DatasourceID {
			return businessdb.B5PostgresAuthorization{}, errors.New("authorizedexecute: B5 policy identity mismatch")
		}
	}
	return businessdb.B5PostgresAuthorization{Policies: policies, Decision: authorizer.config.Decision, PreliminaryAllowed: authorizer.config.PreliminaryAllowed, DatasourceSupported: authorizer.config.DatasourceSupported, ReservedTarget: authorizer.config.ReservedTarget, PolicySnapshotDigest: authorizer.config.PolicySnapshotDigest}, nil
}
