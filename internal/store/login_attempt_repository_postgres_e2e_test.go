package store

import (
	"context"
	"crypto/sha256"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSharedLoginAttemptBudgetAcrossPostgresConnections(t *testing.T) {
	if testing.Short() {
		t.Skip("PostgreSQL integration test")
	}
	ctx := dockerTestContext(t)
	dsn := startPostgres18StoreContainer(t, ctx, "agentsql_login_budget", "login-budget-password")
	open := func() *Store {
		opened, err := OpenMetadata(ctx, MetadataOptions{Driver: DialectPostgres, PostgresDSN: dsn,
			MaxOpenConns: 8, MaxIdleConns: 4, AutoMigrate: true}, []byte(testSecret))
		require.NoError(t, err)
		return opened
	}
	first, second := open(), open()
	t.Cleanup(func() {
		if first != nil {
			require.NoError(t, first.Close())
		}
		require.NoError(t, second.Close())
	})
	key := sha256.Sum256([]byte("tenant/account HMAC fixture"))
	var group sync.WaitGroup
	results := make(chan bool, 20)
	errors := make(chan error, 20)
	for index := range 20 {
		group.Add(1)
		go func() {
			defer group.Done()
			selected := first
			if index%2 == 1 {
				selected = second
			}
			allowed, err := selected.SharedLoginAttempts().Allow(ctx, key)
			results <- allowed
			errors <- err
		}()
	}
	group.Wait()
	close(results)
	close(errors)
	accepted := 0
	for err := range errors {
		require.NoError(t, err)
	}
	for allowed := range results {
		if allowed {
			accepted++
		}
	}
	require.Equal(t, 10, accepted)

	restarted := open()
	require.NoError(t, second.Close())
	second = restarted
	allowed, err := second.SharedLoginAttempts().Allow(ctx, key)
	require.NoError(t, err)
	require.False(t, allowed, "restart cannot reset the shared budget")

	otherKey := sha256.Sum256([]byte("different tenant/account"))
	allowed, err = second.SharedLoginAttempts().Allow(ctx, otherKey)
	require.NoError(t, err)
	require.True(t, allowed)

	_, err = first.metaDB.ExecContext(ctx, "UPDATE login_attempt_budgets SET started_at=now()-interval '61 seconds' WHERE key_hash=$1", key[:])
	require.NoError(t, err)
	allowed, err = second.SharedLoginAttempts().Allow(ctx, key)
	require.NoError(t, err)
	require.True(t, allowed, "expired 60-second window can start again")
	_, err = first.metaDB.ExecContext(ctx, `
INSERT INTO login_attempt_budgets(key_hash,started_at,attempts)
SELECT decode(md5(value::text) || md5((value+8192)::text), 'hex'), clock_timestamp(), 1
FROM generate_series(1,8190) AS generated(value)`)
	require.NoError(t, err)
	newKey := sha256.Sum256([]byte("account after bucket capacity"))
	allowed, err = second.SharedLoginAttempts().Allow(ctx, newKey)
	require.NoError(t, err)
	require.False(t, allowed, "unknown accounts cannot grow the table beyond capacity")
	allowed, err = second.SharedLoginAttempts().Allow(ctx, otherKey)
	require.NoError(t, err)
	require.True(t, allowed, "existing account budget remains available at capacity")

	closedRepository := first.SharedLoginAttempts()
	require.NoError(t, first.Close())
	first = nil
	ctxTimeout, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = closedRepository.Allow(ctxTimeout, key)
	require.Error(t, err)
}
