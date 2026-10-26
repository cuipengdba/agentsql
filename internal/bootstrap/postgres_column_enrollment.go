package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/google/uuid"
)

// BindPostgresColumnPolicy enrolls a regular PostgreSQL table using the
// selected native or closed binder. Discovery holds no control lock; the
// final catalog proof retains the business relation lock through commit.
func (runtime *Runtime) BindPostgresColumnPolicy(ctx context.Context, policyID string, expectedRevision int64, rawSQL string) (model.Policy, error) {
	if runtime == nil || runtime.Store == nil || runtime.business == nil || policyID == "" || expectedRevision <= 0 || strings.TrimSpace(rawSQL) == "" {
		return model.Policy{}, errors.New("PostgreSQL column enrollment is unavailable or invalid")
	}
	policy, err := runtime.Store.Policies().Get(ctx, policyID)
	if err != nil {
		return model.Policy{}, err
	}
	if policy.Revision != expectedRevision || policy.Action != "allow" || policy.ObjectType != "column" ||
		policy.RowFilter != nil || policy.Columns == nil {
		return model.Policy{}, store.ErrRevisionMismatch
	}
	requested := make(map[string]struct{})
	for _, raw := range strings.Split(*policy.Columns, ",") {
		name := strings.TrimSpace(raw)
		if name == "" || name == "*" || len(requested) >= 128 {
			return model.Policy{}, errors.New("invalid PostgreSQL column policy")
		}
		if _, duplicate := requested[name]; duplicate {
			return model.Policy{}, errors.New("duplicate PostgreSQL column grant")
		}
		requested[name] = struct{}{}
	}
	datasource, err := runtime.Store.Datasources().Get(ctx, policy.DatasourceID)
	if err != nil || datasource.DBType != "postgres" {
		return model.Policy{}, fmt.Errorf("policy datasource is not PostgreSQL: %w", err)
	}
	var discovered executor.PostgresPolicyEnrollment
	discover := func(phase context.Context) error {
		candidate, err := runtime.business.EnrollPostgresPolicySelect(phase, datasource, runtime.secret, rawSQL, executor.DefaultLimits)
		if err != nil {
			return err
		}
		if _, err := postgresColumnEnrollment(policy, requested, candidate, ""); err != nil {
			return err
		}
		discovered = candidate
		return nil
	}
	revalidate := func(phase context.Context) (store.RevalidatedColumnBinding, error) {
		lease, err := runtime.business.BindPostgresPolicySelect(phase, datasource, runtime.secret, rawSQL, executor.DefaultLimits)
		if err != nil {
			return store.RevalidatedColumnBinding{}, err
		}
		if !reflect.DeepEqual(discovered, lease.Enrollment) {
			_ = lease.Close()
			return store.RevalidatedColumnBinding{}, errors.New("PostgreSQL catalog changed between discovery and revalidation")
		}
		bindingID := uuid.NewString()
		bound, err := postgresColumnEnrollment(policy, requested, lease.Enrollment, bindingID)
		if err != nil {
			_ = lease.Close()
			return store.RevalidatedColumnBinding{}, err
		}
		bound.Release = lease.Close
		return bound, nil
	}
	return runtime.Store.Policies().FinalizeColumnBindingTwoPhase(ctx, policyID, expectedRevision, discover, revalidate)
}

func postgresColumnEnrollment(policy model.Policy, requested map[string]struct{}, enrollment executor.PostgresPolicyEnrollment, bindingID string) (store.RevalidatedColumnBinding, error) {
	if (enrollment.Mode != "CATALOG_CLOSED_V1" && enrollment.Mode != "NATIVE_C_V1") || enrollment.StatementClass != "SELECT" ||
		len(enrollment.Relations) != 1 || len(enrollment.ColumnUses) == 0 || len(enrollment.WriteTargets) != 0 {
		return store.RevalidatedColumnBinding{}, errors.New("PostgreSQL bound SELECT enrollment is required")
	}
	relation := enrollment.Relations[0]
	if relation.DatabaseOID == 0 || relation.RelationOID == 0 || relation.Kind != 'r' ||
		relation.Schema+"."+relation.Name != policy.ObjectName || relation.CatalogFingerprint == "" {
		return store.RevalidatedColumnBinding{}, errors.New("PostgreSQL relation identity mismatch")
	}
	columns := make(map[string]executor.PostgresPolicyColumnUse, len(requested))
	output := make(map[string]bool, len(requested))
	for _, use := range enrollment.ColumnUses {
		if _, allowed := requested[use.Name]; !allowed || use.RelationOID != relation.RelationOID ||
			use.Attnum <= 0 || (use.Usage != "output" && use.Usage != "reference") {
			return store.RevalidatedColumnBinding{}, errors.New("PostgreSQL enrollment includes an unrequested column")
		}
		if prior, duplicate := columns[use.Name]; duplicate {
			if prior.Attnum != use.Attnum || prior.TypeOID != use.TypeOID || prior.TypeModifier != use.TypeModifier || prior.CollationOID != use.CollationOID {
				return store.RevalidatedColumnBinding{}, errors.New("PostgreSQL column identity changed")
			}
		} else {
			columns[use.Name] = use
		}
		if use.Usage == "output" {
			output[use.Name] = true
		}
	}
	if len(columns) != len(requested) {
		return store.RevalidatedColumnBinding{}, errors.New("PostgreSQL enrollment is missing a requested column")
	}
	for name := range requested {
		if !output[name] {
			return store.RevalidatedColumnBinding{}, errors.New("PostgreSQL requested column is not proven in SELECT output")
		}
	}
	stable := executor.PostgresStableObjectID(relation.DatabaseOID, relation.RelationOID)
	fingerprint := relation.CatalogFingerprint
	result := store.RevalidatedColumnBinding{Binding: model.RelationPolicyBinding{
		ID: bindingID, PolicyID: policy.ID, DatasourceID: policy.DatasourceID,
		SchemaName: relation.Schema, RelationName: relation.Name,
		StableObjectID: &stable, CatalogFingerprint: &fingerprint, Status: "healthy", Revision: 1,
	}}
	for _, raw := range strings.Split(*policy.Columns, ",") {
		use := columns[strings.TrimSpace(raw)]
		for _, usage := range []string{"output", "reference"} {
			result.Permissions = append(result.Permissions, model.PolicyColumnPermission{
				PolicyID: policy.ID, RelationEnrollmentID: bindingID, ColumnOrdinal: int(use.Attnum),
				ColumnName: use.Name, ColumnTypeDigest: executor.PostgresColumnTypeDigest(use.TypeOID, use.TypeModifier, use.CollationOID),
				Usage: usage,
			})
		}
	}
	return result, nil
}
