package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/pipeline"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/stretchr/testify/require"
)

func TestMySQLColumnPolicyEnrollmentRealServer(t *testing.T) {
	if os.Getenv("AGENTSQL_MYSQL_IDENTITY_E2E") != "1" {
		t.Skip("set AGENTSQL_MYSQL_IDENTITY_E2E=1 against the isolated MySQL fixture")
	}
	t.Setenv("AGENTSQL_TEST_MYSQL_IDENTITY_PASSWORD", "")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	secret := []byte("0123456789abcdef0123456789abcdef")
	controlPath := filepath.Join(t.TempDir(), "mysql-control.db")
	control, err := store.OpenWithSecret(ctx, controlPath, secret)
	require.NoError(t, err)
	defer control.Close()
	plaintext, agentHash, err := store.GenerateAPIKey()
	require.NoError(t, err)
	agent, err := control.Agents().Create(ctx, model.Agent{ID: "mysql-agent", Name: "MySQL Agent",
		Status: "active", Level: "readonly", APIKeyHash: agentHash})
	require.NoError(t, err)
	datasource, err := control.Datasources().Create(ctx, model.Datasource{ID: "mysql-datasource",
		Name: "MySQL test", DBType: "mysql", Host: "127.0.0.1", Port: 3306,
		Database: "app", Username: "reader", ConnLimit: 2, StmtTimeoutMS: 5000, RowLimit: 100}, "")
	require.NoError(t, err)
	_, err = control.Policies().Create(ctx, model.Policy{ID: "mysql-table-policy", AgentID: agent.ID,
		DatasourceID: datasource.ID, ObjectType: "table", ObjectName: "app.customers", Action: "allow"})
	require.NoError(t, err)
	created, err := control.Policies().Create(ctx, model.Policy{ID: "mysql-policy", AgentID: agent.ID,
		DatasourceID: datasource.ID, ObjectType: "column", ObjectName: "app.customers", Action: "allow"})
	require.NoError(t, err)
	_, err = control.MaskRules().Create(ctx, model.MaskRule{ID: "mysql-phone-mask", TableName: "customers",
		ColumnName: "phone", SensitiveType: string(mask.TypePhone), Algo: string(mask.AlgoMask), Enabled: true})
	require.NoError(t, err)
	gateway := executor.NewGateway(false)
	defer gateway.CloseAll()
	runtime := &Runtime{Store: control, business: gateway, secret: secret, mysqlColumnEnabled: true,
		mysqlAccounts: map[string]config.MySQLMetadataAccount{datasource.ID: {
			Username: "identity", PasswordEnv: "AGENTSQL_TEST_MYSQL_IDENTITY_PASSWORD"}}}
	bound, err := runtime.BindMySQLColumnPolicy(ctx, created.ID, created.Revision, "app", "customers",
		[]MySQLColumnGrant{{Column: "id", Usage: "output"}, {Column: "id", Usage: "reference"},
			{Column: "phone", Usage: "output"}})
	require.NoError(t, err)
	require.NotNil(t, bound.RelationBinding)
	require.Equal(t, "healthy", bound.RelationBinding.Status)
	require.Len(t, bound.ColumnPermissions, 3)
	require.Empty(t, bound.ColumnStaging)
	require.Greater(t, bound.Revision, created.Revision)
	_, err = runtime.BindMySQLColumnPolicy(ctx, created.ID, created.Revision, "app", "customers",
		[]MySQLColumnGrant{{Column: "id", Usage: "output"}})
	require.ErrorIs(t, err, store.ErrRevisionMismatch)
	activated, err := activateB2Runtime(ctx, control, gateway, secret, "mysql-b2-runtime", 5*time.Second,
		time.Second, mysqlB2RuntimeOptions{enabled: true, accounts: runtime.mysqlAccounts})
	require.NoError(t, err)
	defer activated.close()
	require.Equal(t, B2StateActive, activated.snapshot().State)
	require.True(t, activated.allow(datasource))
	require.True(t, activated.route(datasource))
	activated.close()
	require.NoError(t, gateway.CloseAll())
	require.NoError(t, control.Close())
	cfg := bootstrapTestConfig(controlPath)
	cfg.ColumnAuthorization = config.ColumnAuthorizationConfig{Enabled: true, MySQLSingleTableEnabled: true,
		MySQLMetadataAccounts: runtime.mysqlAccounts, InstanceID: "mysql-b2-reassembled",
		LeaseMS: 5000, HeartbeatIntervalMS: 1000}
	assembled, err := Assemble(ctx, cfg, secret)
	require.NoError(t, err)
	defer assembled.Close()
	response, err := assembled.Pipeline.Process(ctx, pipeline.Request{APIKey: plaintext,
		DatasourceID: datasource.ID, SQL: "SELECT id, phone FROM app.customers WHERE id = 1 ORDER BY customers.id LIMIT 2",
		MCPTool: "query"})
	require.NoError(t, err)
	require.Equal(t, model.DecisionAllow, response.Decision)
	require.NotNil(t, response.Result)
	require.Len(t, response.Result.Rows, 1)
	require.NotContains(t, response.Result.Rows[0][1], "13800005678")
	require.NotNil(t, response.ColumnAuth)
	require.Equal(t, "allow", response.ColumnAuth.Decision)
	require.Positive(t, response.AuditID)
	denied, err := assembled.Pipeline.Process(ctx, pipeline.Request{APIKey: plaintext,
		DatasourceID: datasource.ID,
		SQL:          "SELECT phone FROM app.customers WHERE phone IS NOT NULL ORDER BY customers.id LIMIT 2",
		MCPTool:      "query"})
	require.NoError(t, err)
	require.Equal(t, model.DecisionDeny, denied.Decision)
	require.Nil(t, denied.Result)
	require.NotNil(t, denied.ColumnAuth)
	require.Equal(t, "AUTH_COLUMN_GRANT_MISSING", denied.ColumnAuth.Reason)
	require.Positive(t, denied.AuditID)
}
