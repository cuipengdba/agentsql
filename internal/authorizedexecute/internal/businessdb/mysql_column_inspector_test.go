package businessdb

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestMySQLColumnAuthorizationBoundaryIsAlwaysUnsupported(t *testing.T) {
	report := MySQLInspectorReport{
		InformationSchemaReadable: true,
		ExplainJSONAvailable:      true,
		TableExists:               true,
		ColumnCount:               3,
		ViewCount:                 1,
		TriggerCount:              1,
		EventCount:                1,
		ForeignKeyCount:           1,
	}
	for _, shape := range []string{
		"schema-qualified base-table SELECT",
		"view hiding a base table",
		"multiple statements",
		"table with an implicit trigger target",
		"simple INSERT/UPDATE/DELETE",
	} {
		t.Run(shape, func(t *testing.T) {
			verdict := report.ColumnAuthorizationVerdict()
			require.False(t, verdict.Supported)
			require.Equal(t, BinderCodeModeRequired, verdict.Code)
			require.Equal(t, "当前版本 MySQL 列级授权未闭合，建议使用 PostgreSQL 或保持表级策略", verdict.Message)
		})
	}
}

func TestMySQLInspectorRejectsBroadOrAdministrativeGrants(t *testing.T) {
	require.False(t, mysqlInspectorHasExcessPrivilege([]string{
		"GRANT USAGE ON *.* TO `s6_inspector`@`%`",
		"GRANT SELECT, SHOW VIEW, TRIGGER, EVENT ON `agentsql`.* TO `s6_inspector`@`%`",
	}))
	for _, grant := range []string{
		"GRANT ALL PRIVILEGES ON `agentsql`.* TO `inspector`@`%`",
		"GRANT SELECT, BACKUP_ADMIN ON *.* TO `inspector`@`%`",
		"GRANT SELECT, CONNECTION_ADMIN ON *.* TO `inspector`@`%`",
		"GRANT SELECT, REPLICATION CLIENT ON *.* TO `inspector`@`%`",
		"GRANT SELECT, CREATE ON `agentsql`.* TO `inspector`@`%`",
		"GRANT SELECT, INSERT ON `agentsql`.* TO `inspector`@`%`",
		"GRANT SELECT, LOCK TABLES ON `agentsql`.* TO `inspector`@`%`",
		"GRANT SELECT ON `agentsql`.* TO `inspector`@`%` WITH GRANT OPTION",
	} {
		t.Run(grant, func(t *testing.T) {
			require.True(t, mysqlInspectorHasExcessPrivilege([]string{grant}))
		})
	}
}

func TestMySQLInspectorTargetValidation(t *testing.T) {
	require.NoError(t, validateMySQLInspectorTarget(MySQLInspectorTarget{
		Schema: "agentsql", BaseTable: "orders", View: "visible_orders",
	}))
	for _, target := range []MySQLInspectorTarget{
		{},
		{Schema: "agentsql", BaseTable: ""},
		{Schema: "agentsql; DROP DATABASE agentsql", BaseTable: "orders"},
		{Schema: "agentsql", BaseTable: "orders\x00hidden"},
		{Schema: strings.Repeat("a", 65), BaseTable: "orders"},
	} {
		require.Error(t, validateMySQLInspectorTarget(target), "%+v", target)
	}
}

func TestNewMySQLColumnInspectorRejectsNonMySQLWithoutConnecting(t *testing.T) {
	inspector, err := NewMySQLColumnInspector(context.Background(), model.Datasource{DBType: "postgres"}, "secret")
	require.Nil(t, inspector)
	require.EqualError(t, err, "open MySQL column inspector: datasource is not mysql")
}

func TestMySQLInspectorDoesNotImplementExecutionInterfaces(t *testing.T) {
	var value any = (*MySQLColumnInspector)(nil)
	_, executor := value.(Executor)
	_, runner := value.(mysqlRunner)
	require.False(t, executor)
	require.False(t, runner)
}

func TestMySQLColumnInspectorHasNoProductionHandlerReferences(t *testing.T) {
	repository, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	require.NoError(t, err)
	identifiers := []string{
		"NewMySQLColumnInspector",
		"MySQLColumnInspector",
		"MySQLInspectorReport",
		"MySQLColumnAuthorizationUnsupportedCode",
	}
	var references []string
	for _, directory := range []string{
		filepath.Join(repository, "cmd"),
		filepath.Join(repository, "internal", "mcpserver"),
		filepath.Join(repository, "internal", "adminapi"),
	} {
		err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			content, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			for _, identifier := range identifiers {
				if strings.Contains(string(content), identifier) {
					relative, _ := filepath.Rel(repository, path)
					references = append(references, relative+":"+identifier)
				}
			}
			return nil
		})
		require.NoError(t, err)
	}
	require.Empty(t, references, "MySQL B2 S6 must remain disconnected from production MCP/HTTP handlers")
}
