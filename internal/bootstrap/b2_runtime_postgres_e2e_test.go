package bootstrap

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestB2RuntimeActivationPostgresMatrix(t *testing.T) {
	if testing.Short() {
		t.Skip("B2 PG14-18 runtime activation is an integration test")
	}
	for _, major := range []string{"14", "15", "16", "17", "18"} {
		t.Run("pg"+major, func(t *testing.T) { runB2RuntimeActivationPostgres(t, major) })
	}
}

func runB2RuntimeActivationPostgres(t *testing.T, major string) {
	t.Helper()
	ctx := bootstrapDockerTestContext(t)
	root, err := filepath.Abs(filepath.Join("..", "..", "dbext", "postgres", "agentsql_binder"))
	require.NoError(t, err)
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			FromDockerfile: testcontainers.FromDockerfile{Context: root, Dockerfile: "Dockerfile.test", Repo: "agentsql-b2-runtime", Tag: "pg" + major, BuildArgs: map[string]*string{"PG_MAJOR": &major}, KeepImage: true},
			Env:            map[string]string{"POSTGRES_DB": "agentsql", "POSTGRES_USER": "agentsql", "POSTGRES_PASSWORD": "agentsql-password"},
			ExposedPorts:   []string{"5432/tcp"},
			WaitingFor:     wait.ForAll(wait.ForListeningPort("5432/tcp"), wait.ForLog("database system is ready to accept connections").WithOccurrence(2)).WithDeadline(90 * time.Second),
		}, Started: true,
	})
	require.NoError(t, err)
	testcontainers.CleanupContainer(t, container)
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)

	secret := []byte("0123456789abcdef0123456789abcdef")
	control, err := store.OpenWithSecret(ctx, filepath.Join(t.TempDir(), "control.db"), secret)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, control.Close()) })
	gateway := executor.NewGateway(false)
	t.Cleanup(func() { require.NoError(t, gateway.CloseAll()) })
	datasource, err := control.Datasources().Create(ctx, model.Datasource{
		ID: "b2-runtime-pg-" + major, Name: "B2 runtime PG " + major, DBType: "postgres",
		Host: host, Port: port.Int(), Database: "agentsql", Username: "agentsql",
		ConnLimit: 4, StmtTimeoutMS: 5_000, RowLimit: 100,
	}, "agentsql-password")
	require.NoError(t, err)
	for _, sqlText := range []string{
		`CREATE SCHEMA agentsql_catalog`,
		`CREATE EXTENSION agentsql_binder WITH SCHEMA agentsql_catalog`,
	} {
		statement, statementErr := gateway.AuthorizedExecute(ctx, datasource, secret, sqlText, "")
		require.NoError(t, statementErr)
		_, statementErr = statement.Execute(ctx)
		require.NoError(t, statementErr)
		require.NoError(t, statement.Close())
	}

	runtime, err := activateB2Runtime(ctx, control, gateway, secret, "runtime-pg-"+major, 5*time.Second, time.Second)
	require.NoError(t, err)
	t.Cleanup(runtime.close)
	require.Equal(t, B2StateActive, runtime.snapshot().State)
	require.Equal(t, 3, runtime.snapshot().Protocol)
	require.Len(t, runtime.snapshot().ArtifactDigest, 64)
	require.True(t, runtime.allow(datasource))
	require.True(t, runtime.route(datasource))
	runtime.close()
	_, err = control.Fence().BeginRead(context.Background(), 3, runtime.instance.InstanceID, time.Now())
	require.ErrorIs(t, err, store.ErrFenceLost, "runtime shutdown must drain protocol-3 admission")
}
