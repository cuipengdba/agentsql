package adminapi

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/bootstrap"
	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestTwoPostgresGatewaysSharePasswordLoginBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("PostgreSQL integration test")
	}
	ctx := adminDockerTestContext(t)
	_, dsn := startAuditBarrierPostgres(t, ctx, "shared_login", "agentsql", "shared-login-db-password")
	cfg := adminTestConfig("")
	cfg.Store = config.StoreConfig{AutoMigrate: true, Metadata: &config.MetadataStoreConfig{
		Driver: string(store.DialectPostgres), DSN: dsn, MaxOpenConns: 8,
		MaxIdleConns: 4, ConnMaxLifetime: config.ConfigDuration(time.Minute),
	}}
	newGateway := func() (*bootstrap.Runtime, http.Handler) {
		runtime, err := bootstrap.Assemble(ctx, cfg, []byte(adminTestSecret))
		require.NoError(t, err)
		handler, err := NewHandler(Deps{Runtime: runtime, Config: cfg,
			AdminUsername: "admin", AdminPassword: "shared-login-admin-password",
			TokenKey: DeriveTokenKey([]byte(adminTestSecret)),
		}, zerolog.Nop())
		require.NoError(t, err)
		return runtime, handler
	}
	firstRuntime, first := newGateway()
	secondRuntime, second := newGateway()
	t.Cleanup(func() { require.NoError(t, firstRuntime.Close()); require.NoError(t, secondRuntime.Close()) })
	login := func(handler http.Handler, username, password string) int {
		body := []byte(`{"username":"` + username + `","password":"` + password + `"}`)
		request := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Code
	}
	for attempt := range loginMaxAttempts {
		gateway := first
		if attempt%2 == 1 {
			gateway = second
		}
		require.Equal(t, http.StatusUnauthorized, login(gateway, "AdMiN", "wrong"))
	}
	require.Equal(t, http.StatusTooManyRequests, login(first, "admin", "shared-login-admin-password"))
	require.Equal(t, http.StatusTooManyRequests, login(second, "ADMIN", "shared-login-admin-password"))
	require.Equal(t, http.StatusUnauthorized, login(second, "unknown", "wrong"))

	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	var rowCount int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM login_attempt_budgets").Scan(&rowCount))
	require.Equal(t, 2, rowCount)

	// A metadata failure must never use a local fallback and accidentally
	// admit a password guess on the surviving HTTP handler.
	require.NoError(t, secondRuntime.Store.Close())
	require.Equal(t, http.StatusServiceUnavailable, login(second, "another-account", "wrong"))
}
