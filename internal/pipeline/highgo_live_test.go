package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/audit"
	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/stretchr/testify/require"
)

func TestHighGoV9010PipelineRealE2E(t *testing.T) {
	host := os.Getenv("AGENTSQL_TEST_HIGHGO_HOST")
	if host == "" {
		t.Skip("set AGENTSQL_TEST_HIGHGO_HOST for seeded HighGo 9.0.10 PG-mode testing")
	}
	password := os.Getenv("HIGHGO_RO_PASSWORD")
	require.NotEmpty(t, password)
	port := 5866
	if raw := os.Getenv("AGENTSQL_TEST_HIGHGO_PORT"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		require.NoError(t, err)
		port = parsed
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	fixture := newPipelineFixture(t)
	cipher, err := store.NewPasswordCipher(testPipelineSecret)
	require.NoError(t, err)
	encrypted, err := cipher.Encrypt(password)
	require.NoError(t, err)
	fixture.authenticator.agent.Level = "readonly"
	fixture.datasources.datasource = model.Datasource{
		ID: "datasource-1", DBType: "highgo", Host: host, Port: port,
		Database: "highgo", Username: "agentsql_v170_ro", PasswordEnc: encrypted,
		RowLimit: 10, StmtTimeoutMS: 5_000,
	}
	fixture.policies.policies = []model.Policy{{
		ID: "highgo-verify-read", AgentID: "agent-1", DatasourceID: "datasource-1",
		ObjectType: "table", ObjectName: "public.agentsql_v170_verify", Action: "allow",
	}}
	redactor, err := mask.NewRedactor([]mask.Rule{{
		Schema: "public", Table: "agentsql_v170_verify", Column: "phone",
		SensitiveType: mask.TypePhone, Algorithm: mask.AlgoMask,
	}})
	require.NoError(t, err)
	fixture.redactors.redactor = redactor
	opened, err := store.OpenWithSecret(ctx, filepath.Join(t.TempDir(), "highgo-audit.db"), testPipelineSecret)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, opened.Close()) })
	gateway := executor.NewGateway(true)
	t.Cleanup(func() { require.NoError(t, gateway.CloseAll()) })
	ports := fixture.ports()
	ports.Executors = gateway
	ports.Approvals = opened.Approvals()
	ports.Audit = audit.NewRecorder(opened.AuditLogs())
	fixture.pipeline, err = New(ports, testPipelineSecret)
	require.NoError(t, err)

	allowed, err := fixture.pipeline.Process(ctx, requestWithSQL(
		"SELECT phone FROM public.agentsql_v170_verify WHERE id = 1 LIMIT 1"))
	require.NoError(t, err)
	require.Equal(t, model.DecisionAllow, allowed.Decision, "%+v", allowed)
	require.Equal(t, "138****5678", allowed.Result.Rows[0][0])
	require.Equal(t, 1, allowed.Redact.MaskedCells)
	page, err := opened.AuditLogs().Page(ctx, 1, 10)
	require.NoError(t, err)
	require.Equal(t, int64(1), page.Total)
	require.Equal(t, "allow", page.List[0].Decision)
	require.Equal(t, allowed.AuditID, page.List[0].ID)

	denied, err := fixture.pipeline.Process(ctx, requestWithSQL(
		"SELECT secret FROM public.agentsql_v170_ungranted WHERE id = 1 LIMIT 1"))
	require.NoError(t, err)
	require.Equal(t, model.DecisionDeny, denied.Decision, "%+v", denied)
	require.Nil(t, denied.Result)
	page, err = opened.AuditLogs().Page(ctx, 1, 10)
	require.NoError(t, err)
	require.Equal(t, int64(2), page.Total)
	require.Equal(t, "deny", page.List[0].Decision)

	for _, sql := range []string{
		"UPDATE public.agentsql_v170_verify SET phone = 'changed' WHERE id = 1",
		"SELECT phone FROM public.agentsql_v170_verify WHERE id = 1 LIMIT 1 FOR UPDATE",
		"SELECT phone FROM public.agentsql_v170_verify WHERE id = 1 LIMIT 1; DELETE FROM public.agentsql_v170_verify",
	} {
		response, err := fixture.pipeline.Process(ctx, requestWithSQL(sql))
		require.NoError(t, err)
		require.NotEqual(t, model.DecisionAllow, response.Decision, sql)
		require.Nil(t, response.Result)
	}
	page, err = opened.AuditLogs().Page(ctx, 1, 10)
	require.NoError(t, err)
	require.Equal(t, int64(5), page.Total)
}
