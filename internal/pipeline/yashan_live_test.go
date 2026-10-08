//go:build yashan && cgo

package pipeline

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/parser"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/stretchr/testify/require"
)

func TestYashanPipelineRealE2E(t *testing.T) {
	if os.Getenv("AGENTSQL_YASHAN_E2E") != "1" {
		t.Skip("set AGENTSQL_YASHAN_E2E=1 with a standalone YashanDB client")
	}
	password := os.Getenv("YASHAN_PASSWORD")
	if password == "" {
		t.Fatal("YASHAN_PASSWORD is required")
	}
	host := os.Getenv("YASHAN_HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	escape := strings.NewReplacer(`\`, `\\`, `/`, `\/`, `@`, `\@`)
	dsn := "SYS/" + escape.Replace(password) + "@" + host + ":1688?compat_vector=yashan"
	admin, err := sql.Open("yasdb", dsn)
	if err != nil {
		t.Fatal("open YashanDB admin connection failed")
	}
	t.Cleanup(func() { _ = admin.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := admin.PingContext(ctx); err != nil {
		t.Fatal("ping YashanDB admin connection failed")
	}
	suffixBytes := make([]byte, 4)
	if _, err := rand.Read(suffixBytes); err != nil {
		t.Fatal("generate temporary table name failed")
	}
	suffix := strings.ToUpper(hex.EncodeToString(suffixBytes))
	allowedTable := "AGSQL_B71_P_" + suffix
	deniedTable := "AGSQL_B71_D_" + suffix
	created := make([]string, 0, 2)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		for _, table := range created {
			if _, err := admin.ExecContext(cleanupCtx, "DROP TABLE "+table); err != nil {
				t.Errorf("remove temporary YashanDB table failed")
			}
		}
	})
	for _, table := range []string{allowedTable, deniedTable} {
		if _, err := admin.ExecContext(ctx, "CREATE TABLE "+table+" (ID NUMBER, PHONE VARCHAR2(32))"); err != nil {
			t.Fatal("create temporary YashanDB table failed")
		}
		created = append(created, table)
		if _, err := admin.ExecContext(ctx, "INSERT INTO "+table+" (ID, PHONE) VALUES (1, '13800135678')"); err != nil {
			t.Fatal("seed temporary YashanDB table failed")
		}
	}
	fixture := newPipelineFixture(t)
	cipher, err := store.NewPasswordCipher(testPipelineSecret)
	require.NoError(t, err)
	encrypted, err := cipher.Encrypt(password)
	require.NoError(t, err)
	fixture.datasources.datasource = model.Datasource{
		ID: "datasource-1", DBType: "yashan", Host: host, Port: 1688,
		Database: "SYS", Username: "SYS", PasswordEnc: encrypted,
		RowLimit: 2, StmtTimeoutMS: 15_000,
	}
	fixture.policies.policies = []model.Policy{{
		ID: "yashan-live-select", AgentID: "agent-1", DatasourceID: "datasource-1",
		ObjectType: "table", ObjectName: "SYS." + allowedTable, Action: "allow",
	}}
	redactor, err := mask.NewRedactor([]mask.Rule{{
		Schema: "SYS", Table: allowedTable, Column: "PHONE",
		SensitiveType: mask.TypePhone, Algorithm: mask.AlgoMask,
	}})
	require.NoError(t, err)
	fixture.redactors.redactor = redactor
	gateway := executor.NewGateway(true)
	t.Cleanup(func() {
		if err := gateway.CloseAll(); err != nil {
			t.Errorf("close YashanDB gateway failed")
		}
	})
	ports := fixture.ports()
	ports.Executors = gateway
	fixture.pipeline, err = New(ports, testPipelineSecret)
	require.NoError(t, err)

	allowSQL := "SELECT PHONE FROM SYS." + allowedTable + " WHERE ID = 1"
	parsed, err := parser.NewParser(model.DialectYashan)
	require.NoError(t, err)
	ast, err := parsed.Parse(allowSQL)
	require.NoError(t, err)
	require.Equal(t, model.ObjectRef{Schema: "SYS", Table: allowedTable},
		ast.ProjectionLineages[0].Arms[0].Dependencies[0].Origin.Relation)
	allowed, err := fixture.pipeline.Process(ctx, requestWithSQL(allowSQL))
	require.NoError(t, err)
	require.Equal(t, model.DecisionAllow, allowed.Decision)
	require.NotNil(t, allowed.Result)
	require.Equal(t, "138****5678", allowed.Result.Rows[0][0])
	require.Equal(t, 1, fixture.audit.calls())
	require.Equal(t, "allow", fixture.audit.last().Decision)

	denied, err := fixture.pipeline.Process(ctx, requestWithSQL(
		"SELECT PHONE FROM SYS."+deniedTable+" WHERE ID = 1"))
	require.NoError(t, err)
	require.Equal(t, model.DecisionDeny, denied.Decision)
	require.Nil(t, denied.Result)
	require.Equal(t, 2, fixture.audit.calls())
	require.Equal(t, "deny", fixture.audit.last().Decision)

	commented, err := fixture.pipeline.Process(ctx, requestWithSQL(
		"SELECT /* b71 */ PHONE FROM SYS."+allowedTable+" WHERE ID = 1"))
	require.NoError(t, err)
	require.Equal(t, model.DecisionError, commented.Decision)
	require.Equal(t, string(executor.DBErrorCodeSyntax), commented.ErrorCode)
	require.Equal(t, 3, fixture.audit.calls())
	require.Equal(t, "error", fixture.audit.last().Decision)
}
