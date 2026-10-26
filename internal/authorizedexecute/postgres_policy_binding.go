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
	closed     *businessdb.PostgresClosedPrepared
	native     *businessdb.PostgresPreparedSelect
}

func (lease *PostgresPolicyBindingLease) Close() error {
	if lease == nil {
		return nil
	}
	if lease.native != nil {
		err := lease.native.Close(context.Background(), false)
		lease.native = nil
		return err
	}
	if lease.closed != nil {
		err := lease.closed.Close(context.Background())
		lease.closed = nil
		return err
	}
	return nil
}

// BindPostgresPolicySelect revalidates a SELECT under the selected binder while
// retaining its relation locks through the control-plane policy commit. The
// lease exposes identities only; its native prepared portal is always rolled
// back without executing the query.
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
	if capability.Mode != "CATALOG_CLOSED_V1" && capability.Mode != "NATIVE_C_V1" {
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
	if capability.Mode == "NATIVE_C_V1" {
		if !capability.NativeAvailable {
			return nil, &AuthError{Reason: ReasonBinderModeUnsupported}
		}
		prepared, err := postgres.PrepareBoundPostgresSelect(ctx, rawSQL, nil, NewBudget(limits))
		if err != nil {
			return nil, StableError(err)
		}
		facts, err := businessdb.NativeSelectSemanticFacts(datasource.ID,
			businessdb.SemanticIdentity{DatasourceIdentity: datasource.ID}, prepared.Manifest(), prepared.Fpre())
		if err != nil {
			_ = prepared.Close(context.Background(), false)
			return nil, StableError(err)
		}
		if facts.StatementClass != businessdb.BinderStatementSelect || len(facts.WriteTargets) != 0 {
			_ = prepared.Close(context.Background(), false)
			return nil, &AuthError{Reason: ReasonBinderIncomplete}
		}
		enrollment, err := publicNativePreparedPolicyEnrollment(capability.Mode, prepared.Manifest(), prepared.Fpre())
		if err != nil {
			_ = prepared.Close(context.Background(), false)
			return nil, err
		}
		return &PostgresPolicyBindingLease{
			Enrollment: enrollment, native: prepared,
		}, nil
	}
	if capability.NativeAvailable {
		return nil, &AuthError{Reason: ReasonBinderModeUnsupported}
	}
	prepared, err := postgres.BindClosedSelect(ctx, businessdb.BindRequest{
		RawSQL: rawSQL, Identity: businessdb.SemanticIdentity{DatasourceIdentity: datasource.ID},
	}, NewBudget(limits))
	if err != nil {
		return nil, StableError(err)
	}
	return &PostgresPolicyBindingLease{
		Enrollment: publicPolicyEnrollment(capability.Mode, prepared.Program().Facts),
		closed:     prepared,
	}, nil
}
