package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/bootstrap"
	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/mcpserver"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

const redactionKeyE2EKey1 = "0123456789abcdef0123456789abcdef"
const redactionKeyE2EKey2 = "fedcba9876543210fedcba9876543210"
const redactionKeyE2EPlaintext = "restart-switch-fixed-plaintext"

func redactionKeyE2EManifest(activeVersion int) string {
	key1 := []byte(redactionKeyE2EKey1)
	key2 := []byte(redactionKeyE2EKey2)
	return fmt.Sprintf("redaction:\n  hash_keys:\n    active_version: %d\n    revision: rev-e2e\n    keys:\n", activeVersion) +
		"      - id: 1\n        key_b64: " + base64.StdEncoding.EncodeToString(key1) + "\n" +
		"      - id: 2\n        key_b64: " + base64.StdEncoding.EncodeToString(key2) + "\n"
}

// TestPostgres18RedactionKeySeparatedRelayE2E drives restart-only activation,
// rollback, retirement, and outbox delivery against two real PostgreSQL 18
// containers. Each active selector gets a fresh production assembly.
func TestPostgres18RedactionKeySeparatedRelayE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("dual postgres:18 redaction-key restart/rollback E2E is an integration test")
	}
	ctx := commandDockerTestContext(t)
	t.Setenv("AGENTSQL_SECRET", "0123456789abcdef0123456789abcdef")
	metadataDSN := startCommandPostgres18Container(t, ctx, "agentsql_meta", "metadata-relay-password")
	auditDSN := startCommandPostgres18Container(t, ctx, "agentsql_audit", "audit-relay-password")
	t.Setenv("AGENTSQL_STORE_METADATA_DSN", metadataDSN)
	t.Setenv("AGENTSQL_STORE_AUDIT_DSN", auditDSN)
	configPath := writeControlConfig(t, redactionKeyE2EConfig(true, 7781, 1))

	runRedactionKeyE2EPreparation(t, configPath, true)
	metadataDB := openRedactionKeyE2EDB(t, metadataDSN)
	auditDB := openRedactionKeyE2EDB(t, auditDSN)

	var pendingOutbox int
	require.NoError(t, metadataDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM management_audit_outbox WHERE delivered_at IS NULL").Scan(&pendingOutbox))
	require.Zero(t, pendingOutbox, "inline delivery should mark events delivered")

	var stdout, stderr strings.Builder
	require.Equal(t, 0, run([]string{"redaction-key", "relay", "--once", "--config", configPath}, &stdout, &stderr), stderr.String())
	require.Contains(t, stdout.String(), "delivered=0")

	exerciseRestartSwitchRollbackE2E(t, ctx, configPath, metadataDB)

	require.NoError(t, metadataDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM management_audit_outbox WHERE delivered_at IS NULL").Scan(&pendingOutbox))
	require.Zero(t, pendingOutbox)
	assertManagementAuditComplete(t, ctx, auditDB)
}

// TestPostgres18RedactionKeyCombinedE2E runs the same restart-only activation
// and rollback against one PostgreSQL 18 database and asserts direct audit
// writes without outbox use.
func TestPostgres18RedactionKeyCombinedE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("postgres:18 redaction-key combined restart/rollback E2E is an integration test")
	}
	ctx := commandDockerTestContext(t)
	t.Setenv("AGENTSQL_SECRET", "0123456789abcdef0123456789abcdef")
	dsn := startCommandPostgres18Container(t, ctx, "agentsql", "combined-password")
	t.Setenv("AGENTSQL_STORE_METADATA_DSN", dsn)
	t.Setenv("AGENTSQL_STORE_AUDIT_DSN", "")
	configPath := writeControlConfig(t, redactionKeyE2EConfig(false, 7782, 1))

	runRedactionKeyE2EPreparation(t, configPath, false)
	database := openRedactionKeyE2EDB(t, dsn)
	exerciseRestartSwitchRollbackE2E(t, ctx, configPath, database)

	var outboxRows int
	require.NoError(t, database.QueryRowContext(ctx, "SELECT COUNT(*) FROM management_audit_outbox").Scan(&outboxRows))
	require.Zero(t, outboxRows, "combined deployment must not use the outbox")
	assertManagementAuditComplete(t, ctx, database)

	var stdout, stderr strings.Builder
	require.Equal(t, 0, run([]string{"redaction-key", "relay", "--once", "--config", configPath}, &stdout, &stderr), stderr.String())
	require.Contains(t, stdout.String(), "relay not required")
}

func redactionKeyE2EConfig(separated bool, port, activeVersion int) string {
	audit := "  audit:\n    separate: false\n"
	metadataDSN := "postgres://yaml:ignored@example.invalid/agentsql"
	if separated {
		metadataDSN = "postgres://yaml:ignored@example.invalid/meta"
		audit = "  audit:\n    separate: true\n    driver: postgres\n    dsn: \"postgres://yaml:ignored@example.invalid/audit\"\n"
	}
	return fmt.Sprintf(`server:
  http_listen: "127.0.0.1:%d"
  console_enabled: false
store:
  auto_migrate: false
  metadata:
    driver: postgres
    dsn: %q
%scolumn_authorization:
  enabled: false
mcp:
  sessions:
    enabled: false
defaults:
  statement_timeout_ms: 5000
  row_limit: 1000
  max_conns_per_datasource: 5
  qps_per_agent: 20
theme:
  default: dark
%s`, port, metadataDSN, audit, redactionKeyE2EManifest(activeVersion))
}

func runRedactionKeyE2EPreparation(t *testing.T, configPath string, separated bool) {
	t.Helper()
	var stdout, stderr strings.Builder
	require.Equal(t, 0, run([]string{"migrate", "--config", configPath}, &stdout, &stderr), stderr.String())
	if separated {
		require.Contains(t, stdout.String(), "metadata migration driver=postgres current=9 latest=9")
		require.Contains(t, stdout.String(), "audit migration driver=postgres current=5 latest=5")
	} else {
		require.Contains(t, stdout.String(), "migration driver=postgres current=10 latest=10")
	}
	runRedactionKeyCommand(t, configPath, []string{"reconcile"}, "registered_standby=1,2")
	runRedactionKeyCommand(t, configPath, []string{"registry-mark-active", "--id", "1"}, "after=active")
}

func exerciseRestartSwitchRollbackE2E(t *testing.T, ctx context.Context, activeOneConfig string, metadataDB *sql.DB) {
	t.Helper()
	activeTwoConfig := writeControlConfig(t, redactionKeyE2EConfig(configUsesSeparateAudit(t, activeOneConfig), 7792, 2))

	// Phase A ends before phase B is assembled: this is the stop-write/drain
	// boundary of the restart model. No old redactor is reused after this scope.
	phaseOneFingerprint := func() string {
		redactor, assembly := loadE2ERedactor(t, activeOneConfig)
		fingerprint, report := applyE2EFingerprint(t, redactor)
		require.Regexp(t, `^h\.[0-9a-f]{32}$`, fingerprint)
		require.NotNil(t, report.HashKeyVersion)
		require.Equal(t, 1, *report.HashKeyVersion)
		matched, err := assembly.Verify(1, redactionKeyE2EPlaintext, fingerprint)
		require.NoError(t, err)
		require.True(t, matched)
		return fingerprint
	}()

	runRedactionKeyCommand(t, activeOneConfig, []string{"registry-mark-active", "--id", "2"}, "result=switched")
	phaseTwoFingerprint := func() string {
		redactor, assembly := loadE2ERedactor(t, activeTwoConfig)
		fingerprint, report := applyE2EFingerprint(t, redactor)
		require.Regexp(t, `^h\.2\.[0-9a-f]{32}$`, fingerprint)
		require.NotEqual(t, phaseOneFingerprint, fingerprint, "rotation must split equality across versions")
		require.NotNil(t, report.HashKeyVersion)
		require.Equal(t, 2, *report.HashKeyVersion)

		matched, err := assembly.Verify(1, redactionKeyE2EPlaintext, phaseOneFingerprint)
		require.NoError(t, err)
		require.True(t, matched, "legacy v1 fingerprint remains verifiable")
		matched, err = assembly.Verify(2, redactionKeyE2EPlaintext, fingerprint)
		require.NoError(t, err)
		require.True(t, matched)
		matched, err = mask.VerifyFingerprint(2, []byte(redactionKeyE2EKey1), redactionKeyE2EPlaintext, fingerprint)
		require.NoError(t, err)
		require.False(t, matched, "wrong material must not verify")
		matched, err = assembly.Verify(3, redactionKeyE2EPlaintext, fingerprint)
		require.NoError(t, err)
		require.False(t, matched, "unknown version must not verify")
		assertRestartReconciledAndReady(t, ctx, activeTwoConfig, 2)
		return fingerprint
	}()

	// Roll back by CAS, discard the v2 assembly, and construct another fresh v1
	// assembly. Determinism restores the historical v1 bytes exactly.
	runRedactionKeyCommand(t, activeTwoConfig, []string{"registry-mark-active", "--id", "1"}, "result=switched")
	rollbackRedactor, rollbackAssembly := loadE2ERedactor(t, activeOneConfig)
	rollbackFingerprint, rollbackReport := applyE2EFingerprint(t, rollbackRedactor)
	require.Equal(t, phaseOneFingerprint, rollbackFingerprint)
	require.NotNil(t, rollbackReport.HashKeyVersion)
	require.Equal(t, 1, *rollbackReport.HashKeyVersion)
	matched, err := rollbackAssembly.Verify(2, redactionKeyE2EPlaintext, phaseTwoFingerprint)
	require.NoError(t, err)
	require.True(t, matched, "rolled-back v2 remains legacy-verifiable")
	assertRestartReconciledAndReady(t, ctx, activeOneConfig, 1)

	runRedactionKeyCommand(t, activeOneConfig, []string{"registry-mark-retired", "--id", "2"}, "after=retired")
	plaintextPath := filepath.Join(t.TempDir(), "plaintext.txt")
	require.NoError(t, os.WriteFile(plaintextPath, []byte(redactionKeyE2EPlaintext), 0o600))
	var stdout, stderr strings.Builder
	exitCode := run([]string{
		"redaction-key", "verify", "--id", "2", "--plaintext-file", plaintextPath,
		"--fingerprint", phaseTwoFingerprint, "--config", activeOneConfig,
	}, &stdout, &stderr)
	require.Equal(t, 1, exitCode)
	require.Equal(t, "match=false\n", stdout.String())
	require.Contains(t, stderr.String(), "fingerprint does not match")

	var states string
	require.NoError(t, metadataDB.QueryRowContext(ctx, "SELECT string_agg(id||':'||state, ',' ORDER BY id) FROM redaction_key_versions").Scan(&states))
	require.Equal(t, "1:active,2:retired", states)
}

func loadE2ERedactor(t *testing.T, configPath string) (mask.Redactor, config.RedactionAssembly) {
	t.Helper()
	loaded, err := config.Load(configPath)
	require.NoError(t, err)
	resolved, err := config.ResolveRedaction(&loaded, os.LookupEnv)
	require.NoError(t, err)
	defer resolved.Clear()
	assembly, err := config.BuildRedactionAssembly(resolved)
	require.NoError(t, err)
	redactor, err := mask.NewRedactor(
		[]mask.Rule{{Column: "value", SensitiveType: mask.TypeGeneric, Algorithm: mask.AlgoHash}},
		assembly.PlanOptions...,
	)
	require.NoError(t, err)
	return redactor, assembly
}

func applyE2EFingerprint(t *testing.T, redactor mask.Redactor) (string, mask.RedactReport) {
	t.Helper()
	result, report := redactor.Apply(model.QueryResult{
		Columns: []string{"value"}, Rows: [][]string{{redactionKeyE2EPlaintext}},
	})
	require.Equal(t, 1, report.MaskedCells)
	return result.Rows[0][0], report
}

func assertRestartReconciledAndReady(t *testing.T, ctx context.Context, configPath string, activeVersion int) {
	t.Helper()
	loaded, err := config.Load(configPath)
	require.NoError(t, err)
	runtime, err := bootstrap.Assemble(ctx, loaded, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	require.NoError(t, runtime.RerunRedactionReconciliation(ctx))
	reconciliation, available := runtime.RedactionReconciliation()
	require.True(t, available)
	require.True(t, reconciliation.Ready)
	require.Empty(t, reconciliation.Unsatisfied)
	require.Empty(t, reconciliation.Warnings)
	require.Empty(t, reconciliation.Information)
	require.Equal(t, activeVersion, reconciliation.Observed.ActiveVersion)
	require.Equal(t, "rev-e2e", reconciliation.Observed.Revision)
	require.Len(t, reconciliation.Observed.Keys, 2)

	handler, err := mcpserver.NewHTTPHandler(runtime, loaded, zerolog.Nop())
	require.NoError(t, err)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	require.Equal(t, http.StatusOK, response.Code)
	require.JSONEq(t, `{"status":"ready","b2":{"state":"feature-off","reason":"B2_FEATURE_OFF","protocol":2}}`, response.Body.String())
	require.NoError(t, runtime.Close(), "closing the old runtime is the drain point before the next assembly")
}

func runRedactionKeyCommand(t *testing.T, configPath string, arguments []string, contains string) {
	t.Helper()
	args := append([]string{"redaction-key"}, arguments...)
	args = append(args, "--config", configPath)
	var stdout, stderr strings.Builder
	require.Equal(t, 0, run(args, &stdout, &stderr), stderr.String())
	require.Contains(t, stdout.String(), contains)
}

func configUsesSeparateAudit(t *testing.T, configPath string) bool {
	t.Helper()
	loaded, err := config.Load(configPath)
	require.NoError(t, err)
	return loaded.Store.Audit != nil && loaded.Store.Audit.Separate
}

func openRedactionKeyE2EDB(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	database, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	return database
}

func assertManagementAuditComplete(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	var total, reconciles, activations, switches, retirements, legacyRetirements int
	err := database.QueryRowContext(ctx, `
SELECT COUNT(*),
       COUNT(*) FILTER (WHERE action='redaction_key_reconcile'),
       COUNT(*) FILTER (WHERE action='redaction_key_mark_active'),
       COUNT(*) FILTER (WHERE action='redaction_key_mark_active' AND details_json::jsonb->>'result'='switched'),
       COUNT(*) FILTER (WHERE action='redaction_key_mark_retired'),
       COUNT(*) FILTER (WHERE action='redaction_key_mark_retired'
                         AND details_json::jsonb->>'before_state'='legacy'
                         AND details_json::jsonb->>'after_state'='retired')
FROM audit_logs
WHERE action LIKE 'redaction_key_%' AND event_uuid IS NOT NULL AND details_json IS NOT NULL`).Scan(
		&total, &reconciles, &activations, &switches, &retirements, &legacyRetirements,
	)
	require.NoError(t, err)
	require.Equal(t, 5, total)
	require.Equal(t, 1, reconciles)
	require.Equal(t, 3, activations)
	require.Equal(t, 2, switches)
	require.Equal(t, 1, retirements)
	require.Equal(t, 1, legacyRetirements)
}
