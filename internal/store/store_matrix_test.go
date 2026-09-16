package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
)

type storeVariant struct {
	name    string
	dialect Dialect
	open    func(t *testing.T) *Store
}

func forEachStore(t *testing.T, test func(t *testing.T, opened *Store)) {
	t.Helper()
	variants := []storeVariant{
		{
			name:    "sqlite",
			dialect: DialectSQLite,
			open: func(t *testing.T) *Store {
				t.Helper()
				opened, err := OpenWithSecret(
					context.Background(),
					filepath.Join(t.TempDir(), "agentsql.db"),
					[]byte(testSecret),
				)
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, opened.Close()) })
				return opened
			},
		},
		{
			name:    "postgres18",
			dialect: DialectPostgres,
			open:    openPostgres18TestStore,
		},
	}
	for _, variant := range variants {
		variant := variant
		t.Run(variant.name, func(t *testing.T) {
			if testing.Short() && variant.dialect == DialectPostgres {
				t.Skip("postgres:18 store matrix is an integration test")
			}
			test(t, variant.open(t))
		})
	}
}

func openPostgres18TestStore(t *testing.T) *Store {
	t.Helper()
	ctx := dockerTestContext(t)
	const (
		databaseName = "agentsql"
		username     = "agentsql"
		password     = "agentsql-password"
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
	dsn := fmt.Sprintf(
		"postgres://%s:%s@%s:%s/%s?sslmode=disable",
		username, password, host, port.Port(), databaseName,
	)
	opened, err := OpenMetadata(ctx, MetadataOptions{
		Driver:          DialectPostgres,
		PostgresDSN:     dsn,
		MaxOpenConns:    8,
		MaxIdleConns:    4,
		ConnMaxLifetime: time.Minute,
		AutoMigrate:     true,
	}, []byte(testSecret))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, opened.Close()) })
	return opened
}

func execStoreSQL(t *testing.T, opened *Store, query string, args ...any) {
	t.Helper()
	_, err := opened.metaDB.ExecContext(
		context.Background(),
		repositoryBase{dialect: opened.metaDriver}.bind(query),
		args...,
	)
	require.NoError(t, err)
}
