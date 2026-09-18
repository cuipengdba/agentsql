package pipeline

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/engine"
	"github.com/cuipengdba/agentsql/internal/executor"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/rules"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	mysqlcontainer "github.com/testcontainers/testcontainers-go/modules/mysql"
)

func TestPipelineMySQL8E2E(t *testing.T) {
	ctx := pipelineDockerTestContext(t)
	const (
		database = "agentsql"
		username = "agentsql"
		password = "agentsql-password"
	)
	container, err := mysqlcontainer.Run(
		ctx,
		"mysql:8",
		mysqlcontainer.WithDatabase(database),
		mysqlcontainer.WithUsername(username),
		mysqlcontainer.WithPassword(password),
	)
	if err != nil {
		if container != nil {
			testcontainers.CleanupContainer(t, container)
		}
		require.NoError(t, err, "start mysql:8 (Docker daemon probe already succeeded)")
	}
	testcontainers.CleanupContainer(t, container)
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "3306/tcp")
	require.NoError(t, err)
	datasource := model.Datasource{
		ID:            "pipeline-mysql-8",
		DBType:        "mysql",
		Host:          host,
		Port:          port.Int(),
		Database:      database,
		Username:      username,
		ConnLimit:     5,
		StmtTimeoutMS: 5_000,
		RowLimit:      50,
	}
	databaseExecutor, err := executor.NewMySQLExecutor(ctx, datasource, password, false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, databaseExecutor.Close()) })
	setupMySQLPipelineSchema(t, ctx, databaseExecutor)

	counted := &countingDatabaseExecutor{delegate: databaseExecutor}
	allowedTables := []string{
		database + ".allowed_rows",
		database + ".big_rows",
		database + ".customers",
		database + ".orders",
		database + ".sensitive_rows",
	}

	t.Run("normal select allow", func(t *testing.T) {
		flow, ports := newDatabaseE2EPipeline(t, datasource, counted, "dml", allowedTables)
		before := counted.snapshot()
		response, err := flow.Process(ctx, databaseE2ERequest(datasource.ID,
			"SELECT value FROM agentsql.allowed_rows WHERE id = 1 LIMIT 1"))
		require.NoError(t, err)
		require.Equal(t, model.DecisionAllow, response.Decision)
		require.Equal(t, "original", response.Result.Rows[0][0])
		delta := counted.snapshot().minus(before)
		require.Equal(t, 1, delta.explain)
		require.Equal(t, 1, delta.query)
		require.Zero(t, delta.execute)
		require.Equal(t, "allow", ports.audit.last().Decision)
	})

	t.Run("real explain dynamic gate creates approval", func(t *testing.T) {
		flow, ports := newDatabaseE2EPipeline(
			t,
			datasource,
			counted,
			"dml",
			allowedTables,
			WithRuleLayers(engine.RuleLayers{Agent: engine.RuleLayer{
				"R004": {Thresholds: map[string]float64{rules.ThresholdMaxScanRows: 10}},
			}}),
		)
		ports.approvals.audit = ports.audit
		before := counted.snapshot()
		response, err := flow.Process(ctx, databaseE2ERequest(datasource.ID,
			"SELECT id FROM agentsql.big_rows"))
		require.NoError(t, err)
		require.Equal(t, model.DecisionApprove, response.Decision)
		require.Greater(t, response.Assessment.EstScanRows, int64(10))
		require.NotEmpty(t, response.ApprovalID)
		delta := counted.snapshot().minus(before)
		require.Equal(t, 1, delta.explain)
		require.Zero(t, delta.query+delta.execute)
		require.Equal(t, "approve", ports.audit.last().Decision)
	})

	t.Run("readonly write denied", func(t *testing.T) {
		flow, ports := newDatabaseE2EPipeline(t, datasource, counted, "readonly", allowedTables)
		before := counted.snapshot()
		response, err := flow.Process(ctx, databaseE2ERequest(datasource.ID,
			"UPDATE agentsql.allowed_rows SET value = 'changed' WHERE id = 1"))
		require.NoError(t, err)
		require.Equal(t, model.DecisionDeny, response.Decision)
		delta := counted.snapshot().minus(before)
		require.Zero(t, delta.explain+delta.query+delta.execute+delta.openSession)
		require.Equal(t, "deny", ports.audit.last().Decision)
	})

	t.Run("sensitive result is redacted", func(t *testing.T) {
		flow, _ := newDatabaseE2EPipeline(t, datasource, counted, "dml", allowedTables)
		response, err := flow.Process(ctx, databaseE2ERequest(datasource.ID,
			"SELECT phone FROM agentsql.allowed_rows WHERE id = 1 LIMIT 1"))
		require.NoError(t, err)
		require.Equal(t, model.DecisionAllow, response.Decision)
		require.Equal(t, "138****5678", response.Result.Rows[0][0])
		require.Equal(t, 1, response.Redact.MaskedCells)
		require.Equal(t, map[int]mask.SensitiveType{0: mask.TypePhone}, response.Redact.TouchedColumns)
	})

	t.Run("four category real driver values are redacted without audit leakage", func(t *testing.T) {
		flow, ports := newDatabaseE2EPipelineWithRules(t, datasource, counted, "dml", allowedTables, sensitiveE2ERules(false))
		response, err := flow.Process(ctx, databaseE2ERequest(datasource.ID,
			"SELECT id_card, legacy_id_card, pan_plain, pan_formatted, client_ip, birth_date FROM agentsql.sensitive_rows WHERE id = 1 LIMIT 1"))
		require.NoError(t, err)
		require.Equal(t, []string{"110105********002X", "130503******001", "411111******1111", "411111******1111", "192.168.*.*", "2000-**-**"}, response.Result.Rows[0])
		require.Equal(t, 6, response.Redact.MaskedCells)
		require.Equal(t, map[int]mask.SensitiveType{0: mask.TypeIDCard, 1: mask.TypeIDCard, 2: mask.TypeBankCard, 3: mask.TypeBankCard, 4: mask.TypeIP, 5: mask.TypeBirthDate}, response.Redact.TouchedColumns)
		assertSensitiveE2ENotPresent(t, response, ports.audit.last(), []string{"11010519491231002X", "130503670401001", "4111111111111111", "4111 1111-1111 1111", "192.168.10.20", "2000-02-29T00:00:00Z"})

		fallback, err := flow.Process(ctx, databaseE2ERequest(datasource.ID,
			"SELECT id_card FROM agentsql.sensitive_rows WHERE id = 2 LIMIT 1"))
		require.NoError(t, err)
		require.Equal(t, mask.RedactedFallback, fallback.Result.Rows[0][0])
		require.NotContains(t, strings.Join(fallback.Result.Rows[0], ""), "MYSQL_INVALID_ID_SENTINEL_4d72")
	})

	runMaskScopeScenarios(t, ctx, "mysql", datasource, databaseExecutor, counted, allowedTables)
	runDirectSourceFallbackScenario(t, ctx, "mysql", datasource, counted, allowedTables)
	runExpressionLineageKnownLimitScenario(t, ctx, "mysql", datasource, counted, allowedTables)
}

func setupMySQLPipelineSchema(t *testing.T, ctx context.Context, databaseExecutor *executor.MySQLExecutor) {
	t.Helper()
	statements := []string{
		`CREATE TABLE allowed_rows (id integer PRIMARY KEY, value text NOT NULL, phone text NOT NULL)`,
		`INSERT INTO allowed_rows (id, value, phone) VALUES (1, 'original', '13812345678')`,
		`CREATE TABLE big_rows (id integer PRIMARY KEY, value integer NOT NULL)`,
		`CREATE TABLE customers (id integer PRIMARY KEY, phone text NOT NULL, name text NOT NULL)`,
		`INSERT INTO customers (id, phone, name) VALUES
 (1, '13812345678', 'Alice'), (2, '13987654321', 'Bob')`,
		`CREATE TABLE orders (id integer PRIMARY KEY, customer_id integer NOT NULL, note text NOT NULL)`,
		`INSERT INTO orders (id, customer_id, note) VALUES
 (10, 1, 'first note'), (20, 2, 'second note')`,
		`CREATE TABLE sensitive_rows (id integer PRIMARY KEY, id_card text NOT NULL, legacy_id_card text NOT NULL, pan_plain text NOT NULL, pan_formatted text NOT NULL, client_ip varchar(64) NOT NULL, birth_date date NOT NULL)`,
		`INSERT INTO sensitive_rows VALUES
 (1, '11010519491231002X', '130503670401001', '4111111111111111', '4111 1111-1111 1111', '192.168.10.20', '2000-02-29'),
 (2, 'MYSQL_INVALID_ID_SENTINEL_4d72', '130503670401001', '4111111111111111', '4111 1111-1111 1111', '192.168.10.20', '2000-02-29')`,
	}
	for _, statement := range statements {
		_, err := databaseExecutor.Execute(ctx, statement)
		require.NoError(t, err, statement)
	}
	var insert strings.Builder
	insert.WriteString("INSERT INTO big_rows (id, value) VALUES ")
	for value := 1; value <= 200; value++ {
		if value > 1 {
			insert.WriteByte(',')
		}
		insert.WriteByte('(')
		insert.WriteString(strconv.Itoa(value))
		insert.WriteByte(',')
		insert.WriteString(strconv.Itoa(value))
		insert.WriteByte(')')
	}
	_, err := databaseExecutor.Execute(ctx, insert.String())
	require.NoError(t, err)
	for _, table := range []string{"allowed_rows", "big_rows", "customers", "orders", "sensitive_rows"} {
		_, err := databaseExecutor.Execute(ctx, "ANALYZE TABLE "+table)
		require.NoError(t, err)
	}
}
