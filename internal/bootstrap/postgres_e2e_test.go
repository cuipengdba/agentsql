package bootstrap

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/redaction"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestAssemblePostgres18MetadataE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("postgres:18 bootstrap E2E is an integration test")
	}
	ctx := bootstrapDockerTestContext(t)
	const (
		databaseName = "agentsql"
		username     = "agentsql"
		password     = "bootstrap-pg-password"
	)
	container, err := postgrescontainer.Run(
		ctx,
		"postgres:18",
		postgrescontainer.WithDatabase(databaseName),
		postgrescontainer.WithUsername(username),
		postgrescontainer.WithPassword(password),
		postgrescontainer.BasicWaitStrategies(),
	)
	if err != nil {
		if container != nil {
			testcontainers.CleanupContainer(t, container)
		}
		require.NoError(t, err, "start postgres:18 (Docker daemon probe already succeeded)")
	}
	testcontainers.CleanupContainer(t, container)

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)
	dsn := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", username, password, host, port.Port(), databaseName)
	t.Setenv("AGENTSQL_STORE_METADATA_DSN", dsn)

	cfg := bootstrapTestConfig("")
	cfg.Store.Metadata = &config.MetadataStoreConfig{
		Driver:          "postgres",
		DSN:             "postgres://yaml:must-be-overridden@example/agentsql",
		MaxOpenConns:    4,
		MaxIdleConns:    2,
		ConnMaxLifetime: config.ConfigDuration(time.Minute),
	}
	runtime, err := Assemble(ctx, cfg, bootstrapTestSecret)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })
	require.NoError(t, runtime.Store.Ping(ctx))
	chainDatabase, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	exerciseCombinedGroupCommit(t, ctx, runtime, chainDatabase, store.DialectPostgres, 128)
	require.NoError(t, chainDatabase.Close())

	keyDigest := sha256.Sum256([]byte("bootstrap-pg-api-key"))
	created, err := runtime.Store.Agents().Create(ctx, model.Agent{
		ID: "bootstrap-pg-agent", Name: "Bootstrap PG Agent", Status: "active",
		APIKeyHash: hex.EncodeToString(keyDigest[:]), Level: "readonly",
	})
	require.NoError(t, err)
	loaded, err := runtime.Store.Agents().Get(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, created.ID, loaded.ID)
	require.Equal(t, created.Name, loaded.Name)

	width, offset := int64(25), int64(0)
	_, err = runtime.Store.MaskRules().Create(ctx, model.MaskRule{
		ID: "bootstrap-pg-range", ColumnName: "amount", SensitiveType: string(mask.TypeNumber),
		Algo: string(mask.AlgoRange), Enabled: true,
		RangeBucketWidth: &width, RangeBucketOffset: &offset,
	})
	require.NoError(t, err)
	require.NoError(t, runtime.Close())

	unsetRedactionHashKeyEnv(t)
	rangeRuntime, err := Assemble(ctx, cfg, bootstrapTestSecret)
	require.NoError(t, err, "a PostgreSQL-backed range-only rule set must activate without a hash key")
	t.Cleanup(func() { require.NoError(t, rangeRuntime.Close()) })
	redactor, err := rangeRuntime.redactors.RedactorFor(ctx, "pg-datasource")
	require.NoError(t, err)
	result, report := redactor.Apply(model.QueryResult{
		Columns: []string{"amount"}, Rows: [][]string{{"42"}},
	})
	require.Equal(t, "[25,50)", result.Rows[0][0])
	require.Equal(t, mask.TypeNumber, report.TouchedColumns[0])
	require.NoError(t, rangeRuntime.Close())

	// B3 S3: empty registry is #3, a fresh strong round restores readiness,
	// active mismatch is hard #2, and standby mismatch is readiness-only #4.
	encodedOne := base64.StdEncoding.EncodeToString([]byte("0123456789abcdefghijklmnopqrstuv"))
	encodedTwo := base64.StdEncoding.EncodeToString([]byte("ABCDEFGHIJKLMNOPQRSTUV0123456789"))
	cfg.Redaction.HashKeys = &config.RedactionHashKeysConfig{
		ActiveVersion: 1,
		Keys: []config.RedactionHashKeySpec{
			{ID: 1, KeyB64: &encodedOne}, {ID: 2, KeyB64: &encodedTwo},
		},
	}
	multiRuntime, err := Assemble(ctx, cfg, bootstrapTestSecret)
	require.NoError(t, err)
	ready, unmet := multiRuntime.RedactionReady()
	require.False(t, ready)
	require.Equal(t, []int{3}, unmet)
	observed, available := multiRuntime.RedactionReconciliation()
	require.True(t, available)
	require.Len(t, observed.Observed.Keys, 2)
	require.NoError(t, multiRuntime.Store.RedactionKeys().RegisterStandby(ctx, "1", observed.Observed.Keys[0].Commitment, "active", observed.Observed.Revision, nil))
	require.NoError(t, multiRuntime.Store.RedactionKeys().RegisterStandby(ctx, "2", observed.Observed.Keys[1].Commitment, "standby", observed.Observed.Revision, nil))
	_, _, _, err = multiRuntime.Store.RedactionKeys().MarkActiveCAS(ctx, "1")
	require.NoError(t, err)
	ready, _ = multiRuntime.RedactionReady()
	require.False(t, ready, "registry mutation cannot recover readiness without a new round")
	require.NoError(t, multiRuntime.RerunRedactionReconciliation(ctx))
	ready, unmet = multiRuntime.RedactionReady()
	require.True(t, ready)
	require.Empty(t, unmet)
	require.NoError(t, multiRuntime.Close())

	connection, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	_, err = connection.Exec(ctx, `UPDATE redaction_key_versions SET commitment=$1 WHERE id='1'`, strings.Repeat("f", 64))
	require.NoError(t, err)
	require.NoError(t, connection.Close(ctx))
	hardRuntime, err := Assemble(ctx, cfg, bootstrapTestSecret)
	require.Nil(t, hardRuntime)
	require.ErrorIs(t, err, redaction.ErrActiveCommitmentMismatch)
	require.NotContains(t, err.Error(), encodedOne)

	connection, err = pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	_, err = connection.Exec(ctx, `UPDATE redaction_key_versions SET commitment=$1 WHERE id='1'`, observed.Observed.Keys[0].Commitment)
	require.NoError(t, err)
	_, err = connection.Exec(ctx, `UPDATE redaction_key_versions SET commitment=$1 WHERE id='2'`, strings.Repeat("e", 64))
	require.NoError(t, err)
	require.NoError(t, connection.Close(ctx))
	standbyDriftRuntime, err := Assemble(ctx, cfg, bootstrapTestSecret)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, standbyDriftRuntime.Close()) })
	ready, unmet = standbyDriftRuntime.RedactionReady()
	require.False(t, ready)
	require.Equal(t, []int{4}, unmet)
}

func bootstrapDockerTestContext(t *testing.T) context.Context {
	t.Helper()
	unavailable, err := probeBootstrapDockerAvailable()
	if unavailable {
		t.Logf("docker daemon unavailable: %v", err)
		t.Skip("docker daemon unavailable")
	}
	require.NoError(t, err, "Docker probe failed after the client connected")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

func probeBootstrapDockerAvailable() (unavailable bool, err error) {
	clientConstructed := false
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("docker probe panic: %v", recovered)
			unavailable = !clientConstructed
		}
	}()
	probeContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dockerClient, err := testcontainers.NewDockerClient()
	if err != nil {
		return true, err
	}
	clientConstructed = true
	if _, err := dockerClient.Ping(probeContext); err != nil {
		_ = dockerClient.Close()
		return bootstrapDockerDaemonUnavailable(err), err
	}
	return false, dockerClient.Close()
}

func bootstrapDockerDaemonUnavailable(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"cannot connect to the docker daemon",
		"docker daemon is not running",
		"connection refused",
		"no such file or directory",
		"the system cannot find the file specified",
		"open //./pipe/docker_engine",
		"open \\\\.\\pipe\\docker_engine",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}
