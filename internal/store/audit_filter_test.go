package store

import (
	"context"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestAuditLogRepositoryFilteredPage(t *testing.T) {
	opened := openTestStore(t)
	repository := opened.AuditLogs()
	base := time.Date(2026, time.September, 9, 8, 0, 0, 0, time.UTC)
	type fixture struct {
		agent      string
		datasource string
		session    string
		tool       string
		decision   string
		statement  string
		risk       int
		raw        string
		normalized string
		objects    string
		timestamp  time.Time
	}
	fixtures := []fixture{
		{agent: "agent-a", datasource: "ds-1", session: "s-1", tool: "query", decision: "allow", statement: "SELECT", risk: 4, raw: "SELECT email FROM customers /* 100%_match */", normalized: "SELECT email FROM customers", objects: "public.customers", timestamp: base},
		{agent: "agent-a", datasource: "ds-1", session: "s-1", tool: "execute", decision: "deny", statement: "UPDATE", risk: 1, raw: "UPDATE orders SET state='paid'", normalized: "UPDATE orders SET state=$1", objects: "public.orders", timestamp: base.Add(time.Minute)},
		{agent: "agent-b", datasource: "ds-1", session: "s-2", tool: "execute", decision: "approve", statement: "DELETE", risk: 2, raw: "DELETE FROM sessions", normalized: "DELETE FROM sessions", objects: "public.sessions", timestamp: base.Add(2 * time.Minute)},
		{agent: "agent-b", datasource: "ds-2", session: "s-3", tool: "query", decision: "warn", statement: "SELECT", risk: 3, raw: "SELECT phone FROM contacts", normalized: "SELECT phone FROM contacts", objects: "crm.contacts", timestamp: base.Add(3 * time.Minute)},
		{agent: "agent-c", datasource: "ds-2", session: "s-4", tool: "execute", decision: "error", statement: "INSERT", risk: 2, raw: "INSERT INTO events VALUES (1)", normalized: "INSERT INTO events VALUES ($1)", objects: "public.events", timestamp: base.Add(4 * time.Minute)},
		{agent: "agent-a", datasource: "ds-2", session: "s-5", tool: "query", decision: "allow", statement: "SELECT", risk: 4, raw: "SELECT id FROM orders", normalized: "SELECT id FROM orders", objects: "sales.orders", timestamp: base.Add(5 * time.Minute)},
		{agent: "agent-c", datasource: "ds-1", session: "s-6", tool: "execute", decision: "deny", statement: "DDL", risk: 1, raw: "DROP TABLE archive", normalized: "DROP TABLE archive", objects: "public.archive", timestamp: base.Add(6 * time.Minute)},
		{agent: "agent-b", datasource: "ds-2", session: "s-7", tool: "query", decision: "approve", statement: "SELECT", risk: 2, raw: "SELECT secret FROM vault", normalized: "SELECT secret FROM vault", objects: "secure.vault", timestamp: base.Add(6 * time.Minute)},
	}
	for _, fixture := range fixtures {
		_, err := opened.metaDB.ExecContext(
			context.Background(),
			`INSERT INTO audit_logs (
  ts, agent_id, datasource_id, session_id, mcp_tool, sql_raw, sql_norm,
  stmt_type, objects, decision, risk_level
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			fixture.timestamp,
			fixture.agent,
			fixture.datasource,
			fixture.session,
			fixture.tool,
			fixture.raw,
			fixture.normalized,
			fixture.statement,
			fixture.objects,
			fixture.decision,
			fixture.risk,
		)
		require.NoError(t, err)
	}

	tests := []struct {
		name     string
		filter   model.AuditFilter
		total    int64
		firstRaw string
	}{
		{name: "empty filter", filter: model.AuditFilter{}, total: 8, firstRaw: "SELECT secret FROM vault"},
		{name: "agent exact", filter: model.AuditFilter{AgentID: pointer("agent-a")}, total: 3},
		{name: "decision in", filter: model.AuditFilter{Decisions: []string{"deny", "approve"}}, total: 4},
		{name: "statement in", filter: model.AuditFilter{StmtTypes: []string{"UPDATE", "DELETE"}}, total: 2},
		{name: "inclusive time range", filter: model.AuditFilter{TimeStart: pointer(base.Add(2 * time.Minute)), TimeEnd: pointer(base.Add(4 * time.Minute))}, total: 3},
		{name: "risk range", filter: model.AuditFilter{RiskMin: pointer(2), RiskMax: pointer(3)}, total: 4},
		{name: "keyword raw", filter: model.AuditFilter{Keyword: "phone FROM"}, total: 1, firstRaw: "SELECT phone FROM contacts"},
		{name: "keyword normalized", filter: model.AuditFilter{Keyword: "state=$1"}, total: 1},
		{name: "keyword wildcards escaped", filter: model.AuditFilter{Keyword: "%_"}, total: 1},
		{name: "object literal percent escaped", filter: model.AuditFilter{ObjectLike: "public.%"}, total: 0},
		{name: "object substring", filter: model.AuditFilter{ObjectLike: "public."}, total: 5},
		{name: "datasource session and tool", filter: model.AuditFilter{DatasourceID: pointer("ds-2"), SessionID: pointer("s-3"), MCPTool: pointer("query")}, total: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			page, err := repository.FilteredPage(context.Background(), test.filter, 1, 100)
			require.NoError(t, err)
			require.Equal(t, test.total, page.Total)
			require.Len(t, page.List, int(test.total))
			if test.firstRaw != "" {
				require.NotNil(t, page.List[0].SQLRaw)
				require.Equal(t, test.firstRaw, *page.List[0].SQLRaw)
			}
		})
	}

	t.Run("pagination total and stable descending order", func(t *testing.T) {
		first, err := repository.FilteredPage(context.Background(), model.AuditFilter{}, 1, 2)
		require.NoError(t, err)
		second, err := repository.FilteredPage(context.Background(), model.AuditFilter{}, 2, 2)
		require.NoError(t, err)
		require.Equal(t, int64(8), first.Total)
		require.Equal(t, 1, first.Page)
		require.Equal(t, 2, first.PageSize)
		require.Len(t, first.List, 2)
		require.Greater(t, first.List[0].ID, first.List[1].ID)
		require.True(t, first.List[0].TS.Equal(first.List[1].TS))
		require.True(t, first.List[1].TS.After(second.List[0].TS))
	})

	t.Run("keyword injection remains data", func(t *testing.T) {
		page, err := repository.FilteredPage(context.Background(), model.AuditFilter{Keyword: "' OR 1=1--"}, 1, 100)
		require.NoError(t, err)
		require.Zero(t, page.Total)
		all, err := repository.FilteredPage(context.Background(), model.AuditFilter{}, 1, 100)
		require.NoError(t, err)
		require.Equal(t, int64(8), all.Total)
	})

	t.Run("legacy page delegates to empty filter", func(t *testing.T) {
		legacy, err := repository.Page(context.Background(), 1, 3)
		require.NoError(t, err)
		filtered, err := repository.FilteredPage(context.Background(), model.AuditFilter{}, 1, 3)
		require.NoError(t, err)
		require.Equal(t, filtered, legacy)
	})
}

func TestAuditLogRepositoryFilteredPageValidation(t *testing.T) {
	repository := openTestStore(t).AuditLogs()
	_, err := repository.FilteredPage(context.Background(), model.AuditFilter{}, 0, 10)
	require.ErrorIs(t, err, ErrInvalidPage)
	_, err = repository.FilteredPage(context.Background(), model.AuditFilter{}, 1, 0)
	require.ErrorIs(t, err, ErrInvalidPageSize)
	_, err = repository.FilteredPage(context.Background(), model.AuditFilter{}, 1, 1001)
	require.ErrorIs(t, err, ErrInvalidPageSize)
	_, err = repository.FilteredPage(nil, model.AuditFilter{}, 1, 10)
	require.ErrorIs(t, err, ErrNilContext)
}

func TestBuildAuditWhereUsesOnlyPlaceholdersForValues(t *testing.T) {
	malicious := "%' OR 1=1--_!"
	clause, args := buildAuditWhere(model.AuditFilter{
		AgentID:    pointer(malicious),
		Decisions:  []string{"allow", malicious},
		Keyword:    malicious,
		ObjectLike: malicious,
	})
	require.NotContains(t, clause, malicious)
	require.Equal(t, " WHERE 1=1 AND agent_id = ? AND decision IN (?,?) AND (sql_raw LIKE ? ESCAPE '!' OR sql_norm LIKE ? ESCAPE '!') AND objects LIKE ? ESCAPE '!'", clause)
	require.Len(t, args, 6)
	require.Equal(t, malicious, args[0])
	require.Equal(t, "%!%' OR 1=1--!_!!%", args[3])
	require.Equal(t, args[3], args[4])
	require.Equal(t, args[3], args[5])
}

func TestAuditFilterIsEmpty(t *testing.T) {
	require.True(t, (model.AuditFilter{}).IsEmpty())
	require.False(t, (model.AuditFilter{Keyword: "x"}).IsEmpty())
	require.False(t, (model.AuditFilter{Decisions: []string{"allow"}}).IsEmpty())
	require.False(t, (model.AuditFilter{AgentID: pointer("")}).IsEmpty())
}
