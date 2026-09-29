package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/engine"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/rules"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	mysqlcontainer "github.com/testcontainers/testcontainers-go/modules/mysql"
	"github.com/testcontainers/testcontainers-go/wait"
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
		testcontainers.WithWaitStrategyAndDeadline(300*time.Second,
			wait.ForLog("port: 3306  MySQL Community Server").WithStartupTimeout(300*time.Second),
		),
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
	databaseExecutor := newPipelineE2EDatabase(t, &datasource, password, false)
	setupMySQLPipelineSchema(t, ctx, databaseExecutor)

	counted := &countingDatabaseExecutor{delegate: databaseExecutor.gateway}
	allowedTables := []string{
		database + ".a4_missing_ddl",
		database + ".allowed_rows",
		database + ".big_rows",
		database + ".customers",
		database + ".orders",
		database + ".sensitive_rows",
		database + ".hash_rows",
		database + ".block_rows",
		database + ".insert_rows",
		database + ".unique_rows",
		database + ".strict_rows",
		database + ".lock_rows",
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

	t.Run("A4 failed DDL persists related intent and error outcome", func(t *testing.T) {
		flow, ports := newDatabaseE2EPipeline(t, datasource, counted, "ddl", allowedTables)
		response, err := flow.Process(ctx, databaseE2ERequest(datasource.ID,
			"DROP TABLE agentsql.a4_missing_ddl"))
		require.NoError(t, err)
		require.Equal(t, model.DecisionError, response.Decision, "%+v", response)
		require.Equal(t, string(executor.DBErrorCodeObjectNotFound), response.ErrorCode)
		require.Equal(t, string(executor.DBStageExecute), response.ErrorStage)
		require.Nil(t, response.Result)
		logs := ports.audit.snapshot()
		require.Len(t, logs, 2)
		require.Equal(t, "allow", logs[0].Decision)
		require.Equal(t, "error", logs[1].Decision)
		require.Equal(t, response.ErrorCode, requireStringPointer(t, logs[1].ErrorCode))
		require.Nil(t, logs[1].RowsReturned)
		assertAuditPhase(t, logs[0], "intent", 0)
		assertAuditPhase(t, logs[1], "outcome", logs[0].ID)
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

	t.Run("insert values write path and classified failures", func(t *testing.T) {
		t.Run("successful insert", func(t *testing.T) {
			flow, ports := newDatabaseE2EPipeline(t, datasource, counted, "dml", allowedTables)
			response, err := flow.Process(ctx, databaseE2ERequest(
				datasource.ID,
				"INSERT INTO agentsql.insert_rows (id, v) VALUES (1, 7)",
			))
			require.NoError(t, err)
			require.Equal(t, model.DecisionAllow, response.Decision)
			require.NotNil(t, response.Result)
			require.Equal(t, 1, response.Result.RowCount)
			require.Equal(t, "allow", ports.audit.last().Decision)

			stored, err := databaseExecutor.Query(
				ctx,
				"SELECT v FROM agentsql.insert_rows WHERE id = 1",
				1,
			)
			require.NoError(t, err)
			require.Equal(t, [][]string{{"7"}}, stored.Rows)
		})

		t.Run("duplicate key 1062", func(t *testing.T) {
			flow, ports := newDatabaseE2EPipeline(t, datasource, counted, "dml", allowedTables)
			response, err := flow.Process(ctx, databaseE2ERequest(
				datasource.ID,
				"INSERT INTO agentsql.unique_rows (id, name) VALUES (1, 'dup')",
			))
			assertMySQLPipelineDBError(
				t, response, err, ports.audit.last(),
				executor.DBErrorCodeConstraint, executor.DBStageExecute,
				[]string{"1062", "Duplicate entry"},
			)
		})

		t.Run("strict mode 1366", func(t *testing.T) {
			flow, ports := newDatabaseE2EPipeline(t, datasource, counted, "dml", allowedTables)
			response, err := flow.Process(ctx, databaseE2ERequest(
				datasource.ID,
				"INSERT INTO agentsql.strict_rows (id, v) VALUES (9, '')",
			))
			assertMySQLPipelineDBError(
				t, response, err, ports.audit.last(),
				executor.DBErrorCodeData, executor.DBStageExecute,
				[]string{"1366", "Incorrect integer value"},
			)
		})
	})

	t.Run("select for update nowait 3572 is retryable", func(t *testing.T) {
		holder, err := databaseExecutor.statement(ctx, "SELECT v FROM agentsql.lock_rows WHERE id = 1 FOR UPDATE", "pipeline-mysql-nowait-holder")
		require.NoError(t, err)
		locked := make(chan struct{})
		release := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			_, executeErr := holder.ExecuteTransactional(ctx, func(model.QueryResult) error {
				close(locked)
				<-release
				return errors.New("release lock holder")
			})
			done <- executeErr
		}()
		select {
		case <-locked:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out acquiring MySQL row lock")
		}
		defer func() {
			close(release)
			require.Error(t, <-done)
			require.NoError(t, holder.Close())
		}()

		flow, ports := newDatabaseE2EPipeline(t, datasource, counted, "dml", allowedTables)
		response, err := flow.Process(ctx, databaseE2ERequest(
			datasource.ID,
			"SELECT v FROM agentsql.lock_rows WHERE id = 1 FOR UPDATE NOWAIT",
		))
		assertMySQLPipelineDBError(
			t, response, err, ports.audit.last(),
			executor.DBErrorCodeRetryable, executor.DBStageQuery,
			[]string{"3572", "NOWAIT", "Do not wait for lock"},
		)
		require.Equal(t, executor.Suggestion(executor.DBErrorCodeRetryable), response.Suggestion)
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
		flow, ports := newDatabaseE2EPipelineWithRules(t, datasource, counted, "dml", allowedTables, sensitiveE2ERules(false), nil)
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
	runHashE2EScenarios(t, ctx, "agentsql.hash_rows", datasource, counted, allowedTables)
	runBlockE2EScenarios(t, ctx, "agentsql.block_rows", datasource, counted, allowedTables)

	runMaskScopeScenarios(t, ctx, "mysql", datasource, databaseExecutor, counted, allowedTables)
	runDirectSourceFallbackScenario(t, ctx, "mysql", datasource, counted, allowedTables)
	runExpressionLineageNowMaskedScenario(t, ctx, "mysql", datasource, counted, allowedTables)
}

func setupMySQLPipelineSchema(t *testing.T, ctx context.Context, databaseExecutor *pipelineE2EDatabase) {
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
		`CREATE TABLE hash_rows (id integer PRIMARY KEY, secret text NULL)`,
		`INSERT INTO hash_rows VALUES
 (1, 'Ordinary Alice'), (2, 'Ordinary Alice'), (3, ' Ordinary Alice '), (4, ''), (5, NULL), (6, 'T41_E2E_RAW_SENTINEL_b7a3')`,
		`CREATE TABLE block_rows (id integer PRIMARY KEY, phone text NULL, hash_secret text NULL, blocked_secret text NULL)`,
		`INSERT INTO block_rows VALUES
 (1, '13812345678', 'T42_E2E_HASH_RAW_2f64', 'T42_E2E_BLOCK_RAW_6d19'),
 (2, '', '', ''),
 (3, NULL, NULL, NULL)`,
		`CREATE TABLE insert_rows (id integer PRIMARY KEY, v integer NOT NULL)`,
		`CREATE TABLE unique_rows (id integer PRIMARY KEY, name varchar(32) UNIQUE)`,
		`INSERT INTO unique_rows (id, name) VALUES (1, 'dup')`,
		`CREATE TABLE strict_rows (id integer PRIMARY KEY, v integer NOT NULL)`,
		`CREATE TABLE lock_rows (id integer PRIMARY KEY, v integer NOT NULL)`,
		`INSERT INTO lock_rows (id, v) VALUES (1, 1)`,
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
	for _, table := range []string{
		"allowed_rows", "big_rows", "customers", "orders", "sensitive_rows", "hash_rows", "block_rows",
		"insert_rows", "unique_rows", "strict_rows", "lock_rows",
	} {
		_, err := databaseExecutor.Execute(ctx, "ANALYZE TABLE "+table)
		require.NoError(t, err)
	}
}

func assertMySQLPipelineDBError(
	t *testing.T,
	response Response,
	processErr error,
	auditLog model.AuditLog,
	code executor.DBErrorCode,
	stage executor.DBStage,
	leaks []string,
) {
	t.Helper()
	require.NoError(t, processErr)
	require.Equal(t, model.DecisionError, response.Decision)
	require.Equal(t, string(code), response.ErrorCode)
	require.Equal(t, string(stage), response.ErrorStage)
	require.Nil(t, response.Result)
	require.Equal(t, "error", auditLog.Decision)
	require.NotNil(t, auditLog.ErrorCode)
	require.Equal(t, string(code), *auditLog.ErrorCode)
	require.NotNil(t, auditLog.ErrorMsg)
	require.Equal(t, response.ErrorMessage, *auditLog.ErrorMsg)
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	for _, leak := range leaks {
		require.NotContains(t, string(encoded), leak)
		require.NotContains(t, *auditLog.ErrorMsg, leak)
	}
}
