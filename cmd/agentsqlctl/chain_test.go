package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/stretchr/testify/require"
)

func TestChainStatusDisabledAndJSON(t *testing.T) {
	configPath, _ := prepareChainSQLite(t)

	var stdout, stderr strings.Builder
	require.Equal(t, 0, run([]string{"chain", "status", "--config", configPath}, &stdout, &stderr), stderr.String())
	require.Contains(t, stdout.String(), "chain_id=management")
	require.Contains(t, stdout.String(), "status=DISABLED")
	require.Contains(t, stdout.String(), "mode=null")
	require.Contains(t, stdout.String(), "head_seq=0")
	require.Contains(t, stdout.String(), "head_id=null")
	require.Contains(t, stdout.String(), "protected_since=null")
	require.Contains(t, stdout.String(), "genesis_at=null")
	require.Contains(t, stdout.String(), "observed_instance=null")
	require.Contains(t, stdout.String(), "result=null")
	require.Contains(t, stdout.String(), "last_verified_at=null")
	require.Contains(t, stdout.String(), "break_seq=null")
	require.Contains(t, stdout.String(), "break_reason=null")

	stdout.Reset()
	stderr.Reset()
	require.Equal(t, 0, run([]string{"chain", "status", "--json", "-c", configPath}, &stdout, &stderr), stderr.String())
	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(stdout.String()), &decoded))
	require.Equal(t, "management", decoded["chain_id"])
	require.Equal(t, "DISABLED", decoded["status"])
	require.Nil(t, decoded["mode"])
	require.Equal(t, float64(0), decoded["head_seq"])
	for _, field := range []string{
		"head_id", "protected_since", "genesis_at", "observed_instance",
		"result", "last_verified_at", "break_seq", "break_reason",
	} {
		_, present := decoded[field]
		require.True(t, present, field)
	}
}

func TestChainProvisionKeylessAndVerifyExitCodes(t *testing.T) {
	configPath, databasePath := prepareChainSQLite(t)
	database := openChainSQLiteForTest(t, databasePath)
	_, err := database.Exec(`INSERT INTO audit_logs(decision) VALUES('allow')`)
	require.NoError(t, err)
	require.NoError(t, database.Close())

	var stdout, stderr strings.Builder
	exitCode := run([]string{"chain", "provision", "--mode", "keyless", "--owner", "chain-test", "-c", configPath}, &stdout, &stderr)
	require.Equal(t, 0, exitCode, stderr.String())
	require.Contains(t, stdout.String(), "status=ACTIVE")
	require.Contains(t, stdout.String(), "result=VALID_AT_OBSERVED_HEAD")
	require.Contains(t, stdout.String(), "total=1")

	stdout.Reset()
	stderr.Reset()
	require.Equal(t, 0, run([]string{"chain", "verify", "--mode", "keyless", "-c", configPath}, &stdout, &stderr), stderr.String())
	require.Contains(t, stdout.String(), "result=VALID_AT_OBSERVED_HEAD")

	stdout.Reset()
	stderr.Reset()
	require.Equal(t, 0, run([]string{"chain", "status", "-c", configPath}, &stdout, &stderr), stderr.String())
	require.Contains(t, stdout.String(), "status=ACTIVE")
	require.Contains(t, stdout.String(), "mode=keyless")
	require.Contains(t, stdout.String(), "result=VALID_AT_OBSERVED_HEAD")
	require.NotContains(t, stdout.String(), "observed_instance=null")
	require.NotContains(t, stdout.String(), "last_verified_at=null")

	database = openChainSQLiteForTest(t, databasePath)
	_, err = database.Exec(`DROP TRIGGER trg_audit_logs_chain_contract_update`)
	require.NoError(t, err)
	_, err = database.Exec(`UPDATE audit_logs SET self_hash=? WHERE id=(SELECT MIN(id) FROM audit_logs)`, strings.Repeat("f", 64))
	require.NoError(t, err)
	require.NoError(t, database.Close())

	stdout.Reset()
	stderr.Reset()
	exitCode = run([]string{"chain", "verify", "--mode", "keyless", "-c", configPath}, &stdout, &stderr)
	require.Equal(t, 2, exitCode, stderr.String())
	require.Contains(t, stdout.String(), "break_reason=self_mismatch")
}

func TestChainHMACKeys(t *testing.T) {
	const validKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	t.Run("provision requires key", func(t *testing.T) {
		configPath, _ := prepareChainSQLite(t)
		var stderr strings.Builder
		require.NotZero(t, run([]string{"chain", "provision", "--mode", "hmac", "-c", configPath}, io.Discard, &stderr))
		require.Contains(t, stderr.String(), "requires --key-hex or --key-env")
	})

	t.Run("missing verify key is exit three and a valid key succeeds", func(t *testing.T) {
		configPath, _ := prepareChainSQLite(t)
		var stdout, stderr strings.Builder
		require.Equal(t, 0, run([]string{"chain", "provision", "--mode", "hmac", "--key-hex", validKey, "-c", configPath}, &stdout, &stderr), stderr.String())

		stdout.Reset()
		stderr.Reset()
		exitCode := run([]string{"chain", "verify", "--mode", "hmac", "-c", configPath}, &stdout, &stderr)
		require.Equal(t, 3, exitCode, stderr.String())
		require.Contains(t, stdout.String(), "break_reason=key_unavailable")

		stdout.Reset()
		stderr.Reset()
		require.Equal(t, 0, run([]string{"chain", "verify", "--mode", "hmac", "--key-hex", validKey, "-c", configPath}, &stdout, &stderr), stderr.String())
		require.Contains(t, stdout.String(), "result=VALID_AT_OBSERVED_HEAD")
	})

	t.Run("key environment", func(t *testing.T) {
		configPath, _ := prepareChainSQLite(t)
		t.Setenv("AGENTSQL_CHAIN_TEST_KEY", validKey)
		var stderr strings.Builder
		require.Equal(t, 0, run([]string{"chain", "provision", "--mode", "hmac", "--key-env", "AGENTSQL_CHAIN_TEST_KEY", "-c", configPath}, io.Discard, &stderr), stderr.String())
	})
}

func TestChainDomainMappingAndMissingState(t *testing.T) {
	configPath, _ := prepareChainSQLite(t)
	var stdout, stderr strings.Builder
	require.Equal(t, 0, run([]string{"chain", "status", "-c", configPath}, &stdout, &stderr), stderr.String())
	require.Contains(t, stdout.String(), "chain_id=management")

	stdout.Reset()
	stderr.Reset()
	require.Equal(t, 1, run([]string{"chain", "status", "--domain", "traffic", "-c", configPath}, &stdout, &stderr))
	require.Contains(t, stderr.String(), `domain "traffic"`)
	require.Contains(t, stderr.String(), "selected metadata database")
	require.Contains(t, stderr.String(), "does not exist")

	stderr.Reset()
	require.Equal(t, 1, run([]string{"chain", "status", "--domain", "invalid", "-c", configPath}, io.Discard, &stderr))
	require.Contains(t, stderr.String(), "auto, management, or traffic")
}

func TestChainKeyFlagValidation(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "short", args: []string{"--key-hex", "abcd"}, want: "exactly 64"},
		{name: "not hex", args: []string{"--key-hex", strings.Repeat("z", 64)}, want: "only hexadecimal"},
		{name: "mutually exclusive", args: []string{"--key-hex", strings.Repeat("a", 64), "--key-env", "CHAIN_KEY"}, want: "mutually exclusive"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			args := append([]string{"chain", "verify", "--mode", "hmac"}, test.args...)
			var stderr strings.Builder
			require.Equal(t, 1, run(args, io.Discard, &stderr))
			require.Contains(t, stderr.String(), test.want)
		})
	}
}

func TestStaticChainManifestRejectsModeVersionMismatch(t *testing.T) {
	_, err := (&staticChainManifest{mode: "keyless", version: 1}).CurrentKeyVersion(context.Background())
	require.ErrorContains(t, err, "version 0")
	_, err = (&staticChainManifest{mode: "hmac", version: 0}).CurrentKeyVersion(context.Background())
	require.ErrorContains(t, err, "version 1")
	_, err = (&staticChainManifest{mode: "unknown", version: 1}).ExpectedMode(context.Background())
	require.ErrorContains(t, err, "invalid")
}

func prepareChainSQLite(t *testing.T) (configPath, databasePath string) {
	t.Helper()
	t.Setenv("AGENTSQL_STORE_METADATA_DSN", "")
	t.Setenv("AGENTSQL_STORE_AUDIT_DSN", "")
	t.Setenv("AGENTSQL_SECRET", "")
	databasePath = filepath.Join(t.TempDir(), "agentsql.db")
	database := openChainSQLiteForTest(t, databasePath)
	require.NoError(t, store.MigrateMetadata(context.Background(), database, store.DialectSQLite, false))
	require.NoError(t, database.Close())
	configContents := strings.Replace(defaultConfigTemplate, "./data/agentsql.db", filepath.ToSlash(databasePath), 1)
	configPath = writeControlConfig(t, configContents)
	return configPath, databasePath
}

func openChainSQLiteForTest(t *testing.T, path string) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	require.NoError(t, database.Ping())
	return database
}

func TestChainErrorsDoNotEchoKeys(t *testing.T) {
	secret := strings.Repeat("g", 64)
	var stderr strings.Builder
	require.Equal(t, 1, run([]string{"chain", "verify", "--mode", "hmac", "--key-hex", secret}, io.Discard, &stderr))
	require.NotContains(t, stderr.String(), secret)
}
