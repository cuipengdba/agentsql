package main

import (
	"database/sql"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

const redactionKeyE2EKey1 = "0123456789abcdef0123456789abcdef"
const redactionKeyE2EKey2 = "fedcba9876543210fedcba9876543210"

func redactionKeyE2EManifest() string {
	key1 := []byte(redactionKeyE2EKey1)
	key2 := []byte(redactionKeyE2EKey2)
	return "redaction:\n  hash_keys:\n    active_version: 1\n    revision: rev-e2e\n    keys:\n" +
		"      - id: 1\n        key_b64: " + base64.StdEncoding.EncodeToString(key1) + "\n" +
		"      - id: 2\n        key_b64: " + base64.StdEncoding.EncodeToString(key2) + "\n"
}

// TestPostgres18RedactionKeySeparatedRelayE2E drives the full separated chain
// against real PostgreSQL 18 containers: reconcile -> mark-active -> relay
// idempotency -> switch -> retire, with management audit delivered via outbox.
func TestPostgres18RedactionKeySeparatedRelayE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("dual postgres:18 redaction-key relay E2E is an integration test")
	}
	ctx := commandDockerTestContext(t)
	t.Setenv("AGENTSQL_SECRET", "0123456789abcdef0123456789abcdef")
	metadataDSN := startCommandPostgres18Container(t, ctx, "agentsql_meta", "metadata-relay-password")
	auditDSN := startCommandPostgres18Container(t, ctx, "agentsql_audit", "audit-relay-password")
	t.Setenv("AGENTSQL_STORE_METADATA_DSN", metadataDSN)
	t.Setenv("AGENTSQL_STORE_AUDIT_DSN", auditDSN)
	configPath := writeControlConfig(t, fmt.Sprintf(`server:
  http_listen: "127.0.0.1:7781"
store:
  auto_migrate: false
  metadata:
    driver: postgres
    dsn: "postgres://yaml:ignored@example.invalid/meta"
  audit:
    separate: true
    driver: postgres
    dsn: "postgres://yaml:ignored@example.invalid/audit"
defaults:
  statement_timeout_ms: 5000
  row_limit: 1000
  max_conns_per_datasource: 5
  qps_per_agent: 20
theme:
  default: dark
%s`, redactionKeyE2EManifest()))

	var stdout, stderr strings.Builder
	require.Equal(t, 0, run([]string{"migrate", "--config", configPath}, &stdout, &stderr), stderr.String())
	require.Contains(t, stdout.String(), "metadata migration driver=postgres current=6 latest=6")
	require.Contains(t, stdout.String(), "audit migration driver=postgres current=4 latest=4")

	stdout.Reset()
	stderr.Reset()
	require.Equal(t, 0, run([]string{"redaction-key", "reconcile", "--config", configPath}, &stdout, &stderr), stderr.String())
	require.Contains(t, stdout.String(), "registered_standby=1,2")

	stdout.Reset()
	stderr.Reset()
	require.Equal(t, 0, run([]string{"redaction-key", "registry-mark-active", "--id", "1", "--config", configPath}, &stdout, &stderr), stderr.String())
	require.Contains(t, stdout.String(), "after=active revision=rev-e2e result=first_activated")

	metadataDB, err := sql.Open("pgx", metadataDSN)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, metadataDB.Close()) })
	auditDB, err := sql.Open("pgx", auditDSN)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, auditDB.Close()) })

	var pendingOutbox int
	require.NoError(t, metadataDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM management_audit_outbox WHERE delivered_at IS NULL").Scan(&pendingOutbox))
	require.Zero(t, pendingOutbox, "inline delivery should mark events delivered")

	var managementRows int
	require.NoError(t, auditDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_logs WHERE action LIKE 'redaction_key_%'").Scan(&managementRows))
	require.Equal(t, 2, managementRows)

	stdout.Reset()
	stderr.Reset()
	require.Equal(t, 0, run([]string{"redaction-key", "relay", "--once", "--config", configPath}, &stdout, &stderr), stderr.String())
	require.Contains(t, stdout.String(), "delivered=0")

	// Explicit fingerprint verification through a file (no plaintext in argv).
	plan, err := mask.BuildRedactionPlan(1, map[int][]byte{1: []byte(redactionKeyE2EKey1)})
	require.NoError(t, err)
	redactor, err := mask.NewRedactor([]mask.Rule{{Column: "value", SensitiveType: mask.TypeGeneric, Algorithm: mask.AlgoHash}}, mask.WithRedactionPlan(plan))
	require.NoError(t, err)
	result, _ := redactor.Apply(model.QueryResult{Columns: []string{"value"}, Rows: [][]string{{"plaintext"}}})
	fingerprint := result.Rows[0][0]
	plaintextPath := filepath.Join(t.TempDir(), "plaintext.txt")
	require.NoError(t, os.WriteFile(plaintextPath, []byte("plaintext"), 0o600))
	stdout.Reset()
	stderr.Reset()
	require.Equal(t, 0, run([]string{"redaction-key", "verify", "--id", "1", "--plaintext-file", plaintextPath, "--fingerprint", fingerprint, "--config", configPath}, &stdout, &stderr), stderr.String())
	require.Equal(t, "match=true\n", stdout.String())

	// Switch 1 -> 2 (legacy path), then retire 1.
	stdout.Reset()
	stderr.Reset()
	require.Equal(t, 0, run([]string{"redaction-key", "registry-mark-active", "--id", "2", "--config", configPath}, &stdout, &stderr), stderr.String())
	require.Contains(t, stdout.String(), "after=active revision=rev-e2e result=switched")

	stdout.Reset()
	stderr.Reset()
	require.Equal(t, 1, run([]string{"redaction-key", "registry-mark-retired", "--id", "2", "--config", configPath}, &stdout, &stderr))
	require.Contains(t, stderr.String(), "switch active version first")

	stdout.Reset()
	stderr.Reset()
	require.Equal(t, 0, run([]string{"redaction-key", "registry-mark-retired", "--id", "1", "--config", configPath}, &stdout, &stderr), stderr.String())
	require.Contains(t, stdout.String(), "after=retired")

	var states string
	require.NoError(t, metadataDB.QueryRowContext(ctx, "SELECT string_agg(id||':'||state, ',' ORDER BY id) FROM redaction_key_versions").Scan(&states))
	require.Equal(t, "1:retired,2:active", states)

	var deliveredRows int
	require.NoError(t, auditDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_logs WHERE action LIKE 'redaction_key_%' AND event_uuid IS NOT NULL").Scan(&deliveredRows))
	require.Equal(t, 4, deliveredRows)
}

// TestPostgres18RedactionKeyCombinedE2E verifies the combined deployment writes
// registry changes and management audit in one transaction (no outbox rows).
func TestPostgres18RedactionKeyCombinedE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("postgres:18 redaction-key combined E2E is an integration test")
	}
	ctx := commandDockerTestContext(t)
	t.Setenv("AGENTSQL_SECRET", "0123456789abcdef0123456789abcdef")
	dsn := startCommandPostgres18Container(t, ctx, "agentsql", "combined-password")
	t.Setenv("AGENTSQL_STORE_METADATA_DSN", dsn)
	t.Setenv("AGENTSQL_STORE_AUDIT_DSN", "")
	configPath := writeControlConfig(t, fmt.Sprintf(`server:
  http_listen: "127.0.0.1:7782"
store:
  auto_migrate: false
  metadata:
    driver: postgres
    dsn: "postgres://yaml:ignored@example.invalid/agentsql"
  audit:
    separate: false
defaults:
  statement_timeout_ms: 5000
  row_limit: 1000
  max_conns_per_datasource: 5
  qps_per_agent: 20
theme:
  default: dark
%s`, redactionKeyE2EManifest()))

	var stdout, stderr strings.Builder
	require.Equal(t, 0, run([]string{"migrate", "--config", configPath}, &stdout, &stderr), stderr.String())
	require.Contains(t, stdout.String(), "migration driver=postgres current=7 latest=7")

	stdout.Reset()
	stderr.Reset()
	require.Equal(t, 0, run([]string{"redaction-key", "reconcile", "--config", configPath}, &stdout, &stderr), stderr.String())
	require.Contains(t, stdout.String(), "registered_standby=1,2")

	stdout.Reset()
	stderr.Reset()
	require.Equal(t, 0, run([]string{"redaction-key", "registry-mark-active", "--id", "1", "--config", configPath}, &stdout, &stderr), stderr.String())
	require.Contains(t, stdout.String(), "before=standby after=active")

	database, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	var outboxRows int
	require.NoError(t, database.QueryRowContext(ctx, "SELECT COUNT(*) FROM management_audit_outbox").Scan(&outboxRows))
	require.Zero(t, outboxRows, "combined deployment must not use the outbox")

	var managementRows int
	require.NoError(t, database.QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_logs WHERE action LIKE 'redaction_key_%'").Scan(&managementRows))
	require.Equal(t, 2, managementRows)

	stdout.Reset()
	stderr.Reset()
	require.Equal(t, 0, run([]string{"redaction-key", "relay", "--once", "--config", configPath}, &stdout, &stderr), stderr.String())
	require.Contains(t, stdout.String(), "relay not required")
}
