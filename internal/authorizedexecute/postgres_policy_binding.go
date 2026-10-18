package authorizedexecute

import (
	"context"

	"github.com/cuipengdba/agentsql/internal/authorizedexecute/internal/businessdb"
	"github.com/cuipengdba/agentsql/internal/model"
)

// PostgresPolicyBindingLease holds the business catalog locks until the
// control-plane policy transaction has committed or rolled back. It cannot
// execute the SELECT used to prove the column identities.
type PostgresPolicyBindingLease struct {
	Enrollment PostgresPolicyEnrollment
	prepared   *businessdb.PostgresClosedPrepared
}

func (lease *PostgresPolicyBindingLease) Close() error {
	if lease == nil || lease.prepared == nil {
		return nil
	}
	err := lease.prepared.Close(context.Background())
	lease.prepared = nil
	return err
}

// BindPostgresPolicySelect revalidates one closed-binder SELECT while retaining
// its relation locks. Native-extension enrollment requires a separate locked
// implementation and is deliberately refused here.
func (gateway *Gateway) BindPostgresPolicySelect(ctx context.Context, datasource model.Datasource, secret []byte, rawSQL string, limits Limits) (*PostgresPolicyBindingLease, error) {
	if datasource.DBType != "postgres" {
		return nil, &AuthError{Reason: ReasonDatasourceUnsupported}
	}
	if err := ValidatePreParse(rawSQL, nil, limits); err != nil {
		return nil, err
	}
	capability, err := gateway.ProbePostgresB2Modes(ctx, datasource, secret)
	if err != nil {
		return nil, err
	}
	if capability.NativeAvailable || capability.Mode != "CATALOG_CLOSED_V1" {
		return nil, &AuthError{Reason: ReasonBinderModeUnsupported}
	}
	opened, err := gateway.open(datasource, secret)
	if err != nil {
		return nil, fixedExecutionError(err)
	}
	postgres, ok := opened.(*businessdb.PostgresExecutor)
	if !ok {
		return nil, &AuthError{Reason: ReasonDatasourceUnsupported}
	}
	prepared, err := postgres.BindClosedSelect(ctx, businessdb.BindRequest{
		RawSQL: rawSQL, Identity: businessdb.SemanticIdentity{DatasourceIdentity: datasource.ID},
	}, NewBudget(limits))
	if err != nil {
		return nil, StableError(err)
	}
	return &PostgresPolicyBindingLease{
		Enrollment: publicPolicyEnrollment(capability.Mode, prepared.Program().Facts),
		prepared:   prepared,
	}, nil
}
