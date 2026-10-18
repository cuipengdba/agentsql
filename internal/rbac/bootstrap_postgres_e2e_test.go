package rbac

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestConcurrentPostgresBootstrapE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("requires a real PostgreSQL container")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	container, err := postgrescontainer.Run(ctx, "postgres:18",
		postgrescontainer.WithDatabase("agentsql_bootstrap"),
		postgrescontainer.WithUsername("agentsql_bootstrap"),
		postgrescontainer.WithPassword("bootstrap-e2e-password"),
		postgrescontainer.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	testcontainers.CleanupContainer(t, container)
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)
	dsn := fmt.Sprintf("postgres://agentsql_bootstrap:bootstrap-e2e-password@%s:%s/agentsql_bootstrap?sslmode=disable", host, port.Port())
	options := store.MetadataOptions{Driver: store.DialectPostgres, PostgresDSN: dsn, MaxOpenConns: 4, MaxIdleConns: 2, AutoMigrate: true}
	const secret = "0123456789abcdef0123456789abcdef"
	seed, err := store.OpenMetadata(ctx, options, []byte(secret))
	require.NoError(t, err)
	require.NoError(t, seed.Close())

	const replicas = 8
	services := make([]*Service, 0, replicas)
	for range replicas {
		options.AutoMigrate = false
		opened, openErr := store.OpenMetadata(ctx, options, []byte(secret))
		require.NoError(t, openErr)
		t.Cleanup(func() { require.NoError(t, opened.Close()) })
		services = append(services, NewService(opened.RBAC()))
	}

	start := make(chan struct{})
	results := make(chan error, replicas)
	var workers sync.WaitGroup
	for _, service := range services {
		workers.Add(1)
		go func(service *Service) {
			defer workers.Done()
			<-start
			results <- service.Bootstrap(ctx, "admin", "concurrent-bootstrap-password")
		}(service)
	}
	close(start)
	workers.Wait()
	close(results)
	for bootstrapErr := range results {
		require.NoError(t, bootstrapErr)
	}
	principal, err := services[0].Principal(ctx, DefaultTenantID, BootstrapUserID)
	require.NoError(t, err)
	require.True(t, principal.Has(PermissionRoleManage))
	require.True(t, principal.Has(PermissionAuditView))
}
