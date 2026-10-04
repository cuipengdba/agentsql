package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/cuipengdba/agentsql/internal/model"
)

var ErrRevisionMismatch = errors.New("store revision mismatch")

// PolicyRepository owns policy parents, bindings, formal permissions, and
// legacy staging rows. Mutations use one control-plane transaction.
type PolicyRepository struct{ repositoryBase }

type policyQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// RevalidatedColumnBinding is produced from original legacy/star input while
// the caller holds its business schema lock. Discovery results are never
// accepted as this value.
type RevalidatedColumnBinding struct {
	Binding     model.RelationPolicyBinding
	Permissions []model.PolicyColumnPermission
}

// FinalizeColumnBindingTwoPhase enforces discovery-without-control followed by
// control-exclusive -> business revalidation -> metadata CAS. The revalidate
// callback is the only place a business lock may be acquired.
func (repository *PolicyRepository) FinalizeColumnBindingTwoPhase(
	ctx context.Context,
	policyID string,
	expectedRevision int64,
	discover func(context.Context) error,
	revalidate func(context.Context) (RevalidatedColumnBinding, error),
) (model.Policy, error) {
	tenantID, tenantErr := repository.requireTenant(ctx, "finalize policy binding")
	if tenantErr != nil {
		return model.Policy{}, tenantErr
	}
	if discover == nil || revalidate == nil {
		return model.Policy{}, fmt.Errorf("two-phase binding callbacks are required")
	}
	// Phase one must return after releasing every candidate business resource.
	if err := discover(ctx); err != nil {
		return model.Policy{}, fmt.Errorf("binding discovery: %w", err)
	}
	tx, err := repository.beginMutation(ctx)
	if err != nil {
		return model.Policy{}, err
	}
	currentRevision, err := repository.lockPolicy(ctx, tx, policyID)
	if err != nil {
		return model.Policy{}, rollbackPolicyTx(tx, err)
	}
	if currentRevision != expectedRevision {
		return model.Policy{}, rollbackPolicyTx(tx, ErrRevisionMismatch)
	}
	// The control fence/parent row is now held. This callback must acquire and
	// retain the business lock until it returns the fully revalidated result.
	bound, err := revalidate(ctx)
	if err != nil {
		return model.Policy{}, rollbackPolicyTx(tx, fmt.Errorf("binding revalidation: %w", err))
	}
	parent, err := scanPolicy(tx.QueryRowContext(ctx, repository.bind(policySelect+` WHERE id=? AND tenant_id=?`), policyID, tenantID))
	if err != nil {
		return model.Policy{}, rollbackPolicyTx(tx, err)
	}
	parent.RelationBinding = &bound.Binding
	parent.RelationBindingID = &bound.Binding.ID
	parent.ColumnPermissions = bound.Permissions
	parent.ColumnStaging = nil
	next := currentRevision + 1
	if err := repository.replaceChildren(ctx, tx, parent, next); err != nil {
		return model.Policy{}, rollbackPolicyTx(tx, err)
	}
	if _, err := tx.ExecContext(ctx, repository.bind(`UPDATE policies SET relation_binding_id=?,revision=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND tenant_id=?`), bound.Binding.ID, next, policyID, tenantID); err != nil {
		return model.Policy{}, rollbackPolicyTx(tx, err)
	}
	if err := tx.Commit(); err != nil {
		return model.Policy{}, fmt.Errorf("commit two-phase binding: %w", err)
	}
	return repository.Get(ctx, policyID)
}

func (repository *PolicyRepository) Create(ctx context.Context, policy model.Policy) (model.Policy, error) {
	if ctx == nil {
		return model.Policy{}, fmt.Errorf("create policy: %w", ErrNilContext)
	}
	tenantID, err := repository.requireTenant(ctx, "create policy")
	if err != nil {
		return model.Policy{}, err
	}
	tx, err := repository.beginMutation(ctx)
	if err != nil {
		return model.Policy{}, err
	}
	if policy.Revision <= 0 {
		policy.Revision = 1
	}
	_, err = tx.ExecContext(ctx, repository.bind(`INSERT INTO policies
  (id,tenant_id,agent_id,datasource_id,object_type,object_name,columns,row_filter,action,relation_binding_id,revision,legacy_unrepresentable)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`), policy.ID, tenantID, policy.AgentID, policy.DatasourceID, policy.ObjectType,
		policy.ObjectName, optionalString(policy.Columns), optionalString(policy.RowFilter), policy.Action,
		optionalString(policy.RelationBindingID), policy.Revision, policy.LegacyUnrepresentable)
	if err != nil {
		return model.Policy{}, rollbackPolicyTx(tx, fmt.Errorf("create policy %q: %w", policy.ID, err))
	}
	if err := repository.replaceChildren(ctx, tx, policy, policy.Revision); err != nil {
		return model.Policy{}, rollbackPolicyTx(tx, err)
	}
	if _, err := tx.ExecContext(ctx, repository.bind(`UPDATE policies SET revision=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND tenant_id=?`), policy.Revision, policy.ID, tenantID); err != nil {
		return model.Policy{}, rollbackPolicyTx(tx, err)
	}
	if err := tx.Commit(); err != nil {
		return model.Policy{}, fmt.Errorf("commit policy %q: %w", policy.ID, err)
	}
	return repository.Get(ctx, policy.ID)
}

func (repository *PolicyRepository) Get(ctx context.Context, id string) (model.Policy, error) {
	tenantID, tenantErr := repository.requireTenant(ctx, "get policy")
	if tenantErr != nil {
		return model.Policy{}, tenantErr
	}
	policy, err := scanPolicy(repository.db.QueryRowContext(ctx, repository.bind(policySelect+` WHERE id=? AND tenant_id=?`), id, tenantID))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Policy{}, fmt.Errorf("get policy %q: %w", id, errors.Join(ErrNotFound, err))
	}
	if err != nil {
		return model.Policy{}, fmt.Errorf("get policy %q: %w", id, err)
	}
	items := []model.Policy{policy}
	if err := repository.loadChildren(ctx, items); err != nil {
		return model.Policy{}, fmt.Errorf("get policy %q children: %w", id, err)
	}
	return items[0], nil
}

func (repository *PolicyRepository) ListByAgentAndDatasource(ctx context.Context, agentID, datasourceID string) ([]model.Policy, error) {
	return repository.list(ctx, ` WHERE agent_id=? AND datasource_id=? ORDER BY created_at ASC,id ASC`, agentID, datasourceID)
}

func (repository *PolicyRepository) List(ctx context.Context) ([]model.Policy, error) {
	return repository.list(ctx, ` ORDER BY agent_id ASC,datasource_id ASC,object_name ASC,id ASC`)
}

func (repository *PolicyRepository) ListByAgent(ctx context.Context, agentID string) ([]model.Policy, error) {
	return repository.list(ctx, ` WHERE agent_id=? ORDER BY datasource_id ASC,object_name ASC,id ASC`, agentID)
}

func (repository *PolicyRepository) list(ctx context.Context, suffix string, arguments ...any) ([]model.Policy, error) {
	return repository.listWith(ctx, repository.db, suffix, arguments...)
}

func (repository *PolicyRepository) listWith(ctx context.Context, queryer policyQueryer, suffix string, arguments ...any) ([]model.Policy, error) {
	if ctx == nil {
		return nil, fmt.Errorf("list policies: %w", ErrNilContext)
	}
	tenantID, err := repository.requireTenant(ctx, "list policies")
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(suffix)
	if strings.HasPrefix(trimmed, "WHERE ") {
		suffix = " AND " + strings.TrimPrefix(trimmed, "WHERE ")
	} else {
		suffix = " " + trimmed
	}
	query := policySelect + " WHERE tenant_id=?" + suffix
	arguments = append([]any{tenantID}, arguments...)
	if strings.Contains(suffix, "agent_id=? AND datasource_id=?") {
		query = policySelect + indexHint(repository.dialect, " INDEXED BY idx_policies_agent_ds") + " WHERE tenant_id=?" + suffix
	}
	rows, err := queryer.QueryContext(ctx, repository.bind(query), arguments...)
	if err != nil {
		return nil, fmt.Errorf("list policies: %w", err)
	}
	items := make([]model.Policy, 0)
	for rows.Next() {
		item, scanErr := scanPolicy(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan policies: %w", closePolicyRowsAfterError(rows, scanErr))
		}
		items = append(items, item)
	}
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil || closeErr != nil {
		return nil, fmt.Errorf("finish policies: %w", errors.Join(iterationErr, closeErr))
	}
	if err := repository.loadChildrenWith(ctx, queryer, items); err != nil {
		return nil, fmt.Errorf("load policy children: %w", err)
	}
	return items, nil
}

func (repository *PolicyRepository) Update(ctx context.Context, policy model.Policy) (model.Policy, error) {
	return repository.update(ctx, policy, 0, false)
}

func (repository *PolicyRepository) UpdateIfRevision(ctx context.Context, policy model.Policy, expected int64) (model.Policy, error) {
	return repository.update(ctx, policy, expected, true)
}

func (repository *PolicyRepository) update(ctx context.Context, policy model.Policy, expected int64, compare bool) (model.Policy, error) {
	tenantID, tenantErr := repository.requireTenant(ctx, "update policy")
	if tenantErr != nil {
		return model.Policy{}, tenantErr
	}
	tx, err := repository.beginMutation(ctx)
	if err != nil {
		return model.Policy{}, err
	}
	current, err := repository.lockPolicy(ctx, tx, policy.ID)
	if err != nil {
		return model.Policy{}, rollbackPolicyTx(tx, err)
	}
	if compare && current != expected {
		return model.Policy{}, rollbackPolicyTx(tx, ErrRevisionMismatch)
	}
	next := current + 1
	result, err := tx.ExecContext(ctx, repository.bind(`UPDATE policies SET agent_id=?,datasource_id=?,object_type=?,object_name=?,columns=?,row_filter=?,action=?,relation_binding_id=?,legacy_unrepresentable=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND tenant_id=? AND revision=?`),
		policy.AgentID, policy.DatasourceID, policy.ObjectType, policy.ObjectName, optionalString(policy.Columns), optionalString(policy.RowFilter), policy.Action,
		optionalString(policy.RelationBindingID), policy.LegacyUnrepresentable, policy.ID, tenantID, current)
	if err != nil {
		return model.Policy{}, rollbackPolicyTx(tx, fmt.Errorf("update policy %q: %w", policy.ID, err))
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return model.Policy{}, rollbackPolicyTx(tx, ErrRevisionMismatch)
	}
	if err := repository.replaceChildren(ctx, tx, policy, next); err != nil {
		return model.Policy{}, rollbackPolicyTx(tx, err)
	}
	if _, err := tx.ExecContext(ctx, repository.bind(`UPDATE policies SET revision=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`), next, policy.ID); err != nil {
		return model.Policy{}, rollbackPolicyTx(tx, err)
	}
	if err := tx.Commit(); err != nil {
		return model.Policy{}, fmt.Errorf("commit policy %q: %w", policy.ID, err)
	}
	return repository.Get(ctx, policy.ID)
}

func (repository *PolicyRepository) Delete(ctx context.Context, id string) error {
	return repository.delete(ctx, id, 0, false)
}

func (repository *PolicyRepository) DeleteIfRevision(ctx context.Context, id string, expected int64) error {
	return repository.delete(ctx, id, expected, true)
}

func (repository *PolicyRepository) delete(ctx context.Context, id string, expected int64, compare bool) error {
	tenantID, tenantErr := repository.requireTenant(ctx, "delete policy")
	if tenantErr != nil {
		return tenantErr
	}
	tx, err := repository.beginMutation(ctx)
	if err != nil {
		return err
	}
	current, err := repository.lockPolicy(ctx, tx, id)
	if err != nil {
		return rollbackPolicyTx(tx, err)
	}
	if compare && current != expected {
		return rollbackPolicyTx(tx, ErrRevisionMismatch)
	}
	if _, err := tx.ExecContext(ctx, repository.bind(`DELETE FROM policies WHERE id=? AND tenant_id=? AND revision=?`), id, tenantID, current); err != nil {
		return rollbackPolicyTx(tx, fmt.Errorf("delete policy %q: %w", id, err))
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit delete policy %q: %w", id, err)
	}
	return nil
}

func (repository *PolicyRepository) beginMutation(ctx context.Context) (*sql.Tx, error) {
	if ctx == nil {
		return nil, fmt.Errorf("policy mutation: %w", ErrNilContext)
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin policy mutation: %w", err)
	}
	if repository.dialect == DialectPostgres {
		if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1),set_config('agentsql.policy_mutation_guard','on',true)`, int64(0x4153514c46454e43)); err != nil {
			return nil, rollbackPolicyTx(tx, fmt.Errorf("lock policy fence: %w", err))
		}
	} else if _, err = tx.ExecContext(ctx, `UPDATE control_plane_compat SET revision=revision WHERE fence_key='global'`); err != nil {
		return nil, rollbackPolicyTx(tx, fmt.Errorf("lock policy fence: %w", err))
	}
	return tx, nil
}

func (repository *PolicyRepository) lockPolicy(ctx context.Context, tx *sql.Tx, id string) (int64, error) {
	tenantID, err := repository.requireTenant(ctx, "lock policy")
	if err != nil {
		return 0, err
	}
	query := repository.bind(`SELECT revision FROM policies WHERE id=? AND tenant_id=?`)
	if repository.dialect == DialectPostgres {
		query += ` FOR UPDATE`
	}
	var revision int64
	if err := tx.QueryRowContext(ctx, query, id, tenantID).Scan(&revision); errors.Is(err, sql.ErrNoRows) {
		return 0, errors.Join(ErrNotFound, err)
	} else if err != nil {
		return 0, err
	}
	return revision, nil
}

func (repository *PolicyRepository) replaceChildren(ctx context.Context, tx *sql.Tx, policy model.Policy, revision int64) error {
	tenantID, tenantErr := repository.requireTenant(ctx, "replace policy children")
	if tenantErr != nil {
		return tenantErr
	}
	if policy.Columns != nil {
		for _, token := range strings.Split(*policy.Columns, ",") {
			if strings.TrimSpace(token) == "*" {
				return fmt.Errorf("new wildcard column grants are forbidden")
			}
		}
	}
	for _, table := range []string{"policy_column_permissions", "policy_column_permission_staging", "relation_policy_bindings"} {
		if _, err := tx.ExecContext(ctx, repository.bind(`DELETE FROM `+table+` WHERE policy_id=? AND tenant_id=?`), policy.ID, tenantID); err != nil {
			return fmt.Errorf("clear %s for policy %q: %w", table, policy.ID, err)
		}
	}
	if policy.RelationBinding != nil {
		binding := *policy.RelationBinding
		binding.PolicyID, binding.DatasourceID = policy.ID, policy.DatasourceID
		if binding.Revision <= 0 {
			binding.Revision = 1
		}
		if binding.Status == "" {
			binding.Status = "staging"
		}
		_, err := tx.ExecContext(ctx, repository.bind(`INSERT INTO relation_policy_bindings
 (id,tenant_id,policy_id,datasource_id,schema_name,relation_name,stable_object_id,catalog_fingerprint,status,revision)
 VALUES(?,?,?,?,?,?,?,?,?,?)`), binding.ID, tenantID, binding.PolicyID, binding.DatasourceID, binding.SchemaName, binding.RelationName,
			optionalString(binding.StableObjectID), optionalString(binding.CatalogFingerprint), binding.Status, binding.Revision)
		if err != nil {
			return fmt.Errorf("insert policy binding: %w", err)
		}
		if _, err := tx.ExecContext(ctx, repository.bind(`UPDATE policies SET relation_binding_id=? WHERE id=?`), binding.ID, policy.ID); err != nil {
			return err
		}
	}
	for _, permission := range policy.ColumnPermissions {
		if permission.ColumnOrdinal <= 0 || permission.RelationEnrollmentID == "" || permission.ColumnName == "" || permission.ColumnName == "*" || permission.ColumnTypeDigest == "" || (permission.Usage != "output" && permission.Usage != "reference") {
			return fmt.Errorf("invalid column permission")
		}
		_, err := tx.ExecContext(ctx, repository.bind(`INSERT INTO policy_column_permissions
 (policy_id,tenant_id,relation_enrollment_id,column_ordinal,column_name,column_type_digest,usage,parent_revision) VALUES(?,?,?,?,?,?,?,?)`),
			policy.ID, tenantID, permission.RelationEnrollmentID, permission.ColumnOrdinal, permission.ColumnName, permission.ColumnTypeDigest, permission.Usage, revision)
		if err != nil {
			return fmt.Errorf("insert policy permission: %w", err)
		}
	}
	staging := policy.ColumnStaging
	if len(staging) == 0 && len(policy.ColumnPermissions) == 0 && policy.Columns != nil {
		staging = legacyStaging(policy.ID, *policy.Columns)
	}
	for _, row := range staging {
		if row.TokenOrdinal <= 0 || row.LegacyToken == "" || (row.RequestedUsage != "output" && row.RequestedUsage != "reference") || len(row.SourceCSVHash) != 64 {
			return fmt.Errorf("invalid staged column permission")
		}
		status := row.BindStatus
		if status == "" {
			status = "pending"
		}
		_, err := tx.ExecContext(ctx, repository.bind(`INSERT INTO policy_column_permission_staging
 (policy_id,tenant_id,token_ordinal,legacy_token,requested_usage,source_csv_sha256,bind_status,error_code) VALUES(?,?,?,?,?,?,?,?)`),
			policy.ID, tenantID, row.TokenOrdinal, row.LegacyToken, row.RequestedUsage, row.SourceCSVHash, status, optionalString(row.ErrorCode))
		if err != nil {
			return fmt.Errorf("insert staged column permission: %w", err)
		}
	}
	return nil
}

func legacyStaging(policyID, source string) []model.PolicyColumnPermissionStaging {
	digest := sha256.Sum256([]byte(source))
	hash := hex.EncodeToString(digest[:])
	parts := strings.Split(source, ",")
	rows := make([]model.PolicyColumnPermissionStaging, 0, len(parts)*2)
	for index, part := range parts {
		token := strings.TrimSpace(part)
		if token == "" {
			continue
		}
		for _, usage := range []string{"output", "reference"} {
			rows = append(rows, model.PolicyColumnPermissionStaging{PolicyID: policyID, TokenOrdinal: index + 1, LegacyToken: token, RequestedUsage: usage, SourceCSVHash: hash, BindStatus: "pending"})
		}
	}
	return rows
}

func (repository *PolicyRepository) loadChildren(ctx context.Context, policies []model.Policy) error {
	return repository.loadChildrenWith(ctx, repository.db, policies)
}

func (repository *PolicyRepository) loadChildrenWith(ctx context.Context, queryer policyQueryer, policies []model.Policy) error {
	if len(policies) == 0 {
		return nil
	}
	byID := make(map[string]*model.Policy, len(policies))
	for index := range policies {
		byID[policies[index].ID] = &policies[index]
	}
	tenantID, err := repository.requireTenant(ctx, "load policy children")
	if err != nil {
		return err
	}
	bindings, err := queryer.QueryContext(ctx, repository.bind(`SELECT id,policy_id,datasource_id,schema_name,relation_name,stable_object_id,catalog_fingerprint,status,revision,created_at,updated_at FROM relation_policy_bindings WHERE tenant_id=? ORDER BY policy_id`), tenantID)
	if err != nil {
		return err
	}
	for bindings.Next() {
		var binding model.RelationPolicyBinding
		var stable, fingerprint sql.NullString
		var created, updated databaseTimestamp
		if err := bindings.Scan(&binding.ID, &binding.PolicyID, &binding.DatasourceID, &binding.SchemaName, &binding.RelationName, &stable, &fingerprint, &binding.Status, &binding.Revision, &created, &updated); err != nil {
			_ = bindings.Close()
			return err
		}
		binding.StableObjectID, binding.CatalogFingerprint = stringPointer(stable), stringPointer(fingerprint)
		binding.CreatedAt, _ = created.required("binding.created_at")
		binding.UpdatedAt, _ = updated.required("binding.updated_at")
		if parent := byID[binding.PolicyID]; parent != nil {
			parent.RelationBinding = &binding
		}
	}
	if err := errors.Join(bindings.Err(), bindings.Close()); err != nil {
		return err
	}
	permissions, err := queryer.QueryContext(ctx, repository.bind(`SELECT policy_id,relation_enrollment_id,column_ordinal,column_name,column_type_digest,usage,parent_revision FROM policy_column_permissions WHERE tenant_id=? ORDER BY policy_id,column_ordinal,usage`), tenantID)
	if err != nil {
		return err
	}
	for permissions.Next() {
		var permission model.PolicyColumnPermission
		if err := permissions.Scan(&permission.PolicyID, &permission.RelationEnrollmentID, &permission.ColumnOrdinal, &permission.ColumnName, &permission.ColumnTypeDigest, &permission.Usage, &permission.ParentRevision); err != nil {
			_ = permissions.Close()
			return err
		}
		if parent := byID[permission.PolicyID]; parent != nil {
			parent.ColumnPermissions = append(parent.ColumnPermissions, permission)
		}
	}
	if err := errors.Join(permissions.Err(), permissions.Close()); err != nil {
		return err
	}
	staging, err := queryer.QueryContext(ctx, repository.bind(`SELECT policy_id,token_ordinal,legacy_token,requested_usage,source_csv_sha256,bind_status,error_code FROM policy_column_permission_staging WHERE tenant_id=? ORDER BY policy_id,token_ordinal,requested_usage`), tenantID)
	if err != nil {
		return err
	}
	for staging.Next() {
		var row model.PolicyColumnPermissionStaging
		var code sql.NullString
		if err := staging.Scan(&row.PolicyID, &row.TokenOrdinal, &row.LegacyToken, &row.RequestedUsage, &row.SourceCSVHash, &row.BindStatus, &code); err != nil {
			_ = staging.Close()
			return err
		}
		row.ErrorCode = stringPointer(code)
		if parent := byID[row.PolicyID]; parent != nil {
			parent.ColumnStaging = append(parent.ColumnStaging, row)
		}
	}
	return errors.Join(staging.Err(), staging.Close())
}

const policySelect = `SELECT id,tenant_id,agent_id,datasource_id,object_type,object_name,columns,row_filter,action,relation_binding_id,revision,legacy_unrepresentable,created_at,updated_at FROM policies`

func scanPolicy(scanner rowScanner) (model.Policy, error) {
	var policy model.Policy
	var columns, rowFilter, binding sql.NullString
	var created, updated databaseTimestamp
	if err := scanner.Scan(&policy.ID, &policy.TenantID, &policy.AgentID, &policy.DatasourceID, &policy.ObjectType, &policy.ObjectName, &columns, &rowFilter, &policy.Action, &binding, &policy.Revision, &policy.LegacyUnrepresentable, &created, &updated); err != nil {
		return policy, fmt.Errorf("scan policy: %w", err)
	}
	policy.Columns, policy.RowFilter, policy.RelationBindingID = stringPointer(columns), stringPointer(rowFilter), stringPointer(binding)
	var err error
	policy.CreatedAt, err = created.required("policies.created_at")
	if err != nil {
		return policy, err
	}
	policy.UpdatedAt, err = updated.required("policies.updated_at")
	return policy, err
}

func rollbackPolicyTx(tx *sql.Tx, cause error) error {
	if err := tx.Rollback(); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

func closePolicyRowsAfterError(rows *sql.Rows, cause error) error {
	if err := rows.Close(); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}
