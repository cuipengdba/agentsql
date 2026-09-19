package demoseed

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/stretchr/testify/require"
)

const demoSeedTestSecret = "0123456789abcdef0123456789abcdef"

type demoSeedNoopPinger struct{}

func (demoSeedNoopPinger) Ping(context.Context, model.Datasource, string) error { return nil }

func TestStaticDemoManifestUsesGlobalColumnRules(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("..", "..", "examples", "docker", "demo-seed.yaml"))
	require.NoError(t, err)
	manifest, err := ParseManifest(contents)
	require.NoError(t, err)
	require.Len(t, manifest.MaskRules, 4)
	for _, rule := range manifest.MaskRules {
		require.Empty(t, rule.SchemaName)
		require.Empty(t, rule.TableName)
	}
}

func TestEnsureMaskRuleUpgradesOldTableScopedFixedID(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "demo.db")
	opened, err := store.OpenWithSecret(ctx, path, []byte(demoSeedTestSecret))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, opened.Close()) })

	datasourceID := "ds-demo-pg"
	old := model.MaskRule{ID: "mask-demo-pg-phone", DatasourceID: &datasourceID, TableName: "customers",
		ColumnName: "phone", SensitiveType: "phone", Algo: "mask", Enabled: true}
	_, err = opened.MaskRules().Create(ctx, old)
	require.NoError(t, err)
	expected := old
	expected.TableName = ""

	require.NoError(t, ensureMaskRule(ctx, opened, expected, false))
	stored, err := opened.MaskRules().Get(ctx, expected.ID)
	require.NoError(t, err)
	require.Empty(t, stored.SchemaName)
	require.Empty(t, stored.TableName)
	require.True(t, sameMaskRule(expected, stored))
	rules, err := opened.MaskRules().ListByDatasource(ctx, datasourceID)
	require.NoError(t, err)
	require.Len(t, rules, 1)

	redactor, err := mask.NewRedactor([]mask.Rule{{Column: stored.ColumnName, SensitiveType: mask.TypePhone, Algorithm: mask.AlgoMask}})
	require.NoError(t, err)
	result, report := redactor.Apply(model.QueryResult{Columns: []string{"phone"}, Rows: [][]string{{"13812345678"}}})
	require.Equal(t, "138****5678", result.Rows[0][0])
	require.Equal(t, 1, report.MaskedCells)
}

func TestDemoSeedRestartUpsertsOldTableScopedRuleAndKeepsAllRules(t *testing.T) {
	ctx := context.Background()
	contents, err := os.ReadFile(filepath.Join("..", "..", "examples", "docker", "demo-seed.yaml"))
	require.NoError(t, err)
	manifest, err := ParseManifest(contents)
	require.NoError(t, err)
	passwords := make(map[string]string, len(manifest.Datasources))
	for _, datasource := range manifest.Datasources {
		passwords[datasource.ID] = "demo-password"
	}
	apiKeys := make(map[string]string, len(manifest.Agents))
	for _, agent := range manifest.Agents {
		apiKeys[agent.ID] = "asql_demo-test-key-" + agent.ID
	}
	secrets := Secrets{Passwords: passwords, APIKeys: apiKeys}
	anchor := time.Date(2026, time.September, 19, 12, 0, 0, 0, time.UTC)
	opened, err := store.OpenWithSecret(ctx, filepath.Join(t.TempDir(), "demo-restart.db"), []byte(demoSeedTestSecret))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, opened.Close()) })

	_, err = Run(ctx, opened, manifest, secrets, anchor, false, demoSeedNoopPinger{})
	require.NoError(t, err)
	legacy, err := opened.MaskRules().Get(ctx, "mask-demo-pg-phone")
	require.NoError(t, err)
	legacy.TableName = "customers"
	_, err = opened.MaskRules().Update(ctx, legacy)
	require.NoError(t, err)

	_, err = Run(ctx, opened, manifest, secrets, anchor, false, demoSeedNoopPinger{})
	require.NoError(t, err, "restarting the seed must upsert a fixed-ID legacy table-scoped rule")
	rules, err := opened.MaskRules().List(ctx)
	require.NoError(t, err)
	require.Len(t, rules, len(manifest.MaskRules))
	for _, rule := range rules {
		require.Empty(t, rule.SchemaName)
		require.Empty(t, rule.TableName)
		require.True(t, rule.Enabled)
	}
}

func TestSameMaskRuleTreatsStoredNullScopeAsEmpty(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "demo-null.db")
	opened, err := store.OpenWithSecret(ctx, path, []byte(demoSeedTestSecret))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, opened.Close()) })
	datasourceID := "ds-demo-mysql"
	expected := model.MaskRule{ID: "mask-demo-mysql-email", DatasourceID: &datasourceID,
		ColumnName: "email", SensitiveType: "email", Algo: "mask", Enabled: true}
	_, err = opened.MaskRules().Create(ctx, expected)
	require.NoError(t, err)

	database, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	_, err = database.ExecContext(ctx, `UPDATE mask_rules SET schema_name=NULL WHERE id=?`, expected.ID)
	require.NoError(t, err)

	stored, err := opened.MaskRules().Get(ctx, expected.ID)
	require.NoError(t, err)
	require.True(t, sameMaskRule(expected, stored), "repository COALESCE must make legacy NULL and empty scope equivalent")
}
