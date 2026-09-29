package businessdb

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	mysqlcontainer "github.com/testcontainers/testcontainers-go/modules/mysql"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestMySQLColumnInspectorS6Matrix(t *testing.T) {
	for _, image := range []string{"mysql:8.0", "mysql:8.4"} {
		image := image
		t.Run(strings.TrimPrefix(image, "mysql:"), func(t *testing.T) {
			runMySQLColumnInspectorS6Matrix(t, image)
		})
	}
}

func runMySQLColumnInspectorS6Matrix(t *testing.T, image string) {
	t.Helper()
	ctx := dockerTestContext(t)
	const (
		database          = "agentsql"
		rootPassword      = "root-password"
		inspectorUser     = "s6_inspector"
		inspectorPassword = "inspector-password"
		proxyUser         = "s6_proxy"
		proxyPassword     = "proxy-password"
	)
	container, err := mysqlcontainer.Run(
		ctx,
		image,
		mysqlcontainer.WithDatabase(database),
		mysqlcontainer.WithUsername("root"),
		mysqlcontainer.WithPassword(rootPassword),
		testcontainers.WithLabels(map[string]string{
			"agentsql.b2.s6.mysql-inspector": "true",
			"agentsql.mysql.image":           image,
		}),
		testcontainers.WithWaitStrategyAndDeadline(180*time.Second,
			wait.ForLog("port: 3306  MySQL Community Server").WithStartupTimeout(180*time.Second),
		),
	)
	if err != nil {
		if container != nil {
			testcontainers.CleanupContainer(t, container)
		}
		require.NoError(t, err, "start %s (Docker daemon probe already succeeded)", image)
	}
	testcontainers.CleanupContainer(t, container)
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "3306/tcp")
	require.NoError(t, err)

	root, err := NewMySQLExecutor(ctx, model.Datasource{
		ID: "b2-s6-root-" + image, DBType: "mysql", Host: host, Port: port.Int(),
		Database: database, Username: "root", ConnLimit: 2, StmtTimeoutMS: 5_000,
	}, rootPassword, false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	for _, statement := range []string{
		`CREATE TABLE agentsql.customer (id BIGINT PRIMARY KEY, public_name VARCHAR(64) NOT NULL, secret_note VARCHAR(64) NOT NULL) ENGINE=InnoDB`,
		`CREATE TABLE agentsql.audit_sink (id BIGINT AUTO_INCREMENT PRIMARY KEY, customer_id BIGINT NOT NULL) ENGINE=InnoDB`,
		`CREATE TABLE agentsql.child (id BIGINT PRIMARY KEY, customer_id BIGINT, CONSTRAINT fk_child_customer FOREIGN KEY (customer_id) REFERENCES agentsql.customer(id)) ENGINE=InnoDB`,
		`CREATE VIEW agentsql.customer_public AS SELECT id, public_name FROM agentsql.customer`,
		`CREATE TRIGGER agentsql.customer_ai AFTER INSERT ON agentsql.customer FOR EACH ROW INSERT INTO agentsql.audit_sink(customer_id) VALUES (NEW.id)`,
		`CREATE EVENT agentsql.s6_daily_event ON SCHEDULE EVERY 1 DAY STARTS CURRENT_TIMESTAMP + INTERVAL 1 DAY DO INSERT INTO agentsql.audit_sink(customer_id) VALUES (0)`,
		fmt.Sprintf("CREATE USER '%s'@'%%' IDENTIFIED BY '%s'", inspectorUser, inspectorPassword),
		fmt.Sprintf("GRANT SELECT ON agentsql.customer TO '%s'@'%%'", inspectorUser),
		fmt.Sprintf("GRANT SELECT ON agentsql.child TO '%s'@'%%'", inspectorUser),
		fmt.Sprintf("GRANT SELECT, SHOW VIEW ON agentsql.customer_public TO '%s'@'%%'", inspectorUser),
		fmt.Sprintf("GRANT TRIGGER ON agentsql.customer TO '%s'@'%%'", inspectorUser),
		fmt.Sprintf("GRANT EVENT ON agentsql.* TO '%s'@'%%'", inspectorUser),
		fmt.Sprintf("CREATE USER '%s'@'%%' IDENTIFIED BY '%s'", proxyUser, proxyPassword),
		fmt.Sprintf("GRANT SELECT ON agentsql.* TO '%s'@'%%'", proxyUser),
	} {
		_, err = root.Execute(ctx, statement)
		require.NoError(t, err, statement)
	}

	inspector, err := NewMySQLColumnInspector(ctx, model.Datasource{
		ID: "b2-s6-inspector-" + image, DBType: "mysql", Host: host, Port: port.Int(),
		Database: database, Username: inspectorUser, ConnLimit: 1, StmtTimeoutMS: 5_000,
	}, inspectorPassword)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, inspector.Close()) })

	started := time.Now()
	report, err := inspector.Probe(ctx, MySQLInspectorTarget{
		Schema: database, BaseTable: "customer", View: "customer_public",
	})
	require.NoError(t, err)
	t.Logf("image=%s server=%s gtid=%s log_bin=%t binlog_format=%s lower_case_table_names=%d facts=%s",
		image, report.ServerVersion, report.GTIDMode, report.LogBin, report.BinlogFormat,
		report.LowerCaseTableNames, report.String())
	require.Less(t, time.Since(started), 10*time.Second)
	require.Contains(t, report.ServerVersion, strings.TrimPrefix(image, "mysql:"))
	require.Equal(t, inspectorUser+"@%", report.CurrentUser)
	require.Equal(t, database, report.Database)
	require.True(t, report.InformationSchemaReadable)
	require.True(t, report.ExplainJSONAvailable)
	require.True(t, report.TableExists)
	require.Equal(t, 3, report.ColumnCount)
	require.GreaterOrEqual(t, report.ViewCount, 1)
	require.Greater(t, report.ViewDefinitionBytes, 0)
	require.True(t, report.ShowCreateViewAvailable)
	require.Equal(t, 1, report.TriggerCount)
	require.Greater(t, report.TriggerDefinitionBytes, 0)
	require.Equal(t, 1, report.EventCount)
	require.Greater(t, report.EventDefinitionBytes, 0)
	require.Equal(t, 1, report.ForeignKeyCount)
	require.NotEmpty(t, report.ServerUUID)
	require.NotEmpty(t, report.GTIDMode)
	require.NotEmpty(t, report.BinlogFormat)
	require.False(t, report.BackupLockAttempted)
	require.False(t, report.GTIDWatcherActive)
	require.False(t, report.BinlogWatcherActive)
	require.ElementsMatch(t, []string{
		"typed_analyzed_tree",
		"per_output_view_lineage",
		"serializable_catalog_lock",
		"stable_semantic_digest",
		"prepared_invalidation_algebra",
	}, report.MissingProofPrimitives)
	require.False(t, mysqlInspectorHasExcessPrivilege(report.Grants))
	requireGrantContains(t, report.Grants, "SELECT")
	requireGrantContains(t, report.Grants, "SHOW VIEW")
	requireGrantContains(t, report.Grants, "TRIGGER")
	requireGrantContains(t, report.Grants, "EVENT")
	for _, absent := range []string{"BACKUP_ADMIN", "CONNECTION_ADMIN", "PROCESS", "REPLICATION CLIENT", "SUPER"} {
		requireGrantAbsent(t, report.Grants, absent)
	}

	// A healthy catalog probe still cannot turn any SQL shape into a v0.4
	// column-authorization allow. In particular, the view hides secret_note,
	// and the trigger has an implicit audit_sink write that EXPLAIN does not
	// close. Multiple statements are rejected by the same stable boundary.
	for _, statementShape := range []string{
		"SELECT id, public_name FROM agentsql.customer",
		"SELECT id, public_name FROM agentsql.customer_public",
		"SELECT id FROM agentsql.customer; SELECT secret_note FROM agentsql.customer",
		"INSERT INTO agentsql.customer(id, public_name, secret_note) VALUES (1, 'visible', 'hidden')",
	} {
		t.Run("unsupported-"+shortMySQLShapeName(statementShape), func(t *testing.T) {
			verdict := report.ColumnAuthorizationVerdict()
			require.False(t, verdict.Supported)
			require.Equal(t, BinderCodeModeRequired, verdict.Code)
			require.Equal(t, MySQLColumnAuthorizationUnsupportedMessage, verdict.Message)
		})
	}

	// The ordinary proxy credential is demonstrably separate and lacks the
	// inspector's DDL-capable TRIGGER/EVENT metadata grants.
	proxy, err := NewMySQLExecutor(ctx, model.Datasource{
		ID: "b2-s6-proxy-" + image, DBType: "mysql", Host: host, Port: port.Int(),
		Database: database, Username: proxyUser, ConnLimit: 1, StmtTimeoutMS: 5_000,
	}, proxyPassword, false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, proxy.Close()) })
	result, err := proxy.Query(ctx, `SELECT id, public_name FROM agentsql.customer_public`, 1)
	require.NoError(t, err)
	require.Equal(t, []string{"id", "public_name"}, result.Columns)
	_, err = proxy.Execute(ctx, `CREATE TRIGGER agentsql.proxy_forbidden AFTER INSERT ON agentsql.customer FOR EACH ROW SET @x = 1`)
	require.Error(t, err)
}

func requireGrantContains(t *testing.T, grants []string, expected string) {
	t.Helper()
	joined := strings.ToUpper(strings.Join(grants, "\n"))
	require.Contains(t, joined, expected)
}

func requireGrantAbsent(t *testing.T, grants []string, forbidden string) {
	t.Helper()
	joined := strings.ToUpper(strings.Join(grants, "\n"))
	require.NotContains(t, joined, forbidden)
}

func shortMySQLShapeName(statement string) string {
	switch {
	case strings.Contains(statement, ";"):
		return "multi-statement"
	case strings.Contains(statement, "customer_public"):
		return "view"
	case strings.HasPrefix(statement, "INSERT"):
		return "trigger-implicit-table"
	default:
		return "base-table"
	}
}
