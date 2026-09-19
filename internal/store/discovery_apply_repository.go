package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/jackc/pgx/v5/pgconn"
	modernsqlite "modernc.org/sqlite"
)

var ErrInvalidDiscoveryDraft = errors.New("invalid discovery draft")

// IsInvalidDiscoveryDraft reports a structurally invalid or non-mask draft.
func IsInvalidDiscoveryDraft(err error) bool {
	return errors.Is(err, ErrInvalidDiscoveryDraft)
}

// DiscoveryDraft is one canonical datasource-scoped disabled mask-rule
// request. ID must be unique and ColumnName must already be normalized.
type DiscoveryDraft struct {
	ID            string
	ColumnName    string
	SensitiveType string
	Algo          string
}

// DiscoveryApplyOutcome contains only persisted rules grouped by the fixed
// idempotency semantics. Conflicts cause the whole metadata mutation to be
// skipped.
type DiscoveryApplyOutcome struct {
	Created         []model.MaskRule
	Existing        []model.MaskRule
	CoveredByGlobal []model.MaskRule
	Conflicts       []model.MaskRule
}

// ApplyDiscoveryDraftsWithAudit uses one transaction when audit and metadata
// share a store. With a separate audit store it writes the immutable audit
// first, then applies metadata in a transaction, matching the approval saga's
// fail-closed ordering. The process-wide mutex makes same-process retries and
// concurrent identical requests deterministic; the unique index remains the
// final database-level barrier.
func (repository *MaskRuleRepository) ApplyDiscoveryDraftsWithAudit(
	ctx context.Context,
	datasourceID string,
	drafts []DiscoveryDraft,
	buildAudit func(DiscoveryApplyOutcome) (model.AuditLog, error),
) (outcome DiscoveryApplyOutcome, recorded model.AuditLog, err error) {
	if repository == nil || repository.db == nil || ctx == nil || strings.TrimSpace(datasourceID) == "" || len(drafts) == 0 || buildAudit == nil {
		return outcome, recorded, fmt.Errorf("apply discovery drafts: invalid input")
	}
	if repository.applyMu != nil {
		repository.applyMu.Lock()
		defer repository.applyMu.Unlock()
	}
	if repository.auditSeparate {
		outcome, err = repository.planDiscoveryDrafts(ctx, repository.db, datasourceID, drafts)
		if err != nil {
			return outcome, recorded, err
		}
		log, err := buildAudit(outcome)
		if err != nil {
			return outcome, recorded, err
		}
		if repository.auditDB == nil {
			return outcome, recorded, fmt.Errorf("apply discovery drafts: audit repository is not initialized")
		}
		recorded, err = insertAuditLog(ctx, repository.auditDB, repository.auditDialect, log)
		if err != nil {
			return outcome, model.AuditLog{}, fmt.Errorf("apply discovery drafts: persist audit: %w", err)
		}
		if len(outcome.Conflicts) != 0 || len(outcome.Created) == 0 {
			return outcome, recorded, nil
		}
		if err := repository.insertDiscoveryDraftsTransaction(ctx, outcome.Created); err != nil {
			// The immutable audit remains as an orphan, exactly like approval's
			// separate-store audit-first saga.
			return DiscoveryApplyOutcome{}, recorded, err
		}
		return outcome, recorded, nil
	}

	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return outcome, recorded, fmt.Errorf("apply discovery drafts: begin transaction: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, rollbackErr)
			outcome = DiscoveryApplyOutcome{}
			recorded = model.AuditLog{}
		}
	}()
	outcome, err = repository.planDiscoveryDrafts(ctx, tx, datasourceID, drafts)
	if err != nil {
		return outcome, recorded, err
	}
	if len(outcome.Conflicts) == 0 {
		for _, rule := range outcome.Created {
			if err := insertDiscoveryDraft(ctx, tx, repository.dialect, rule); err != nil {
				return DiscoveryApplyOutcome{}, recorded, err
			}
		}
	} else {
		outcome.Created = []model.MaskRule{}
	}
	log, err := buildAudit(outcome)
	if err != nil {
		return DiscoveryApplyOutcome{}, recorded, err
	}
	recorded, err = insertAuditLog(ctx, tx, repository.dialect, log)
	if err != nil {
		return DiscoveryApplyOutcome{}, model.AuditLog{}, fmt.Errorf("apply discovery drafts: persist audit: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return DiscoveryApplyOutcome{}, model.AuditLog{}, fmt.Errorf("apply discovery drafts: commit: %w", err)
	}
	committed = true
	return outcome, recorded, nil
}

func (repository *MaskRuleRepository) planDiscoveryDrafts(ctx context.Context, executor sqlExecutor, datasourceID string, drafts []DiscoveryDraft) (DiscoveryApplyOutcome, error) {
	listed, err := listDiscoveryScopeRules(ctx, executor, repository.dialect, datasourceID)
	if err != nil {
		return DiscoveryApplyOutcome{}, err
	}
	outcome := DiscoveryApplyOutcome{Created: []model.MaskRule{}, Existing: []model.MaskRule{}, CoveredByGlobal: []model.MaskRule{}, Conflicts: []model.MaskRule{}}
	for _, draft := range drafts {
		column := strings.ToLower(strings.TrimSpace(draft.ColumnName))
		if column == "" || draft.ID == "" {
			return DiscoveryApplyOutcome{}, fmt.Errorf("apply discovery drafts: %w", ErrInvalidDiscoveryDraft)
		}
		rule := mask.Rule{
			Column:        column,
			SensitiveType: mask.SensitiveType(draft.SensitiveType),
			Algorithm:     mask.Algorithm(draft.Algo),
		}
		if validationErr := mask.ValidateRule(rule); validationErr != nil {
			return DiscoveryApplyOutcome{}, fmt.Errorf("apply discovery drafts: %w: %w", ErrInvalidDiscoveryDraft, validationErr)
		}
		if rule.Algorithm != mask.AlgoMask {
			return DiscoveryApplyOutcome{}, fmt.Errorf(
				"apply discovery drafts: %w: %w",
				ErrInvalidDiscoveryDraft,
				mask.ErrUnsupportedAlgorithm,
			)
		}
		var scoped *model.MaskRule
		var global *model.MaskRule
		for index := range listed {
			rule := &listed[index]
			if strings.ToLower(strings.TrimSpace(rule.ColumnName)) != column {
				continue
			}
			if rule.DatasourceID != nil && strings.TrimSpace(*rule.DatasourceID) == datasourceID {
				scoped = rule
				break
			}
			if (rule.DatasourceID == nil || strings.TrimSpace(*rule.DatasourceID) == "") && rule.Enabled {
				global = rule
			}
		}
		if scoped != nil {
			if scoped.SensitiveType == draft.SensitiveType && scoped.Algo == draft.Algo {
				outcome.Existing = append(outcome.Existing, *scoped)
			} else {
				outcome.Conflicts = append(outcome.Conflicts, *scoped)
			}
			continue
		}
		if global != nil {
			outcome.CoveredByGlobal = append(outcome.CoveredByGlobal, *global)
			continue
		}
		scope := datasourceID
		outcome.Created = append(outcome.Created, model.MaskRule{ID: draft.ID, DatasourceID: &scope, TableName: "", ColumnName: column, SensitiveType: draft.SensitiveType, Algo: draft.Algo, Enabled: false})
	}
	if len(outcome.Conflicts) != 0 {
		outcome.Created = []model.MaskRule{}
	}
	return outcome, nil
}

func listDiscoveryScopeRules(ctx context.Context, executor sqlExecutor, dialect Dialect, datasourceID string) ([]model.MaskRule, error) {
	query := repositoryBase{dialect: dialect}.bind(`
SELECT id, datasource_id, table_name, column_name, sensitive_type, algo, enabled,
       created_at, updated_at
FROM mask_rules
WHERE datasource_id = ? OR datasource_id IS NULL OR TRIM(datasource_id) = ''
ORDER BY id`)
	rows, err := executor.QueryContext(ctx, query, datasourceID)
	if err != nil {
		return nil, fmt.Errorf("apply discovery drafts: list rules: %w", err)
	}
	defer rows.Close()
	rules := make([]model.MaskRule, 0)
	for rows.Next() {
		rule, err := scanMaskRule(rows)
		if err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return rules, nil
}

func insertDiscoveryDraft(ctx context.Context, executor sqlExecutor, dialect Dialect, rule model.MaskRule) error {
	_, err := executor.ExecContext(ctx, repositoryBase{dialect: dialect}.bind(`
INSERT INTO mask_rules (id, datasource_id, table_name, column_name, sensitive_type, algo, enabled)
VALUES (?, ?, ?, ?, ?, ?, ?)`), rule.ID, optionalString(rule.DatasourceID), "", rule.ColumnName, rule.SensitiveType, rule.Algo, false)
	if err != nil {
		if isMaskScopeUniqueViolation(err) {
			return maskRuleConflictError("apply discovery draft", rule, err)
		}
		return fmt.Errorf("apply discovery draft %q: %w", rule.ID, err)
	}
	return nil
}

func isMaskScopeUniqueViolation(err error) bool {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		return postgresError.Code == "23505" && postgresError.ConstraintName == "ux_mask_rules_scope_column"
	}
	var sqliteError *modernsqlite.Error
	if errors.As(err, &sqliteError) {
		return sqliteError.Code()&0xff == 19 && strings.Contains(strings.ToLower(sqliteError.Error()), "ux_mask_rules_scope_column")
	}
	return false
}

func (repository *MaskRuleRepository) insertDiscoveryDraftsTransaction(ctx context.Context, rules []model.MaskRule) (err error) {
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	for _, rule := range rules {
		if err = insertDiscoveryDraft(ctx, tx, repository.dialect, rule); err != nil {
			return err
		}
	}
	return tx.Commit()
}
