package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestRedactionKeyCombinedLifecycleAndVerify(t *testing.T) {
	t.Setenv("AGENTSQL_SECRET", "0123456789abcdef0123456789abcdef")
	t.Setenv("AGENTSQL_STORE_METADATA_DSN", "")
	t.Setenv("AGENTSQL_STORE_AUDIT_DSN", "")
	root := t.TempDir()
	databasePath := filepath.Join(root, "agentsql.db")
	configPath := filepath.Join(root, "config.yaml")
	key1 := []byte("0123456789abcdef0123456789abcdef")
	key2 := []byte("fedcba9876543210fedcba9876543210")
	contents := strings.Replace(defaultConfigTemplate, "./data/agentsql.db", filepath.ToSlash(databasePath), 1) +
		"redaction:\n  hash_keys:\n    active_version: 1\n    revision: rev-1\n    keys:\n" +
		"      - id: 1\n        key_b64: " + base64.StdEncoding.EncodeToString(key1) + "\n" +
		"      - id: 2\n        key_b64: " + base64.StdEncoding.EncodeToString(key2) + "\n"
	require.NoError(t, os.WriteFile(configPath, []byte(contents), 0o600))

	var stdout, stderr strings.Builder
	require.Equal(t, 0, run([]string{"redaction-key", "reconcile", "--config", configPath}, &stdout, &stderr), stderr.String())
	require.Contains(t, stdout.String(), "registered_standby=1,2")
	require.NotContains(t, stdout.String()+stderr.String(), string(key1))

	stdout.Reset()
	stderr.Reset()
	require.Equal(t, 0, run([]string{"redaction-key", "registry-mark-active", "--id", "1", "--config", configPath}, &stdout, &stderr), stderr.String())
	require.Contains(t, stdout.String(), "before=standby after=active")

	stdout.Reset()
	stderr.Reset()
	require.Equal(t, 1, run([]string{"redaction-key", "registry-mark-retired", "--id", "1", "--config", configPath}, &stdout, &stderr))
	require.Contains(t, stderr.String(), "switch active version first")

	stdout.Reset()
	stderr.Reset()
	require.Equal(t, 0, run([]string{"redaction-key", "registry-mark-retired", "--id", "2", "--config", configPath}, &stdout, &stderr), stderr.String())
	require.Contains(t, stdout.String(), "after=retired")

	database, err := sql.Open("sqlite", databasePath)
	require.NoError(t, err)
	defer database.Close()
	var activeCount, outboxCount, auditCount int
	var auditDetails string
	require.NoError(t, database.QueryRow("SELECT COUNT(*) FROM redaction_key_versions WHERE state='active'").Scan(&activeCount))
	require.NoError(t, database.QueryRow("SELECT COUNT(*) FROM management_audit_outbox").Scan(&outboxCount))
	require.NoError(t, database.QueryRow("SELECT COUNT(*) FROM audit_logs WHERE action LIKE 'redaction_key_%'").Scan(&auditCount))
	require.NoError(t, database.QueryRow("SELECT COALESCE(GROUP_CONCAT(details_json),'') FROM audit_logs WHERE action LIKE 'redaction_key_%'").Scan(&auditDetails))
	require.Equal(t, 1, activeCount)
	require.Zero(t, outboxCount)
	require.Equal(t, 3, auditCount)
	require.NotContains(t, auditDetails, string(key1))
	require.NotContains(t, auditDetails, base64.StdEncoding.EncodeToString(key1))

	plan, err := mask.BuildRedactionPlan(1, map[int][]byte{1: key1})
	require.NoError(t, err)
	redactor, err := mask.NewRedactor([]mask.Rule{{Column: "value", SensitiveType: mask.TypeGeneric, Algorithm: mask.AlgoHash}}, mask.WithRedactionPlan(plan))
	require.NoError(t, err)
	result, _ := redactor.Apply(model.QueryResult{Columns: []string{"value"}, Rows: [][]string{{"plaintext"}}})
	fingerprint := result.Rows[0][0]
	plaintextPath := filepath.Join(root, "plaintext.txt")
	require.NoError(t, os.WriteFile(plaintextPath, []byte("plaintext"), 0o600))
	stdout.Reset()
	stderr.Reset()
	require.Equal(t, 0, run([]string{"redaction-key", "verify", "--id", "1", "--plaintext-file", plaintextPath, "--fingerprint", fingerprint, "--config", configPath}, &stdout, &stderr), stderr.String())
	require.Equal(t, "match=true\n", stdout.String())
	stdout.Reset()
	stderr.Reset()
	require.Equal(t, 1, run([]string{"redaction-key", "verify", "--id", "1", "--plaintext-file", plaintextPath, "--fingerprint", fingerprint + "x", "--config", configPath}, &stdout, &stderr))
	require.Equal(t, "match=false\n", stdout.String())

	command := newRootCommand()
	command.SetIn(bytes.NewBufferString("plaintext"))
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SetArgs([]string{"redaction-key", "verify", "--id", strconv.Itoa(1), "--plaintext-file", "-", "--fingerprint", fingerprint, "--config", configPath})
	require.NoError(t, command.ExecuteContext(context.Background()))
	command = newRootCommand()
	command.SetArgs([]string{"redaction-key", "verify", "plaintext", "--id", "1", "--plaintext-file", "-", "--fingerprint", fingerprint, "--config", configPath})
	require.Error(t, command.Execute())
}

func TestRedactionKeySeparatedAuditUnavailableCommitsAndReturnsThree(t *testing.T) {
	t.Setenv("AGENTSQL_SECRET", "0123456789abcdef0123456789abcdef")
	t.Setenv(config.AuditDSNEnv, "postgres://agentsql:unavailable@127.0.0.1:1/audit?sslmode=disable&connect_timeout=1")
	root := t.TempDir()
	databasePath := filepath.Join(root, "metadata.db")
	configPath := filepath.Join(root, "config.yaml")
	contents := strings.Replace(defaultConfigTemplate, "./data/agentsql.db", filepath.ToSlash(databasePath), 1)
	contents = strings.Replace(contents,
		"  auto_migrate: true              # false verifies schema versions without DDL\n",
		"  auto_migrate: true              # false verifies schema versions without DDL\n  audit:\n    separate: true\n    driver: postgres\n    dsn: postgres://yaml:ignored@127.0.0.1:1/audit\n", 1)
	key := []byte("0123456789abcdef0123456789abcdef")
	contents += "redaction:\n  hash_keys:\n    active_version: 1\n    revision: rev-1\n    keys:\n      - id: 1\n        key_b64: " + base64.StdEncoding.EncodeToString(key) + "\n"
	require.NoError(t, os.WriteFile(configPath, []byte(contents), 0o600))

	var stdout, stderr strings.Builder
	require.Equal(t, 3, run([]string{"redaction-key", "reconcile", "--config", configPath}, &stdout, &stderr))
	require.Contains(t, stderr.String(), "变更已提交，管理审计待投递")
	require.Contains(t, stderr.String(), "event_uuid=")
	require.Contains(t, stderr.String(), "redaction-key relay")
	require.NotContains(t, stdout.String()+stderr.String(), string(key))
	stdout.Reset()
	stderr.Reset()
	require.Equal(t, 3, run([]string{"redaction-key", "registry-mark-active", "--id", "1", "--config", configPath}, &stdout, &stderr))
	require.Contains(t, stdout.String(), "before=standby after=active")
	require.Contains(t, stderr.String(), "event_uuid=")

	database, err := sql.Open("sqlite", databasePath)
	require.NoError(t, err)
	defer database.Close()
	var registered, pending int
	var outboxDetails string
	require.NoError(t, database.QueryRow("SELECT COUNT(*) FROM redaction_key_versions WHERE id='1' AND state='active'").Scan(&registered))
	require.NoError(t, database.QueryRow("SELECT COUNT(*) FROM management_audit_outbox WHERE delivered_at IS NULL").Scan(&pending))
	require.NoError(t, database.QueryRow("SELECT COALESCE(GROUP_CONCAT(details_json),'') FROM management_audit_outbox").Scan(&outboxDetails))
	require.Equal(t, 1, registered)
	require.Equal(t, 2, pending)
	require.NotContains(t, outboxDetails, string(key))
	require.NotContains(t, outboxDetails, base64.StdEncoding.EncodeToString(key))
}
