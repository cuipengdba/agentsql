package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/cuipengdba/agentsql/internal/discovery"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	modernsqlite "modernc.org/sqlite"
)

var ErrInvalidDiscoveryDraft = errors.New("invalid discovery draft")

// IsInvalidDiscoveryDraft reports a structurally invalid discovery draft.
func IsInvalidDiscoveryDraft(err error) bool {
	return errors.Is(err, ErrInvalidDiscoveryDraft)
}

// DiscoveryDraft is one canonical datasource-scoped disabled mask-rule
// request. ID must be unique and ColumnName must already be normalized.
type DiscoveryDraft struct {
	ID                string
	SchemaName        string
	TableName         string
	ColumnName        string
	SensitiveType     string
	Algo              string
	RangeBucketWidth  *int64
	RangeBucketOffset *int64
	RangeGranularity  string
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

// ApplyDiscoveryDraftsWithAudit uses one metadata transaction for the rules
// and their audit obligation. Shared stores write audit_logs directly;
// separate stores enqueue a management audit outbox event for eventual relay.
// The process-wide mutex makes same-process retries deterministic; the unique
// index remains the final cross-process barrier.
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
	outcome, recorded, err = repository.applyDiscoveryDraftsTransaction(ctx, datasourceID, drafts, buildAudit)
	if IsMaskRuleConflict(err) {
		// A different process can win after this process plans but before it
		// inserts. Re-plan once after the unique constraint resolves the race so
		// the losing request records final existing/conflict result semantics.
		return repository.applyDiscoveryDraftsTransaction(ctx, datasourceID, drafts, buildAudit)
	}
	return outcome, recorded, err
}

func (repository *MaskRuleRepository) applyDiscoveryDraftsTransaction(
	ctx context.Context,
	datasourceID string,
	drafts []DiscoveryDraft,
	buildAudit func(DiscoveryApplyOutcome) (model.AuditLog, error),
) (outcome DiscoveryApplyOutcome, recorded model.AuditLog, err error) {
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
	if repository.auditSeparate {
		if repository.outbox == nil {
			return DiscoveryApplyOutcome{}, model.AuditLog{}, fmt.Errorf("apply discovery drafts: outbox repository is not initialized")
		}
		event, eventErr := discoveryApplyOutboxEvent(log)
		if eventErr != nil {
			return DiscoveryApplyOutcome{}, model.AuditLog{}, eventErr
		}
		if err = repository.outbox.Append(ctx, tx, event); err != nil {
			return DiscoveryApplyOutcome{}, model.AuditLog{}, fmt.Errorf("apply discovery drafts: persist audit outbox: %w", err)
		}
	} else {
		recorded, err = insertAuditLog(ctx, tx, repository.dialect, log)
		if err != nil {
			return DiscoveryApplyOutcome{}, model.AuditLog{}, fmt.Errorf("apply discovery drafts: persist audit: %w", err)
		}
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
		category := discovery.Category(strings.ToLower(strings.TrimSpace(draft.SensitiveType)))
		requested := discovery.RecommendedRule{
			SensitiveType: mask.SensitiveType(category),
			Algo:          mask.Algorithm(strings.ToLower(strings.TrimSpace(draft.Algo))),
		}
		if draft.RangeBucketWidth != nil || draft.RangeBucketOffset != nil || strings.TrimSpace(draft.RangeGranularity) != "" {
			requested.Range = &discovery.RangeHint{
				BucketWidth:  draft.RangeBucketWidth,
				BucketOffset: draft.RangeBucketOffset,
				Granularity:  strings.ToLower(strings.TrimSpace(draft.RangeGranularity)),
			}
		}
		canonical, validationErr := discovery.CanonicalizeRule(category, requested)
		if validationErr != nil {
			return DiscoveryApplyOutcome{}, fmt.Errorf("apply discovery drafts: %w: %w", ErrInvalidDiscoveryDraft, validationErr)
		}
		candidate := discoveryRuleToMaskRule(draft, column, datasourceID, canonical)
		var scoped *model.MaskRule
		var global *model.MaskRule
		for index := range listed {
			rule := &listed[index]
			if strings.ToLower(strings.TrimSpace(rule.ColumnName)) != column {
				continue
			}
			if rule.DatasourceID != nil && strings.TrimSpace(*rule.DatasourceID) == datasourceID &&
				rule.SchemaName == draft.SchemaName && rule.TableName == draft.TableName {
				scoped = rule
				break
			}
			ruleScope := ""
			if rule.DatasourceID != nil {
				ruleScope = strings.TrimSpace(*rule.DatasourceID)
			}
			if (ruleScope == "" || ruleScope == datasourceID) && rule.SchemaName == "" && rule.TableName == "" && rule.Enabled {
				global = rule
			}
		}
		if scoped != nil {
			if discoveryRulesEquivalent(*scoped, canonical) {
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
		outcome.Created = append(outcome.Created, candidate)
		listed = append(listed, outcome.Created[len(outcome.Created)-1])
	}
	if len(outcome.Conflicts) != 0 {
		outcome.Created = []model.MaskRule{}
	}
	return outcome, nil
}

func listDiscoveryScopeRules(ctx context.Context, executor sqlExecutor, dialect Dialect, datasourceID string) ([]model.MaskRule, error) {
	query := repositoryBase{dialect: dialect}.bind(`
SELECT id, datasource_id, COALESCE(schema_name, ''), COALESCE(table_name, ''), column_name, sensitive_type, algo,
       created_at, updated_at, enabled, range_bucket_width, range_bucket_offset, range_granularity
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
INSERT INTO mask_rules (
  id, datasource_id, schema_name, table_name, column_name, sensitive_type, algo, enabled,
  range_bucket_width, range_bucket_offset, range_granularity
)
VALUES (?, ?, COALESCE(?, ''), COALESCE(?, ''), ?, ?, ?, ?, ?, ?, ?)`),
		rule.ID, optionalString(rule.DatasourceID), rule.SchemaName, rule.TableName,
		rule.ColumnName, rule.SensitiveType, rule.Algo, false,
		rule.RangeBucketWidth, rule.RangeBucketOffset, rule.RangeGranularity)
	if err != nil {
		if isMaskScopeUniqueViolation(err) {
			return maskRuleConflictError("apply discovery draft", rule, err)
		}
		return fmt.Errorf("apply discovery draft %q: %w", rule.ID, err)
	}
	return nil
}

func discoveryRuleToMaskRule(draft DiscoveryDraft, column, datasourceID string, rule discovery.RecommendedRule) model.MaskRule {
	created := model.MaskRule{
		ID: draft.ID, DatasourceID: &datasourceID, SchemaName: draft.SchemaName,
		TableName: draft.TableName, ColumnName: column,
		SensitiveType: string(rule.SensitiveType), Algo: string(rule.Algo), Enabled: false,
	}
	if rule.Range != nil {
		created.RangeBucketWidth = copyInt64Pointer(rule.Range.BucketWidth)
		created.RangeBucketOffset = copyInt64Pointer(rule.Range.BucketOffset)
		if rule.Range.Granularity != "" {
			granularity := rule.Range.Granularity
			created.RangeGranularity = &granularity
		}
	}
	return created
}

func discoveryRulesEquivalent(existing model.MaskRule, desired discovery.RecommendedRule) bool {
	existingRule := discovery.RecommendedRule{
		SensitiveType: mask.SensitiveType(strings.ToLower(strings.TrimSpace(existing.SensitiveType))),
		Algo:          mask.Algorithm(strings.ToLower(strings.TrimSpace(existing.Algo))),
	}
	if existing.RangeBucketWidth != nil || existing.RangeBucketOffset != nil || existing.RangeGranularity != nil {
		existingRule.Range = &discovery.RangeHint{
			BucketWidth:  copyInt64Pointer(existing.RangeBucketWidth),
			BucketOffset: copyInt64Pointer(existing.RangeBucketOffset),
		}
		if existing.RangeGranularity != nil {
			existingRule.Range.Granularity = strings.ToLower(strings.TrimSpace(*existing.RangeGranularity))
		}
	}
	canonical, err := discovery.CanonicalizeRule(discovery.Category(existingRule.SensitiveType), existingRule)
	if err != nil {
		return false
	}
	return sameDiscoveryRule(canonical, desired)
}

func sameDiscoveryRule(left, right discovery.RecommendedRule) bool {
	if left.SensitiveType != right.SensitiveType || left.Algo != right.Algo {
		return false
	}
	if left.Range == nil || right.Range == nil {
		return left.Range == nil && right.Range == nil
	}
	return sameInt64Value(left.Range.BucketWidth, right.Range.BucketWidth) &&
		sameInt64Value(left.Range.BucketOffset, right.Range.BucketOffset) &&
		left.Range.Granularity == right.Range.Granularity
}

func sameInt64Value(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func copyInt64Pointer(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func discoveryApplyOutboxEvent(log model.AuditLog) (model.ManagementAuditOutbox, error) {
	if log.Action == nil || log.ActorType == nil || log.ActorID == nil || log.DetailsJSON == nil {
		return model.ManagementAuditOutbox{}, fmt.Errorf("apply discovery drafts: audit action, actor, and details are required")
	}
	return model.ManagementAuditOutbox{
		EventUUID: uuid.NewString(), Action: "discover_apply",
		ActorType: *log.ActorType, ActorID: *log.ActorID, DetailsJSON: *log.DetailsJSON,
	}, nil
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
