package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/cuipengdba/agentsql/internal/model"
)

// MaskRuleRepository provides CRUD operations for mask rules.
type MaskRuleRepository struct {
	db *sql.DB
}

// Create inserts a mask rule and returns the stored record.
func (repository *MaskRuleRepository) Create(ctx context.Context, rule model.MaskRule) (model.MaskRule, error) {
	_, err := repository.db.ExecContext(ctx, `
INSERT INTO mask_rules (
  id, datasource_id, table_name, column_name, sensitive_type, algo
)
VALUES (?, ?, ?, ?, ?, ?)`,
		rule.ID,
		optionalString(rule.DatasourceID),
		rule.TableName,
		rule.ColumnName,
		rule.SensitiveType,
		rule.Algo,
	)
	if err != nil {
		return model.MaskRule{}, fmt.Errorf("create mask rule %q: %w", rule.ID, err)
	}
	created, err := repository.Get(ctx, rule.ID)
	if err != nil {
		return model.MaskRule{}, fmt.Errorf("read created mask rule %q: %w", rule.ID, err)
	}
	return created, nil
}

// Get returns a mask rule by ID.
func (repository *MaskRuleRepository) Get(ctx context.Context, id string) (model.MaskRule, error) {
	rule, err := scanMaskRule(repository.db.QueryRowContext(ctx, `
SELECT id, datasource_id, table_name, column_name, sensitive_type, algo,
       created_at, updated_at
FROM mask_rules
WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return model.MaskRule{}, fmt.Errorf("get mask rule %q: %w", id, errors.Join(ErrNotFound, err))
	}
	if err != nil {
		return model.MaskRule{}, fmt.Errorf("get mask rule %q: %w", id, err)
	}
	return rule, nil
}

// ListByDatasource returns datasource-specific and global mask rules in stable
// table and column order.
func (repository *MaskRuleRepository) ListByDatasource(
	ctx context.Context,
	datasourceID string,
) ([]model.MaskRule, error) {
	if ctx == nil {
		return nil, fmt.Errorf("list mask rules: %w", ErrNilContext)
	}
	rows, err := repository.db.QueryContext(ctx, `
SELECT id, datasource_id, table_name, column_name, sensitive_type, algo,
       created_at, updated_at
FROM mask_rules
WHERE datasource_id = ? OR datasource_id IS NULL OR TRIM(datasource_id) = ''
ORDER BY table_name ASC, column_name ASC, id ASC`, datasourceID)
	if err != nil {
		return nil, fmt.Errorf("list mask rules for datasource %q: %w", datasourceID, err)
	}
	rules := make([]model.MaskRule, 0)
	for rows.Next() {
		rule, err := scanMaskRule(rows)
		if err != nil {
			return nil, fmt.Errorf(
				"scan mask rules for datasource %q: %w",
				datasourceID,
				closeMaskRuleRowsAfterError(rows, err),
			)
		}
		rules = append(rules, rule)
	}
	iterationError := rows.Err()
	closeError := rows.Close()
	if iterationError != nil || closeError != nil {
		return nil, fmt.Errorf(
			"finish mask rules for datasource %q: %w",
			datasourceID,
			errors.Join(iterationError, closeError),
		)
	}
	return rules, nil
}

// List returns all mask rules ordered by table and column.
func (repository *MaskRuleRepository) List(ctx context.Context) ([]model.MaskRule, error) {
	if ctx == nil {
		return nil, fmt.Errorf("list mask rules: %w", ErrNilContext)
	}
	rows, err := repository.db.QueryContext(ctx, `
SELECT id, datasource_id, table_name, column_name, sensitive_type, algo,
       created_at, updated_at
FROM mask_rules
ORDER BY table_name ASC, column_name ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list mask rules: %w", err)
	}
	rules := make([]model.MaskRule, 0)
	for rows.Next() {
		rule, err := scanMaskRule(rows)
		if err != nil {
			return nil, fmt.Errorf("scan mask rules: %w", closeMaskRuleRowsAfterError(rows, err))
		}
		rules = append(rules, rule)
	}
	iterationError := rows.Err()
	closeError := rows.Close()
	if iterationError != nil || closeError != nil {
		return nil, fmt.Errorf("finish mask rules: %w", errors.Join(iterationError, closeError))
	}
	return rules, nil
}

// Update replaces mutable mask-rule fields and returns the stored record.
func (repository *MaskRuleRepository) Update(ctx context.Context, rule model.MaskRule) (model.MaskRule, error) {
	result, err := repository.db.ExecContext(ctx, `
UPDATE mask_rules
SET datasource_id = ?, table_name = ?, column_name = ?, sensitive_type = ?,
    algo = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?`,
		optionalString(rule.DatasourceID),
		rule.TableName,
		rule.ColumnName,
		rule.SensitiveType,
		rule.Algo,
		rule.ID,
	)
	if err != nil {
		return model.MaskRule{}, fmt.Errorf("update mask rule %q: %w", rule.ID, err)
	}
	if err := checkRowsAffected(result, "mask rule", rule.ID); err != nil {
		return model.MaskRule{}, fmt.Errorf("update mask rule %q: %w", rule.ID, err)
	}
	updated, err := repository.Get(ctx, rule.ID)
	if err != nil {
		return model.MaskRule{}, fmt.Errorf("read updated mask rule %q: %w", rule.ID, err)
	}
	return updated, nil
}

// Delete removes a mask rule by ID.
func (repository *MaskRuleRepository) Delete(ctx context.Context, id string) error {
	result, err := repository.db.ExecContext(ctx, "DELETE FROM mask_rules WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete mask rule %q: %w", id, err)
	}
	if err := checkRowsAffected(result, "mask rule", id); err != nil {
		return fmt.Errorf("delete mask rule %q: %w", id, err)
	}
	return nil
}

func scanMaskRule(scanner rowScanner) (model.MaskRule, error) {
	var rule model.MaskRule
	var datasourceID sql.NullString
	var createdAt, updatedAt databaseTimestamp
	if err := scanner.Scan(
		&rule.ID,
		&datasourceID,
		&rule.TableName,
		&rule.ColumnName,
		&rule.SensitiveType,
		&rule.Algo,
		&createdAt,
		&updatedAt,
	); err != nil {
		return model.MaskRule{}, fmt.Errorf("scan mask rule: %w", err)
	}

	rule.DatasourceID = stringPointer(datasourceID)
	var err error
	rule.CreatedAt, err = createdAt.required("mask_rules.created_at")
	if err != nil {
		return model.MaskRule{}, fmt.Errorf("scan mask rule: %w", err)
	}
	rule.UpdatedAt, err = updatedAt.required("mask_rules.updated_at")
	if err != nil {
		return model.MaskRule{}, fmt.Errorf("scan mask rule: %w", err)
	}
	return rule, nil
}

func closeMaskRuleRowsAfterError(rows *sql.Rows, cause error) error {
	if err := rows.Close(); err != nil {
		return errors.Join(cause, fmt.Errorf("close mask rule rows: %w", err))
	}
	return cause
}
