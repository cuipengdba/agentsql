package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/model"
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
}

func bootstrapDockerTestContext(t *testing.T) context.Context {
	t.Helper()
	unavailable, err := probeBootstrapDockerAvailable()
	if unavailable {
		t.Logf("docker daemon unavailable: %v", err)
		t.Skip("docker daemon unavailable")
	}
	require.NoError(t, err, "Docker probe failed after the client connected")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
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
