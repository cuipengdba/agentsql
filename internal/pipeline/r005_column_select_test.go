package pipeline

import (
	"context"
	"testing"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestPostgresColumnAuthorizationAuditAddsR005WithoutStaticExplain(t *testing.T) {
	for _, test := range []struct {
		name     string
		result   model.QueryResult
		decision model.Decision
		wantR005 bool
	}{
		{
			name: "row limit truncation warns",
			result: model.QueryResult{
				Columns:   []string{"phone"},
				Rows:      [][]string{{"13812345678"}, {"13912345678"}},
				RowCount:  2,
				Truncated: true,
			},
			decision: model.DecisionWarn,
			wantR005: true,
		},
		{
			name: "small bounded result stays allowed",
			result: model.QueryResult{
				Columns:  []string{"phone"},
				Rows:     [][]string{{"13812345678"}},
				RowCount: 1,
			},
			decision: model.DecisionAllow,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPipelineFixture(t)
			run := newPipelineRun(
				fixture.pipeline,
				requestWithSQL("SELECT phone FROM public.customers LIMIT 100"),
				runModeProduction,
			)
			agent := fixture.authenticator.agent
			datasource := fixture.datasources.datasource
			run.agent = &agent
			run.datasource = &datasource
			run.policy = &model.PolicyDecision{
				AllowedTables: []string{"public.customers"},
				ColumnACL:     map[string][]string{},
				Level:         agent.Level,
			}
			run.ast = &model.AST{
				Dialect:  model.DBDialect("postgres"),
				RawSQL:   run.request.SQL,
				StmtType: model.StmtType("SELECT"),
				HasLimit: true,
				Tables:   []model.ObjectRef{{Schema: "public", Table: "customers"}},
			}
			require.Nil(t, run.ast.Explain, "production static assessment must not need Explain")

			err := run.columnDurableAudit(false)(
				context.Background(),
				executor.ColumnAuthorizationAudit{Decision: "allow", Reason: "ALLOW"},
				&test.result,
				mask.RedactReport{},
			)

			require.NoError(t, err)
			require.Equal(t, test.decision, run.response.Decision)
			if test.wantR005 {
				require.Contains(t, ruleHitIDs(run.response.Assessment.Hits), "R005")
			} else {
				require.NotContains(t, ruleHitIDs(run.response.Assessment.Hits), "R005")
			}
			require.Equal(t, string(test.decision), fixture.audit.last().Decision)
		})
	}
}
