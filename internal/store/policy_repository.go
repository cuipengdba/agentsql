package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/cuipengdba/agentsql/internal/model"
)

// PolicyRepository provides CRUD operations for policies.
type PolicyRepository struct {
	db *sql.DB
}

// Create inserts a policy and returns the stored record.
func (repository *PolicyRepository) Create(ctx context.Context, policy model.Policy) (model.Policy, error) {
	_, err := repository.db.ExecContext(ctx, `
INSERT INTO policies (
  id, agent_id, datasource_id, object_type, object_name, columns, row_filter, action
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		policy.ID,
		policy.AgentID,
		policy.DatasourceID,
		policy.ObjectType,
		policy.ObjectName,
		optionalString(policy.Columns),
		optionalString(policy.RowFilter),
		policy.Action,
	)
	if err != nil {
		return model.Policy{}, fmt.Errorf("create policy %q: %w", policy.ID, err)
	}
	created, err := repository.Get(ctx, policy.ID)
	if err != nil {
		return model.Policy{}, fmt.Errorf("read created policy %q: %w", policy.ID, err)
	}
	return created, nil
}

// Get returns a policy by ID.
func (repository *PolicyRepository) Get(ctx context.Context, id string) (model.Policy, error) {
	policy, err := scanPolicy(repository.db.QueryRowContext(ctx, `
SELECT id, agent_id, datasource_id, object_type, object_name, columns, row_filter,
       action, created_at, updated_at
FROM policies
WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Policy{}, fmt.Errorf("get policy %q: %w", id, errors.Join(ErrNotFound, err))
	}
	if err != nil {
		return model.Policy{}, fmt.Errorf("get policy %q: %w", id, err)
	}
	return policy, nil
}

// ListByAgentAndDatasource returns policies in stable creation order.
func (repository *PolicyRepository) ListByAgentAndDatasource(
	ctx context.Context,
	agentID string,
	datasourceID string,
) ([]model.Policy, error) {
	rows, err := repository.db.QueryContext(ctx, `
SELECT id, agent_id, datasource_id, object_type, object_name, columns, row_filter,
       action, created_at, updated_at
FROM policies INDEXED BY idx_policies_agent_ds
WHERE agent_id = ? AND datasource_id = ?
ORDER BY created_at ASC, id ASC`, agentID, datasourceID)
	if err != nil {
		return nil, fmt.Errorf(
			"list policies for agent %q and datasource %q: %w",
			agentID,
			datasourceID,
			err,
		)
	}

	policies := make([]model.Policy, 0)
	for rows.Next() {
		policy, err := scanPolicy(rows)
		if err != nil {
			return nil, fmt.Errorf(
				"list policies for agent %q and datasource %q: %w",
				agentID,
				datasourceID,
				closePolicyRowsAfterError(rows, err),
			)
		}
		policies = append(policies, policy)
	}
	iterationError := rows.Err()
	closeError := rows.Close()
	if iterationError != nil || closeError != nil {
		return nil, fmt.Errorf(
			"finish policies for agent %q and datasource %q: %w",
			agentID,
			datasourceID,
			errors.Join(iterationError, closeError),
		)
	}
	return policies, nil
}

// List returns all policy rows ordered by Agent, datasource, object, and ID.
func (repository *PolicyRepository) List(ctx context.Context) ([]model.Policy, error) {
	if ctx == nil {
		return nil, fmt.Errorf("list policies: %w", ErrNilContext)
	}
	rows, err := repository.db.QueryContext(ctx, `
SELECT id, agent_id, datasource_id, object_type, object_name, columns, row_filter,
       action, created_at, updated_at
FROM policies
ORDER BY agent_id ASC, datasource_id ASC, object_name ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list policies: %w", err)
	}
	policies := make([]model.Policy, 0)
	for rows.Next() {
		stored, err := scanPolicy(rows)
		if err != nil {
			return nil, fmt.Errorf("scan policies: %w", closePolicyRowsAfterError(rows, err))
		}
		policies = append(policies, stored)
	}
	iterationError := rows.Err()
	closeError := rows.Close()
	if iterationError != nil || closeError != nil {
		return nil, fmt.Errorf("finish policies: %w", errors.Join(iterationError, closeError))
	}
	return policies, nil
}

// ListByAgent returns all policy rows for one Agent in stable datasource and
// object order.
func (repository *PolicyRepository) ListByAgent(
	ctx context.Context,
	agentID string,
) ([]model.Policy, error) {
	if ctx == nil {
		return nil, fmt.Errorf("list policies by Agent: %w", ErrNilContext)
	}
	rows, err := repository.db.QueryContext(ctx, `
SELECT id, agent_id, datasource_id, object_type, object_name, columns, row_filter,
       action, created_at, updated_at
FROM policies
WHERE agent_id = ?
ORDER BY datasource_id ASC, object_name ASC, id ASC`, agentID)
	if err != nil {
		return nil, fmt.Errorf("list policies for agent %q: %w", agentID, err)
	}
	policies := make([]model.Policy, 0)
	for rows.Next() {
		stored, err := scanPolicy(rows)
		if err != nil {
			return nil, fmt.Errorf(
				"scan policies for agent %q: %w",
				agentID,
				closePolicyRowsAfterError(rows, err),
			)
		}
		policies = append(policies, stored)
	}
	iterationError := rows.Err()
	closeError := rows.Close()
	if iterationError != nil || closeError != nil {
		return nil, fmt.Errorf(
			"finish policies for agent %q: %w",
			agentID,
			errors.Join(iterationError, closeError),
		)
	}
	return policies, nil
}

// Update replaces mutable policy fields and returns the stored record.
func (repository *PolicyRepository) Update(ctx context.Context, policy model.Policy) (model.Policy, error) {
	result, err := repository.db.ExecContext(ctx, `
UPDATE policies
SET agent_id = ?, datasource_id = ?, object_type = ?, object_name = ?,
    columns = ?, row_filter = ?, action = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?`,
		policy.AgentID,
		policy.DatasourceID,
		policy.ObjectType,
		policy.ObjectName,
		optionalString(policy.Columns),
		optionalString(policy.RowFilter),
		policy.Action,
		policy.ID,
	)
	if err != nil {
		return model.Policy{}, fmt.Errorf("update policy %q: %w", policy.ID, err)
	}
	if err := checkRowsAffected(result, "policy", policy.ID); err != nil {
		return model.Policy{}, fmt.Errorf("update policy %q: %w", policy.ID, err)
	}
	updated, err := repository.Get(ctx, policy.ID)
	if err != nil {
		return model.Policy{}, fmt.Errorf("read updated policy %q: %w", policy.ID, err)
	}
	return updated, nil
}

// Delete removes a policy by ID.
func (repository *PolicyRepository) Delete(ctx context.Context, id string) error {
	result, err := repository.db.ExecContext(ctx, "DELETE FROM policies WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete policy %q: %w", id, err)
	}
	if err := checkRowsAffected(result, "policy", id); err != nil {
		return fmt.Errorf("delete policy %q: %w", id, err)
	}
	return nil
}

func scanPolicy(scanner rowScanner) (model.Policy, error) {
	var policy model.Policy
	var columns, rowFilter sql.NullString
	var createdAt, updatedAt databaseTimestamp
	if err := scanner.Scan(
		&policy.ID,
		&policy.AgentID,
		&policy.DatasourceID,
		&policy.ObjectType,
		&policy.ObjectName,
		&columns,
		&rowFilter,
		&policy.Action,
		&createdAt,
		&updatedAt,
	); err != nil {
		return model.Policy{}, fmt.Errorf("scan policy: %w", err)
	}

	policy.Columns = stringPointer(columns)
	policy.RowFilter = stringPointer(rowFilter)
	var err error
	policy.CreatedAt, err = createdAt.required("policies.created_at")
	if err != nil {
		return model.Policy{}, fmt.Errorf("scan policy: %w", err)
	}
	policy.UpdatedAt, err = updatedAt.required("policies.updated_at")
	if err != nil {
		return model.Policy{}, fmt.Errorf("scan policy: %w", err)
	}
	return policy, nil
}

func closePolicyRowsAfterError(rows *sql.Rows, cause error) error {
	if err := rows.Close(); err != nil {
		return errors.Join(cause, fmt.Errorf("close policy rows: %w", err))
	}
	return cause
}
