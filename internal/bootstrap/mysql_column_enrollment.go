package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/google/uuid"
)

type MySQLColumnGrant struct {
	Column string `json:"column"`
	Usage  string `json:"usage"`
}

// BindMySQLColumnPolicy is the administrative two-phase enrollment boundary.
// The first catalog read holds no control lock. The second revalidates while
// the metadata CAS is held, and keeps the business MDL until commit returns.
func (runtime *Runtime) BindMySQLColumnPolicy(ctx context.Context, policyID string, expectedRevision int64,
	schema, table string, grants []MySQLColumnGrant,
) (model.Policy, error) {
	if runtime == nil || !runtime.mysqlColumnEnabled || runtime.Store == nil || runtime.business == nil ||
		policyID == "" || expectedRevision <= 0 || schema == "" || table == "" ||
		len(grants) == 0 || len(grants) > 256 {
		return model.Policy{}, fmt.Errorf("MySQL column enrollment is unavailable or invalid")
	}
	policy, err := runtime.Store.Policies().Get(ctx, policyID)
	if err != nil {
		return model.Policy{}, err
	}
	if policy.Revision != expectedRevision || policy.Action != "allow" || policy.ObjectType != "column" ||
		policy.ObjectName != schema+"."+table || policy.RowFilter != nil {
		return model.Policy{}, store.ErrRevisionMismatch
	}
	datasource, err := runtime.Store.Datasources().Get(ctx, policy.DatasourceID)
	if err != nil || datasource.DBType != "mysql" {
		return model.Policy{}, fmt.Errorf("policy datasource is not MySQL: %w", err)
	}
	account, configured := runtime.mysqlAccounts[datasource.ID]
	password, present := os.LookupEnv(account.PasswordEnv)
	if !configured || !present || account.Username == "" {
		return model.Policy{}, fmt.Errorf("MySQL metadata identity account is unavailable")
	}
	requested := make(map[string]struct{}, len(grants))
	for _, grant := range grants {
		if grant.Column == "" || (grant.Usage != "output" && grant.Usage != "reference") {
			return model.Policy{}, fmt.Errorf("invalid MySQL column grant")
		}
		key := grant.Column + "\x00" + grant.Usage
		if _, duplicate := requested[key]; duplicate {
			return model.Policy{}, fmt.Errorf("duplicate MySQL column grant")
		}
		requested[key] = struct{}{}
	}
	var discovered executor.MySQLColumnCatalog
	discover := func(phase context.Context) error {
		lease, err := runtime.business.BindMySQLColumnCatalog(phase, datasource, runtime.secret,
			account.Username, password, schema, table)
		if err != nil {
			return err
		}
		discovered = lease.Catalog()
		return lease.Close()
	}
	revalidate := func(phase context.Context) (store.RevalidatedColumnBinding, error) {
		lease, err := runtime.business.BindMySQLColumnCatalog(phase, datasource, runtime.secret,
			account.Username, password, schema, table)
		if err != nil {
			return store.RevalidatedColumnBinding{}, err
		}
		catalog := lease.Catalog()
		if discovered.Fingerprint == "" || catalog.Fingerprint != discovered.Fingerprint ||
			catalog.ServerUUID != discovered.ServerUUID || catalog.TableID != discovered.TableID {
			_ = lease.Close()
			return store.RevalidatedColumnBinding{}, errors.New("MySQL catalog changed between discovery and revalidation")
		}
		byName := make(map[string]executor.MySQLCatalogColumn, len(catalog.Columns))
		for _, column := range catalog.Columns {
			byName[column.Name] = column
		}
		bindingID := uuid.NewString()
		permissions := make([]model.PolicyColumnPermission, 0, len(grants))
		for _, grant := range grants {
			column, exists := byName[grant.Column]
			if !exists || column.Ordinal <= 0 || column.TypeDigest == "" {
				_ = lease.Close()
				return store.RevalidatedColumnBinding{}, fmt.Errorf("requested MySQL column is unproven")
			}
			permissions = append(permissions, model.PolicyColumnPermission{PolicyID: policyID,
				RelationEnrollmentID: bindingID, ColumnOrdinal: column.Ordinal, ColumnName: column.Name,
				ColumnTypeDigest: column.TypeDigest, Usage: grant.Usage})
		}
		stable := fmt.Sprintf("mysql:%s:%d", catalog.ServerUUID, catalog.TableID)
		fingerprint := catalog.Fingerprint
		return store.RevalidatedColumnBinding{Binding: model.RelationPolicyBinding{
			ID: bindingID, PolicyID: policyID, DatasourceID: datasource.ID,
			SchemaName: catalog.Schema, RelationName: catalog.Table,
			StableObjectID: &stable, CatalogFingerprint: &fingerprint, Status: "healthy", Revision: 1,
		}, Permissions: permissions, Release: lease.Close}, nil
	}
	bound, err := runtime.Store.Policies().FinalizeColumnBindingTwoPhase(ctx, policyID, expectedRevision, discover, revalidate)
	if err != nil {
		return model.Policy{}, err
	}
	if bound.ID != policyID || strings.TrimSpace(bound.ID) == "" {
		return model.Policy{}, fmt.Errorf("MySQL column policy binding was not committed")
	}
	return bound, nil
}
