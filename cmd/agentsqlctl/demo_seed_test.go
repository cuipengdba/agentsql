package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/auth"
	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/b5"
	"github.com/cuipengdba/agentsql/internal/b5dml"
	"github.com/cuipengdba/agentsql/internal/demoseed"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/rules"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/stretchr/testify/require"
)

const testDemoBanner = "Live Demo · data resets daily · never connect real data or databases"

type fakeDemoPinger struct {
	err   error
	calls int
}

func (pinger *fakeDemoPinger) Ping(context.Context, model.Datasource, string) error {
	pinger.calls++
	return pinger.err
}

func (pinger *fakeDemoPinger) EnrollPostgresPolicySelect(_ context.Context, _ model.Datasource, _ []byte, sqlText string, _ executor.Limits) (executor.PostgresPolicyEnrollment, error) {
	return executor.PostgresPolicyEnrollment{Mode: "NATIVE_C_V1", StatementClass: "SELECT", Relations: []executor.PostgresPolicyRelation{
		{DatabaseOID: 1, RelationOID: 100, Schema: "public", Name: "demo_b2_customers", Kind: 'r', CatalogFingerprint: "catalog-demo"},
		{DatabaseOID: 1, RelationOID: 101, Schema: "public", Name: "demo_b2_orders", Kind: 'r', CatalogFingerprint: "catalog-demo"},
	}, ColumnUses: []executor.PostgresPolicyColumnUse{
		{RelationOID: 100, Attnum: 1, Name: "id", TypeOID: 20, TypeModifier: -1, Usage: "reference"},
		{RelationOID: 100, Attnum: 2, Name: "full_name", TypeOID: 25, TypeModifier: -1, Usage: "output"},
		{RelationOID: 100, Attnum: 4, Name: "region", TypeOID: 25, TypeModifier: -1, Usage: "output"},
		{RelationOID: 101, Attnum: 1, Name: "id", TypeOID: 20, TypeModifier: -1, Usage: "reference"},
		{RelationOID: 101, Attnum: 2, Name: "customer_id", TypeOID: 20, TypeModifier: -1, Usage: "reference"},
		{RelationOID: 101, Attnum: 3, Name: "status", TypeOID: 25, TypeModifier: -1, Usage: "output"},
	}}, nil
}

func (pinger *fakeDemoPinger) EnrollPostgresPolicyDML(_ context.Context, _ model.Datasource, _ []byte, sqlText string, _ executor.Limits) (executor.PostgresPolicyEnrollment, error) {
	relation := executor.PostgresPolicyRelation{DatabaseOID: 1, RelationOID: 200, Schema: "public", Name: "demo_tx_accounts", Kind: 'r', CatalogFingerprint: "catalog-demo-tx"}
	result := executor.PostgresPolicyEnrollment{Mode: "NATIVE_C_V1", StatementClass: "UPDATE", Action: b5.ActionUpdate, Relations: []executor.PostgresPolicyRelation{relation},
		ColumnUses: []executor.PostgresPolicyColumnUse{{RelationOID: 200, Attnum: 1, Name: "id", TypeOID: 20, TypeModifier: -1, Usage: "reference"}}}
	if strings.Contains(sqlText, "balance=balance") {
		result.WriteTargets = []executor.PostgresPolicyWriteTarget{{RelationOID: 200, Attnum: 2, Name: "balance", TypeOID: 20, TypeModifier: -1, Kind: b5dml.WriteTargetColumn}}
		result.ColumnUses = append(result.ColumnUses, executor.PostgresPolicyColumnUse{RelationOID: 200, Attnum: 2, Name: "balance", TypeOID: 20, TypeModifier: -1, Usage: "reference"})
	} else {
		result.WriteTargets = []executor.PostgresPolicyWriteTarget{{RelationOID: 200, Attnum: 3, Name: "status", TypeOID: 25, TypeModifier: -1, Kind: b5dml.WriteTargetColumn}}
	}
	return result, nil
}

func TestDemoSeedRejectsDisabledBadAnchorAndMissingEnvironment(t *testing.T) {
	t.Setenv("AGENTSQL_DEMO", "")
	temporary := t.TempDir()
	manifestPath := copyDemoManifest(t, temporary)
	disabledConfig := writeDemoConfig(t, temporary, filepath.Join(temporary, "disabled.db"), false)
	dependencies := demoSeedDependencies{lookupEnv: mapLookup(map[string]string{}), pinger: &fakeDemoPinger{}}

	_, _, err := executeDemoSeed(context.Background(), disabledConfig, manifestPath, "2026-09-17", false, dependencies)
	require.ErrorContains(t, err, "demo.enabled must be true")
	for _, anchor := range []string{"", "2026-02-30", "17-09-2026"} {
		_, _, err = executeDemoSeed(context.Background(), disabledConfig, manifestPath, anchor, false, dependencies)
		require.ErrorContains(t, err, "real date")
	}

	enabledConfig := writeDemoConfig(t, temporary, filepath.Join(temporary, "missing-env.db"), true)
	_, _, err = executeDemoSeed(context.Background(), enabledConfig, manifestPath, "2026-09-17", false, dependencies)
	require.ErrorContains(t, err, "AGENTSQL_DEMO_PG_PASSWORD")
	require.NoFileExists(t, filepath.Join(temporary, "missing-env.db"))

	roKey, _, err := store.GenerateAPIKey()
	require.NoError(t, err)
	dmlKey, _, err := store.GenerateAPIKey()
	require.NoError(t, err)
	validEnvironment := map[string]string{
		"AGENTSQL_SECRET": "0123456789abcdef0123456789abcdef", "AGENTSQL_DEMO_PG_PASSWORD": "pg",
		"AGENTSQL_DEMO_MYSQL_PASSWORD": "mysql", "AGENTSQL_DEMO_RO_KEY": roKey, "AGENTSQL_DEMO_DML_KEY": dmlKey,
	}
	_, _, err = executeDemoSeed(context.Background(), enabledConfig, manifestPath, "2026-09-17", true,
		demoSeedDependencies{lookupEnv: mapLookup(validEnvironment), pinger: &fakeDemoPinger{}})
	require.ErrorContains(t, err, "existing SQLite database is required")
	require.NoFileExists(t, filepath.Join(temporary, "missing-env.db"), "verify-only must not create an empty SQLite file")
}

func TestDemoSeedSQLiteIdempotencyVerifyAuthenticationAndTamper(t *testing.T) {
	t.Setenv("AGENTSQL_DEMO", "")
	temporary := t.TempDir()
	databasePath := filepath.Join(temporary, "control.db")
	configPath := writeDemoConfig(t, temporary, databasePath, true)
	manifestPath := copyDemoManifest(t, temporary)
	roKey, _, err := store.GenerateAPIKey()
	require.NoError(t, err)
	dmlKey, _, err := store.GenerateAPIKey()
	require.NoError(t, err)
	values := map[string]string{
		"AGENTSQL_SECRET":              "0123456789abcdef0123456789abcdef",
		"AGENTSQL_DEMO_PG_PASSWORD":    "pg-canary-password",
		"AGENTSQL_DEMO_MYSQL_PASSWORD": "mysql-canary-password",
		"AGENTSQL_DEMO_RO_KEY":         roKey,
		"AGENTSQL_DEMO_DML_KEY":        dmlKey,
	}
	pinger := &fakeDemoPinger{}
	dependencies := demoSeedDependencies{lookupEnv: mapLookup(values), pinger: pinger}

	first, manifest, err := executeDemoSeed(context.Background(), configPath, manifestPath, "2026-09-17", false, dependencies)
	require.NoError(t, err)
	require.Equal(t, demoseed.Summary{Datasources: 2, Agents: 2, MaskRules: 4, Policies: 15, Rules: len(rules.BuiltinRuleOverrides()), Audits: 300, Approvals: 24, B5Grants: 5, B2Mode: "NATIVE_C_V1"}, first)
	firstHashes := readAgentHashes(t, databasePath)
	second, _, err := executeDemoSeed(context.Background(), configPath, manifestPath, "2026-09-17", false, dependencies)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Equal(t, firstHashes, readAgentHashes(t, databasePath), "rerun must not rotate keys")
	verified, _, err := executeDemoSeed(context.Background(), configPath, manifestPath, "2026-09-17", true, dependencies)
	require.NoError(t, err)
	require.Equal(t, first, verified)
	require.Equal(t, 6, pinger.calls)

	database := openDemoSQLite(t, databasePath)
	defer database.Close()
	requireTableCount(t, database, "datasources", 2)
	requireTableCount(t, database, "agents", 2)
	requireTableCount(t, database, "mask_rules", 4)
	requireTableCount(t, database, "policies", 15)
	requireTableCount(t, database, "b5_dml_grants", 5)
	requireTableCount(t, database, "rules", int64(len(rules.BuiltinRuleOverrides())))
	requireTableCount(t, database, "audit_logs", 300)
	requireTableCount(t, database, "approvals", 24)
	requireGroupedCounts(t, database, "SELECT decision, COUNT(*) FROM audit_logs GROUP BY decision", map[string]int64{"allow": 180, "deny": 60, "warn": 36, "approve": 24})
	requireGroupedCounts(t, database, "SELECT db_type, COUNT(*) FROM audit_logs GROUP BY db_type", map[string]int64{"postgres": 150, "mysql": 150})
	requireGroupedCounts(t, database, "SELECT agent_id, COUNT(*) FROM audit_logs GROUP BY agent_id", map[string]int64{"agent-demo-ro": 240, "agent-demo-dml": 60})
	requireGroupedCounts(t, database, "SELECT status, COUNT(*) FROM approvals GROUP BY status", map[string]int64{"pending": 6, "approved": 6, "rejected": 6, "expired": 6})
	rows, err := database.Query("SELECT ts FROM audit_logs")
	require.NoError(t, err)
	days := make(map[string]int)
	for rows.Next() {
		var timestamp time.Time
		require.NoError(t, rows.Scan(&timestamp))
		days[timestamp.UTC().Format("2006-01-02")]++
	}
	require.NoError(t, rows.Close())
	require.Len(t, days, 30)
	for _, count := range days {
		require.Equal(t, 10, count)
	}
	var linked, orphans int64
	require.NoError(t, database.QueryRow("SELECT COUNT(*) FROM approvals WHERE audit_id IS NOT NULL").Scan(&linked))
	require.NoError(t, database.QueryRow("SELECT COUNT(*) FROM approvals p LEFT JOIN audit_logs a ON a.id=p.audit_id WHERE p.audit_id IS NULL OR a.id IS NULL").Scan(&orphans))
	require.Equal(t, int64(24), linked)
	require.Zero(t, orphans)

	require.NoError(t, database.Close())
	opened, err := store.OpenWithSecret(context.Background(), databasePath, []byte(values["AGENTSQL_SECRET"]))
	require.NoError(t, err)
	authenticator := auth.NewAuthenticator(opened.Agents())
	roAgent, err := authenticator.Authenticate(context.Background(), roKey)
	require.NoError(t, err)
	require.Equal(t, "agent-demo-ro", roAgent.ID)
	dmlAgent, err := authenticator.Authenticate(context.Background(), dmlKey)
	require.NoError(t, err)
	require.Equal(t, "agent-demo-dml", dmlAgent.ID)
	require.NoError(t, opened.Close())

	wrongSecret := cloneStringMap(values)
	wrongSecret["AGENTSQL_SECRET"] = "abcdef0123456789abcdef0123456789"
	_, _, err = executeDemoSeed(context.Background(), configPath, manifestPath, "2026-09-17", true,
		demoSeedDependencies{lookupEnv: mapLookup(wrongSecret), pinger: &fakeDemoPinger{}})
	require.Error(t, err)

	database = openDemoSQLite(t, databasePath)
	_, err = database.Exec("UPDATE agents SET name='tampered' WHERE id='agent-demo-ro'")
	require.NoError(t, err)
	require.NoError(t, database.Close())
	_, _, err = executeDemoSeed(context.Background(), configPath, manifestPath, "2026-09-17", true, dependencies)
	require.ErrorContains(t, err, "agent \"agent-demo-ro\" differs")
	_, _, err = executeDemoSeed(context.Background(), configPath, manifestPath, "2026-09-17", false, dependencies)
	require.ErrorContains(t, err, "agent \"agent-demo-ro\" differs")

	database = openDemoSQLite(t, databasePath)
	wantName := ""
	for _, agent := range manifest.Agents {
		if agent.ID == "agent-demo-ro" {
			wantName = agent.Name
		}
	}
	_, err = database.Exec("UPDATE agents SET name=? WHERE id='agent-demo-ro'", wantName)
	require.NoError(t, err)
	_, err = database.Exec("UPDATE audit_logs SET model_name='tampered' WHERE session_id=?", store.DemoSeedSessionPrefix+"000001")
	require.NoError(t, err)
	require.NoError(t, database.Close())
	_, _, err = executeDemoSeed(context.Background(), configPath, manifestPath, "2026-09-17", true, dependencies)
	require.ErrorContains(t, err, "audit \"demo-seed:v1:000001\" differs")
	_, _, err = executeDemoSeed(context.Background(), configPath, manifestPath, "2026-09-17", false, dependencies)
	require.Error(t, err)
}

func TestDemoSeedConnectivityFailureAndOutputAreSecretSafe(t *testing.T) {
	t.Setenv("AGENTSQL_DEMO", "")
	temporary := t.TempDir()
	databasePath := filepath.Join(temporary, "control.db")
	configPath := writeDemoConfig(t, temporary, databasePath, true)
	manifestPath := copyDemoManifest(t, temporary)
	roKey, _, err := store.GenerateAPIKey()
	require.NoError(t, err)
	dmlKey, _, err := store.GenerateAPIKey()
	require.NoError(t, err)
	canaries := []string{"pg-super-secret", "mysql-super-secret", "0123456789abcdef0123456789abcdef", roKey, dmlKey, "postgres://secret-dsn"}
	values := map[string]string{
		"AGENTSQL_SECRET": canaries[2], "AGENTSQL_DEMO_PG_PASSWORD": canaries[0],
		"AGENTSQL_DEMO_MYSQL_PASSWORD": canaries[1], "AGENTSQL_DEMO_RO_KEY": roKey,
		"AGENTSQL_DEMO_DML_KEY": dmlKey,
	}
	pinger := &fakeDemoPinger{err: errors.New(strings.Join(canaries, " "))}
	command := newDemoSeedCommand(demoSeedDependencies{lookupEnv: mapLookup(values), pinger: pinger})
	command.SilenceErrors = true
	command.SilenceUsage = true
	command.SetArgs([]string{"--config", configPath, "--manifest", manifestPath, "--anchor-date", "2026-09-17"})
	var stdout, stderr strings.Builder
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	err = command.Execute()
	require.ErrorContains(t, err, "connectivity probe failed")
	combined := stdout.String() + stderr.String() + err.Error()
	for _, canary := range canaries {
		require.NotContains(t, combined, canary)
	}

	pinger.err = nil
	command = newDemoSeedCommand(demoSeedDependencies{lookupEnv: mapLookup(values), pinger: pinger})
	command.SilenceErrors = true
	command.SilenceUsage = true
	command.SetArgs([]string{"--config", configPath, "--manifest", manifestPath, "--anchor-date", "2026-09-17"})
	stdout.Reset()
	command.SetOut(&stdout)
	command.SetErr(io.Discard)
	require.NoError(t, command.Execute())
	require.Contains(t, stdout.String(), "DEMO_SEED_OK")
	for _, canary := range canaries {
		require.NotContains(t, stdout.String(), canary)
	}
}

func writeDemoConfig(t *testing.T, directory, databasePath string, enabled bool) string {
	t.Helper()
	demo := ""
	if enabled {
		demo = fmt.Sprintf("demo:\n  enabled: true\n  banner: %q\n  qps_per_agent: 2\n", testDemoBanner)
	}
	contents := fmt.Sprintf(`server:
  http_listen: "127.0.0.1:7780"
store:
  sqlite_path: %q
  auto_migrate: false
defaults:
  statement_timeout_ms: 5000
  row_limit: 1000
  max_conns_per_datasource: 5
  qps_per_agent: 20
theme:
  default: dark
%s`, filepath.ToSlash(databasePath), demo)
	path := filepath.Join(directory, strings.ReplaceAll(filepath.Base(databasePath), ".db", ".yaml"))
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	return path
}

func copyDemoManifest(t *testing.T, directory string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("..", "..", "examples", "docker", "demo-seed.yaml"))
	require.NoError(t, err)
	path := filepath.Join(directory, "demo-seed.yaml")
	require.NoError(t, os.WriteFile(path, contents, 0o600))
	return path
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func cloneStringMap(source map[string]string) map[string]string {
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func openDemoSQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	require.NoError(t, database.Ping())
	return database
}

func requireTableCount(t *testing.T, database *sql.DB, table string, want int64) {
	t.Helper()
	var count int64
	require.NoError(t, database.QueryRow("SELECT COUNT(*) FROM "+table).Scan(&count))
	require.Equal(t, want, count)
}

func requireGroupedCounts(t *testing.T, database *sql.DB, query string, want map[string]int64) {
	t.Helper()
	rows, err := database.Query(query)
	require.NoError(t, err)
	got := make(map[string]int64)
	for rows.Next() {
		var key string
		var count int64
		require.NoError(t, rows.Scan(&key, &count))
		got[key] = count
	}
	require.NoError(t, rows.Close())
	require.Equal(t, want, got)
}

func readAgentHashes(t *testing.T, path string) map[string]string {
	t.Helper()
	database := openDemoSQLite(t, path)
	defer database.Close()
	rows, err := database.Query("SELECT id, api_key_hash FROM agents ORDER BY id")
	require.NoError(t, err)
	result := make(map[string]string)
	for rows.Next() {
		var id, hash string
		require.NoError(t, rows.Scan(&id, &hash))
		result[id] = hash
	}
	require.NoError(t, rows.Close())
	return result
}
