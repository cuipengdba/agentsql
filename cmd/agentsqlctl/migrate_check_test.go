package main

import (
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/stretchr/testify/require"
)

func sqliteMigrationCheckConfig(t *testing.T, databasePath string) string {
	t.Helper()
	t.Setenv(config.MetadataDSNEnv, "")
	t.Setenv(config.AuditDSNEnv, "")
	contents := strings.Replace(defaultConfigTemplate, "./data/agentsql.db", filepath.ToSlash(databasePath), 1)
	contents = strings.Replace(contents, "auto_migrate: true", "auto_migrate: false", 1)
	return writeControlConfig(t, contents)
}

func TestMigrateCheckDoesNotCreateMissingSQLiteDatabase(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "missing database.db")
	configPath := sqliteMigrationCheckConfig(t, databasePath)
	var stderr strings.Builder
	require.Equal(t, 1, run([]string{"migrate", "--check", "--config", configPath}, io.Discard, &stderr))
	require.Contains(t, stderr.String(), "inspect metadata migration target")
	require.NoFileExists(t, databasePath)
}

func TestMigrateCheckRejectsExistingEmptySQLiteDatabaseWithoutDDL(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "empty.db")
	configPath := sqliteMigrationCheckConfig(t, databasePath)
	database, err := sql.Open("sqlite", databasePath)
	require.NoError(t, err)
	require.NoError(t, database.Ping())
	require.NoError(t, database.Close())
	var stderr strings.Builder
	require.Equal(t, 1, run([]string{"migrate", "--check", "--config", configPath}, io.Discard, &stderr))
	require.Contains(t, stderr.String(), "read metadata migration version")
	database, err = sql.Open("sqlite", databasePath)
	require.NoError(t, err)
	defer database.Close()
	var tables int
	require.NoError(t, database.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table'").Scan(&tables))
	require.Zero(t, tables, "preflight must not initialize a missing schema")
}

func TestMigrateCheckReportsPendingAndCurrentWithoutDDL(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "metadata with space #1.db")
	configPath := sqliteMigrationCheckConfig(t, databasePath)
	var output, stderr strings.Builder
	require.Equal(t, 0, run([]string{"migrate", "--config", configPath}, &output, &stderr), stderr.String())
	latest := assertCurrentMigrationVersion(t, output.String(), "migration driver=sqlite")

	output.Reset()
	stderr.Reset()
	require.Equal(t, 0, run([]string{"migrate", "--check", "--config", configPath}, &output, &stderr), stderr.String())
	require.Contains(t, output.String(), "migration_check role=metadata driver=sqlite current=")
	require.Contains(t, output.String(), "status=current")

	database, err := sql.Open("sqlite", databasePath)
	require.NoError(t, err)
	_, err = database.Exec("DELETE FROM schema_migrations WHERE version=?", latest)
	require.NoError(t, err)
	require.NoError(t, database.Close())
	before, err := os.ReadFile(databasePath)
	require.NoError(t, err)

	output.Reset()
	stderr.Reset()
	require.Equal(t, 2, run([]string{"migrate", "--check", "--config", configPath}, &output, &stderr))
	require.Contains(t, output.String(), "status=pending")
	require.Contains(t, stderr.String(), "schema migrations pending")
	after, err := os.ReadFile(databasePath)
	require.NoError(t, err)
	require.Equal(t, before, after, "read-only preflight must not change the SQLite database")
}

func TestMigrateCheckRejectsBrokenOrFutureHistory(t *testing.T) {
	for _, test := range []struct {
		name, sql, expected string
	}{
		{name: "gap", sql: "DELETE FROM schema_migrations WHERE version=2", expected: "non-contiguous schema_migrations history"},
		{name: "future", sql: "INSERT INTO schema_migrations(version) VALUES(999)", expected: "newer than this binary"},
	} {
		t.Run(test.name, func(t *testing.T) {
			databasePath := filepath.Join(t.TempDir(), "metadata.db")
			configPath := sqliteMigrationCheckConfig(t, databasePath)
			require.Equal(t, 0, run([]string{"migrate", "--config", configPath}, io.Discard, io.Discard))
			database, err := sql.Open("sqlite", databasePath)
			require.NoError(t, err)
			_, err = database.Exec(test.sql)
			require.NoError(t, err)
			require.NoError(t, database.Close())
			var stdout, stderr strings.Builder
			require.Equal(t, 1, run([]string{"migrate", "--check", "--config", configPath}, &stdout, &stderr))
			require.Contains(t, stdout.String()+stderr.String(), test.expected)
		})
	}
}

func TestMigrateCheckRequiresSeparateAuditAndRedactsDSN(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "metadata.db")
	configPath := sqliteMigrationCheckConfig(t, databasePath)
	require.Equal(t, 0, run([]string{"migrate", "--config", configPath}, io.Discard, io.Discard))
	contents, err := os.ReadFile(configPath)
	require.NoError(t, err)
	password := "audit-secret-must-not-appear"
	dsn := "postgres://audit:" + password + "@127.0.0.1:1/audit?sslmode=disable&connect_timeout=1"
	t.Setenv(config.AuditDSNEnv, dsn)
	separateConfig := strings.Replace(string(contents), "defaults:\n", "  audit:\n    separate: true\n    driver: postgres\ndefaults:\n", 1)
	separatePath := writeControlConfig(t, separateConfig)
	var stdout, stderr strings.Builder
	require.Equal(t, 1, run([]string{"migrate", "--check", "--config", separatePath}, &stdout, &stderr))
	require.Contains(t, stdout.String(), "migration_check role=metadata driver=sqlite")
	require.Contains(t, stderr.String(), "driver=postgres")
	require.NotContains(t, stdout.String()+stderr.String(), password)
	require.NotContains(t, stdout.String()+stderr.String(), dsn)
}
