package main

import (
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/stretchr/testify/require"
)

func TestDoctorReportsCurrentAndPendingWithoutWrites(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "doctor.db")
	configPath := sqliteMigrationCheckConfig(t, databasePath)
	require.Equal(t, 0, run([]string{"migrate", "--config", configPath}, io.Discard, io.Discard))
	before, err := os.ReadFile(databasePath)
	require.NoError(t, err)
	var output, stderr strings.Builder
	require.Equal(t, 0, run([]string{"doctor", "--config", configPath}, &output, &stderr), stderr.String())
	var report doctorReport
	require.NoError(t, json.Unmarshal([]byte(output.String()), &report))
	require.Equal(t, "ready", report.Status)
	require.True(t, report.ReadOnly)
	require.Equal(t, "ready", report.Checks[0].Status)
	require.Equal(t, "ready", report.Checks[1].Status)
	after, err := os.ReadFile(databasePath)
	require.NoError(t, err)
	require.Equal(t, before, after)

	database, err := sql.Open("sqlite", databasePath)
	require.NoError(t, err)
	_, err = database.Exec("DELETE FROM schema_migrations WHERE version=(SELECT MAX(version) FROM schema_migrations)")
	require.NoError(t, err)
	require.NoError(t, database.Close())
	before, err = os.ReadFile(databasePath)
	require.NoError(t, err)
	output.Reset()
	stderr.Reset()
	require.Equal(t, 2, run([]string{"doctor", "--config", configPath, "--format", "json"}, &output, &stderr))
	require.NoError(t, json.Unmarshal([]byte(output.String()), &report))
	require.Equal(t, "pending", report.Status)
	require.Equal(t, "pending", report.Checks[1].Status)
	after, err = os.ReadFile(databasePath)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestDoctorDoesNotCreateMissingSQLiteOrInstanceIdentity(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "missing.db")
	configPath := sqliteMigrationCheckConfig(t, databasePath)
	var output, stderr strings.Builder
	require.Equal(t, 1, run([]string{"doctor", "--config", configPath}, &output, &stderr))
	var report doctorReport
	require.NoError(t, json.Unmarshal([]byte(output.String()), &report))
	require.Equal(t, "blocked", report.Status)
	require.NoFileExists(t, databasePath)
	require.NoFileExists(t, databasePath+".instance-id")
	require.NoFileExists(t, configPath+".instance-id")
	require.NotContains(t, output.String(), databasePath)
}

func TestDoctorInvalidConfigurationDoesNotEchoSecrets(t *testing.T) {
	secret := "doctor-secret-must-never-leak"
	configPath := writeControlConfig(t, "store:\n  sqlite_path: './missing.db'\n  unknown: "+secret+"\n")
	var output, stderr strings.Builder
	require.Equal(t, 1, run([]string{"doctor", "--config", configPath, "--format", "text"}, &output, &stderr))
	require.Contains(t, output.String(), "status=blocked")
	require.Contains(t, output.String(), "action=")
	require.NotContains(t, output.String()+stderr.String(), secret)
}

func TestDoctorBlocksBrokenAndFutureMigrationHistory(t *testing.T) {
	for _, test := range []struct {
		name, statement string
	}{
		{name: "history gap", statement: "DELETE FROM schema_migrations WHERE version=2"},
		{name: "future version", statement: "INSERT INTO schema_migrations(version) VALUES(999)"},
	} {
		t.Run(test.name, func(t *testing.T) {
			databasePath := filepath.Join(t.TempDir(), "metadata.db")
			configPath := sqliteMigrationCheckConfig(t, databasePath)
			require.Equal(t, 0, run([]string{"migrate", "--config", configPath}, io.Discard, io.Discard))
			database, err := sql.Open("sqlite", databasePath)
			require.NoError(t, err)
			_, err = database.Exec(test.statement)
			require.NoError(t, err)
			require.NoError(t, database.Close())
			before, err := os.ReadFile(databasePath)
			require.NoError(t, err)
			var output, stderr strings.Builder
			require.Equal(t, 1, run([]string{"doctor", "--config", configPath}, &output, &stderr))
			var report doctorReport
			require.NoError(t, json.Unmarshal([]byte(output.String()), &report))
			require.Equal(t, "blocked", report.Status)
			require.Equal(t, "metadata_schema", report.Checks[1].Name)
			require.Equal(t, "blocked", report.Checks[1].Status)
			after, err := os.ReadFile(databasePath)
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}

func TestDoctorSeparatesAuditOutageAndRedactsDSN(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "metadata.db")
	configPath := sqliteMigrationCheckConfig(t, databasePath)
	contents, err := os.ReadFile(configPath)
	require.NoError(t, err)
	secret := "audit-doctor-secret"
	t.Setenv(config.AuditDSNEnv, "postgres://audit:"+secret+"@127.0.0.1:1/audit?sslmode=disable&connect_timeout=1")
	separateConfig := strings.Replace(string(contents), "defaults:\n", "  audit:\n    separate: true\n    driver: postgres\ndefaults:\n", 1)
	separatePath := writeControlConfig(t, separateConfig)
	// The metadata migration commits before the intentionally unreachable
	// independent audit target fails, leaving a valid separated metadata store.
	require.Equal(t, 1, run([]string{"migrate", "--config", separatePath}, io.Discard, io.Discard))
	var output, stderr strings.Builder
	require.Equal(t, 1, run([]string{"doctor", "--config", separatePath}, &output, &stderr))
	var report doctorReport
	require.NoError(t, json.Unmarshal([]byte(output.String()), &report))
	require.Equal(t, "blocked", report.Status)
	require.Equal(t, "metadata_schema", report.Checks[1].Name)
	require.Equal(t, "ready", report.Checks[1].Status)
	require.Equal(t, "audit_schema", report.Checks[2].Name)
	require.Equal(t, "blocked", report.Checks[2].Status)
	require.NotContains(t, output.String()+stderr.String(), secret)
}
