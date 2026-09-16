package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/cuipengdba/agentsql/internal/model"
)

// RuleRepository provides CRUD operations for persisted rule definitions.
type RuleRepository struct {
	repositoryBase
}

// Create inserts a rule and returns the stored record.
func (repository *RuleRepository) Create(ctx context.Context, rule model.Rule) (model.Rule, error) {
	_, err := repository.db.ExecContext(ctx, repository.bind(`
INSERT INTO rules (
  id, db_type, title, risk_level, pattern_type, definition, enabled, builtin
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`),
		rule.ID,
		rule.DBType,
		rule.Title,
		rule.RiskLevel,
		rule.PatternType,
		rule.Definition,
		rule.Enabled,
		rule.Builtin,
	)
	if err != nil {
		return model.Rule{}, fmt.Errorf("create rule %q: %w", rule.ID, err)
	}
	created, err := repository.Get(ctx, rule.ID)
	if err != nil {
		return model.Rule{}, fmt.Errorf("read created rule %q: %w", rule.ID, err)
	}
	return created, nil
}

// Get returns a rule by ID.
func (repository *RuleRepository) Get(ctx context.Context, id string) (model.Rule, error) {
	rule, err := scanRule(repository.db.QueryRowContext(ctx, repository.bind(`
SELECT id, db_type, title, risk_level, pattern_type, definition, enabled, builtin,
       created_at, updated_at
FROM rules
WHERE id = ?`), id))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Rule{}, fmt.Errorf("get rule %q: %w", id, errors.Join(ErrNotFound, err))
	}
	if err != nil {
		return model.Rule{}, fmt.Errorf("get rule %q: %w", id, err)
	}
	return rule, nil
}

// List returns rules for one database type, or all rules when dbType is empty.
func (repository *RuleRepository) List(ctx context.Context, dbType string) ([]model.Rule, error) {
	if ctx == nil {
		return nil, fmt.Errorf("list rules: %w", ErrNilContext)
	}
	query := `
SELECT id, db_type, title, risk_level, pattern_type, definition, enabled, builtin,
       created_at, updated_at
FROM rules`
	args := make([]any, 0, 1)
	if dbType != "" {
		query += " WHERE db_type = ?"
		args = append(args, dbType)
	}
	query += " ORDER BY id ASC"
	rows, err := repository.db.QueryContext(ctx, repository.bind(query), args...)
	if err != nil {
		return nil, fmt.Errorf("list rules: %w", err)
	}
	rules := make([]model.Rule, 0)
	for rows.Next() {
		rule, err := scanRule(rows)
		if err != nil {
			return nil, fmt.Errorf("scan rules: %w", closeRowsAfterError(rows, err))
		}
		rules = append(rules, rule)
	}
	iterationError := rows.Err()
	closeError := rows.Close()
	if iterationError != nil || closeError != nil {
		return nil, fmt.Errorf("finish rules: %w", errors.Join(iterationError, closeError))
	}
	return rules, nil
}

// Update replaces mutable rule fields and returns the stored record.
func (repository *RuleRepository) Update(ctx context.Context, rule model.Rule) (model.Rule, error) {
	result, err := repository.db.ExecContext(ctx, repository.bind(`
UPDATE rules
SET db_type = ?, title = ?, risk_level = ?, pattern_type = ?, definition = ?,
    enabled = ?, builtin = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?`),
		rule.DBType,
		rule.Title,
		rule.RiskLevel,
		rule.PatternType,
		rule.Definition,
		rule.Enabled,
		rule.Builtin,
		rule.ID,
	)
	if err != nil {
		return model.Rule{}, fmt.Errorf("update rule %q: %w", rule.ID, err)
	}
	if err := checkRowsAffected(result, "rule", rule.ID); err != nil {
		return model.Rule{}, fmt.Errorf("update rule %q: %w", rule.ID, err)
	}
	updated, err := repository.Get(ctx, rule.ID)
	if err != nil {
		return model.Rule{}, fmt.Errorf("read updated rule %q: %w", rule.ID, err)
	}
	return updated, nil
}

// Delete removes a rule by ID.
func (repository *RuleRepository) Delete(ctx context.Context, id string) error {
	result, err := repository.db.ExecContext(ctx, repository.bind("DELETE FROM rules WHERE id = ?"), id)
	if err != nil {
		return fmt.Errorf("delete rule %q: %w", id, err)
	}
	if err := checkRowsAffected(result, "rule", id); err != nil {
		return fmt.Errorf("delete rule %q: %w", id, err)
	}
	return nil
}

func scanRule(scanner rowScanner) (model.Rule, error) {
	var rule model.Rule
	var createdAt, updatedAt databaseTimestamp
	var enabled, builtin databaseBool
	if err := scanner.Scan(
		&rule.ID,
		&rule.DBType,
		&rule.Title,
		&rule.RiskLevel,
		&rule.PatternType,
		&rule.Definition,
		&enabled,
		&builtin,
		&createdAt,
		&updatedAt,
	); err != nil {
		return model.Rule{}, fmt.Errorf("scan rule: %w", err)
	}
	rule.Enabled = enabled.value
	rule.Builtin = builtin.value

	var err error
	rule.CreatedAt, err = createdAt.required("rules.created_at")
	if err != nil {
		return model.Rule{}, fmt.Errorf("scan rule: %w", err)
	}
	rule.UpdatedAt, err = updatedAt.required("rules.updated_at")
	if err != nil {
		return model.Rule{}, fmt.Errorf("scan rule: %w", err)
	}
	return rule, nil
}
