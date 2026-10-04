package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
)

// DashboardRepository executes read-only dashboard aggregates.
type DashboardRepository struct {
	meta  repositoryBase
	audit repositoryBase
	now   func() time.Time
}

// NewDashboardRepository returns a dashboard repository that reads metadata
// and immutable audit aggregates from their respective targets.
func NewDashboardRepository(
	metaDB, auditDB *sql.DB,
	metaDriver, auditDriver Dialect,
) *DashboardRepository {
	return &DashboardRepository{
		meta:  repositoryBase{db: metaDB, dialect: metaDriver},
		audit: repositoryBase{db: auditDB, dialect: auditDriver},
	}
}

type DashboardSummary struct {
	KPI                  DashboardKPI       `json:"kpi"`
	Trend14D             []TrendDay         `json:"trend_14d"`
	DecisionDistribution []DecisionCount    `json:"decision_distribution"`
	RiskTop              []RiskTopEntry     `json:"risk_top"`
	AgentRanking         []AgentRankingItem `json:"agent_ranking"`
	BattleReport         BattleReport       `json:"battle_report"`
}

type DashboardKPI struct {
	TotalRequests      int64    `json:"total_requests"`
	Blocked            int64    `json:"blocked"`
	PendingApprovals   int64    `json:"pending_approvals"`
	ActiveAgents       int64    `json:"active_agents"`
	DatasourcesTotal   int64    `json:"datasources_total"`
	TotalRequestsDelta *float64 `json:"total_requests_change_pct"`
	BlockedDelta       *float64 `json:"blocked_change_pct"`
}

type TrendDay struct {
	Date    string `json:"date"`
	Total   int64  `json:"total"`
	Deny    int64  `json:"deny"`
	Warn    int64  `json:"warn"`
	Approve int64  `json:"approve"`
	Allow   int64  `json:"allow"`
}

type DecisionCount struct {
	Decision string `json:"decision"`
	Count    int64  `json:"count"`
}

type RiskTopEntry struct {
	RuleID string `json:"rule_id"`
	Count  int64  `json:"count"`
}

type AgentRankingItem struct {
	AgentID      string `json:"agent_id"`
	Name         string `json:"name"`
	BlockedCount int64  `json:"blocked_count"`
}

type BattleReport struct {
	BlockedCount int64 `json:"blocked_count"`
	EstRowsSaved int64 `json:"est_rows_saved"`
}

// Summary returns one read-only dashboard snapshot for the trailing days.
func (repository *DashboardRepository) Summary(ctx context.Context, days int) (DashboardSummary, error) {
	if repository == nil || repository.meta.db == nil || repository.audit.db == nil {
		return DashboardSummary{}, fmt.Errorf("dashboard repository is not initialized")
	}
	if ctx == nil {
		return DashboardSummary{}, fmt.Errorf("dashboard summary: %w", ErrNilContext)
	}
	if days < 1 || days > 90 {
		return DashboardSummary{}, fmt.Errorf("dashboard summary days must be between 1 and 90")
	}
	tenantID, err := repository.meta.requireTenant(ctx, "dashboard summary")
	if err != nil {
		return DashboardSummary{}, err
	}
	now := time.Now()
	if repository.now != nil {
		now = repository.now()
	}
	now = now.UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	startTime := today.AddDate(0, 0, -(days - 1))
	endTime := today.AddDate(0, 0, 1)
	start := startTime
	end := endTime
	previousStart := startTime.AddDate(0, 0, -days)
	previousEnd := start
	total, blocked, err := repository.auditCounts(ctx, start, end)
	if err != nil {
		return DashboardSummary{}, err
	}
	previousTotal, previousBlocked, err := repository.auditCounts(ctx, previousStart, previousEnd)
	if err != nil {
		return DashboardSummary{}, err
	}
	pending, err := repository.metaCount(ctx, "SELECT COUNT(*) FROM approvals WHERE tenant_id = ? AND status = ?", tenantID, "pending")
	if err != nil {
		return DashboardSummary{}, fmt.Errorf("dashboard pending approvals: %w", err)
	}
	activeAgents, err := repository.metaCount(ctx, "SELECT COUNT(*) FROM agents WHERE tenant_id = ? AND status = ?", tenantID, "active")
	if err != nil {
		return DashboardSummary{}, fmt.Errorf("dashboard active agents: %w", err)
	}
	datasources, err := repository.metaCount(ctx, "SELECT COUNT(*) FROM datasources WHERE tenant_id = ?", tenantID)
	if err != nil {
		return DashboardSummary{}, fmt.Errorf("dashboard datasources: %w", err)
	}
	trend, err := repository.trend(ctx, startTime, start, end, days)
	if err != nil {
		return DashboardSummary{}, err
	}
	distribution, err := repository.decisionDistribution(ctx, start, end)
	if err != nil {
		return DashboardSummary{}, err
	}
	riskTop, err := repository.riskTop(ctx, start, end)
	if err != nil {
		return DashboardSummary{}, err
	}
	ranking, err := repository.agentRanking(ctx, start, end)
	if err != nil {
		return DashboardSummary{}, err
	}
	blockedCount, savedRows, err := repository.battleReport(ctx, start, end)
	if err != nil {
		return DashboardSummary{}, err
	}
	return DashboardSummary{
		KPI: DashboardKPI{
			TotalRequests: total, Blocked: blocked, PendingApprovals: pending,
			ActiveAgents: activeAgents, DatasourcesTotal: datasources,
			TotalRequestsDelta: percentageChange(previousTotal, total),
			BlockedDelta:       percentageChange(previousBlocked, blocked),
		},
		Trend14D: trend, DecisionDistribution: distribution, RiskTop: riskTop,
		AgentRanking: ranking, BattleReport: BattleReport{BlockedCount: blockedCount, EstRowsSaved: savedRows},
	}, nil
}

func (repository *DashboardRepository) metaCount(ctx context.Context, query string, args ...any) (int64, error) {
	var result int64
	if err := repository.meta.db.QueryRowContext(ctx, repository.meta.bind(query), args...).Scan(&result); err != nil {
		return 0, err
	}
	return result, nil
}

func (repository *DashboardRepository) auditCounts(ctx context.Context, start, end time.Time) (int64, int64, error) {
	tenantID, err := repository.audit.requireTenant(ctx, "dashboard audit counts")
	if err != nil {
		return 0, 0, err
	}
	var total, blocked int64
	if err := repository.audit.db.QueryRowContext(ctx, repository.audit.bind(`
SELECT COUNT(*), COALESCE(SUM(CASE WHEN decision = 'deny' THEN 1 ELSE 0 END), 0)
FROM audit_logs WHERE tenant_id = ? AND ts >= ? AND ts < ?`), tenantID, start, end).Scan(&total, &blocked); err != nil {
		return 0, 0, fmt.Errorf("dashboard audit counts: %w", err)
	}
	return total, blocked, nil
}

func (repository *DashboardRepository) trend(
	ctx context.Context,
	startTime time.Time,
	start time.Time,
	end time.Time,
	days int,
) ([]TrendDay, error) {
	tenantID, tenantErr := repository.audit.requireTenant(ctx, "dashboard trend")
	if tenantErr != nil {
		return nil, tenantErr
	}
	// substr(ts,1,19) trims fractional seconds/timezone so SQLite date() does not
	// return NULL for RFC3339Nano values written via a parameterized time.Time;
	// CURRENT_TIMESTAMP text is normalized identically.
	selectExpression := auditDaySelect(repository.audit.dialect)
	groupExpression := auditDayGroupBy(repository.audit.dialect)
	query := `
SELECT ` + selectExpression + `, COUNT(*),
       COALESCE(SUM(CASE WHEN decision = 'deny' THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN decision = 'warn' THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN decision = 'approve' THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN decision = 'allow' THEN 1 ELSE 0 END), 0)
FROM audit_logs WHERE tenant_id = ? AND ts >= ? AND ts < ? GROUP BY ` + groupExpression + ` ORDER BY ` + groupExpression + ` ASC`
	rows, err := repository.audit.db.QueryContext(ctx, repository.audit.bind(query), tenantID, start, end)
	if err != nil {
		return nil, fmt.Errorf("dashboard trend: %w", err)
	}
	byDate := make(map[string]TrendDay)
	for rows.Next() {
		var (
			day  TrendDay
			date sql.NullString
		)
		if err := rows.Scan(&date, &day.Total, &day.Deny, &day.Warn, &day.Approve, &day.Allow); err != nil {
			return nil, fmt.Errorf("scan dashboard trend: %w", closeRowsAfterError(rows, err))
		}
		if !date.Valid || date.String == "" {
			continue
		}
		day.Date = date.String
		byDate[day.Date] = day
	}
	iterationError := rows.Err()
	closeError := rows.Close()
	if iterationError != nil || closeError != nil {
		return nil, fmt.Errorf("finish dashboard trend: %w", errors.Join(iterationError, closeError))
	}
	result := make([]TrendDay, 0, days)
	first := time.Date(startTime.Year(), startTime.Month(), startTime.Day(), 0, 0, 0, 0, time.UTC)
	for index := 0; index < days; index++ {
		date := first.AddDate(0, 0, index).Format("2006-01-02")
		day := byDate[date]
		day.Date = date
		result = append(result, day)
	}
	return result, nil
}

func (repository *DashboardRepository) decisionDistribution(ctx context.Context, start, end time.Time) ([]DecisionCount, error) {
	tenantID, tenantErr := repository.audit.requireTenant(ctx, "dashboard decision distribution")
	if tenantErr != nil {
		return nil, tenantErr
	}
	counts := map[string]int64{"allow": 0, "warn": 0, "approve": 0, "deny": 0}
	rows, err := repository.audit.db.QueryContext(ctx, repository.audit.bind(`
SELECT decision, COUNT(*) FROM audit_logs WHERE tenant_id = ? AND ts >= ? AND ts < ? GROUP BY decision`), tenantID, start, end)
	if err != nil {
		return nil, fmt.Errorf("dashboard decision distribution: %w", err)
	}
	for rows.Next() {
		var decision string
		var count int64
		if err := rows.Scan(&decision, &count); err != nil {
			return nil, fmt.Errorf("scan dashboard decision distribution: %w", closeRowsAfterError(rows, err))
		}
		if _, exists := counts[decision]; exists {
			counts[decision] = count
		}
	}
	iterationError := rows.Err()
	closeError := rows.Close()
	if iterationError != nil || closeError != nil {
		return nil, fmt.Errorf("finish dashboard decision distribution: %w", errors.Join(iterationError, closeError))
	}
	return []DecisionCount{
		{Decision: "allow", Count: counts["allow"]},
		{Decision: "deny", Count: counts["deny"]},
		{Decision: "approve", Count: counts["approve"]},
		{Decision: "warn", Count: counts["warn"]},
	}, nil
}

func (repository *DashboardRepository) riskTop(ctx context.Context, start, end time.Time) ([]RiskTopEntry, error) {
	tenantID, tenantErr := repository.audit.requireTenant(ctx, "dashboard risk top")
	if tenantErr != nil {
		return nil, tenantErr
	}
	rows, err := repository.audit.db.QueryContext(ctx, repository.audit.bind(`
SELECT rule_hits FROM audit_logs WHERE tenant_id = ? AND ts >= ? AND ts < ? AND rule_hits IS NOT NULL`), tenantID, start, end)
	if err != nil {
		return nil, fmt.Errorf("dashboard risk top: %w", err)
	}
	counts := make(map[string]int64)
	for rows.Next() {
		var encoded string
		if err := rows.Scan(&encoded); err != nil {
			return nil, fmt.Errorf("scan dashboard risk hits: %w", closeRowsAfterError(rows, err))
		}
		var hits []model.RuleHit
		if err := json.Unmarshal([]byte(encoded), &hits); err != nil {
			continue
		}
		for _, hit := range hits {
			if strings.TrimSpace(hit.RuleID) != "" && hit.Decision != model.DecisionAllow {
				counts[hit.RuleID]++
			}
		}
	}
	iterationError := rows.Err()
	closeError := rows.Close()
	if iterationError != nil || closeError != nil {
		return nil, fmt.Errorf("finish dashboard risk top: %w", errors.Join(iterationError, closeError))
	}
	result := make([]RiskTopEntry, 0, len(counts))
	for ruleID, count := range counts {
		result = append(result, RiskTopEntry{RuleID: ruleID, Count: count})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Count == result[j].Count {
			return result[i].RuleID < result[j].RuleID
		}
		return result[i].Count > result[j].Count
	})
	if len(result) > 5 {
		result = result[:5]
	}
	return result, nil
}

func (repository *DashboardRepository) agentRanking(ctx context.Context, start, end time.Time) ([]AgentRankingItem, error) {
	tenantID, tenantErr := repository.audit.requireTenant(ctx, "dashboard agent ranking")
	if tenantErr != nil {
		return nil, tenantErr
	}
	rows, err := repository.audit.db.QueryContext(ctx, repository.audit.bind(`
SELECT agent_id, COUNT(*) FROM audit_logs
WHERE tenant_id = ? AND ts >= ? AND ts < ? AND decision = 'deny' AND agent_id IS NOT NULL
GROUP BY agent_id ORDER BY COUNT(*) DESC, agent_id ASC LIMIT 5`), tenantID, start, end)
	if err != nil {
		return nil, fmt.Errorf("dashboard agent ranking: %w", err)
	}
	type ranking struct {
		id    string
		count int64
	}
	ids := make([]ranking, 0, 5)
	for rows.Next() {
		var value ranking
		if err := rows.Scan(&value.id, &value.count); err != nil {
			return nil, fmt.Errorf("scan dashboard agent ranking: %w", closeRowsAfterError(rows, err))
		}
		ids = append(ids, value)
	}
	iterationError := rows.Err()
	closeError := rows.Close()
	if iterationError != nil || closeError != nil {
		return nil, fmt.Errorf("finish dashboard agent ranking: %w", errors.Join(iterationError, closeError))
	}
	if len(ids) == 0 {
		return []AgentRankingItem{}, nil
	}
	arguments := make([]any, 0, len(ids)+1)
	arguments = append(arguments, tenantID)
	for index, value := range ids {
		_ = index
		arguments = append(arguments, value.id)
	}
	nameRows, err := repository.meta.db.QueryContext(
		ctx,
		repository.meta.bind("SELECT id, name FROM agents WHERE tenant_id = ? AND id IN ("+auditPlaceholders(len(ids))+")"),
		arguments...,
	)
	if err != nil {
		return nil, fmt.Errorf("read dashboard agent names: %w", err)
	}
	names := make(map[string]string, len(ids))
	for nameRows.Next() {
		var id, name string
		if err := nameRows.Scan(&id, &name); err != nil {
			return nil, fmt.Errorf("scan dashboard agent names: %w", closeRowsAfterError(nameRows, err))
		}
		names[id] = name
	}
	iterationError = nameRows.Err()
	closeError = nameRows.Close()
	if iterationError != nil || closeError != nil {
		return nil, fmt.Errorf("finish dashboard agent names: %w", errors.Join(iterationError, closeError))
	}
	result := make([]AgentRankingItem, 0, len(ids))
	for _, value := range ids {
		result = append(result, AgentRankingItem{
			AgentID: value.id, Name: names[value.id], BlockedCount: value.count,
		})
	}
	return result, nil
}

func (repository *DashboardRepository) battleReport(ctx context.Context, start, end time.Time) (int64, int64, error) {
	tenantID, tenantErr := repository.audit.requireTenant(ctx, "dashboard battle report")
	if tenantErr != nil {
		return 0, 0, tenantErr
	}
	var blocked, rowsSaved int64
	query := `
SELECT COUNT(*), COALESCE(SUM(COALESCE(est_rows, 0)), 0)
FROM audit_logs WHERE tenant_id = ? AND ts >= ? AND ts < ? AND decision = 'deny'`
	if repository.audit.dialect == DialectPostgres {
		query = `
SELECT COUNT(*), CAST(COALESCE(SUM(est_rows), 0) AS BIGINT)
FROM audit_logs WHERE tenant_id = ? AND ts >= ? AND ts < ? AND decision = 'deny'`
	}
	if err := repository.audit.db.QueryRowContext(
		ctx, repository.audit.bind(query), tenantID, start, end,
	).Scan(&blocked, &rowsSaved); err != nil {
		return 0, 0, fmt.Errorf("dashboard battle report: %w", err)
	}
	return blocked, rowsSaved, nil
}

func percentageChange(previous, current int64) *float64 {
	if previous == 0 {
		return nil
	}
	change := (float64(current-previous) / float64(previous)) * 100
	return &change
}
