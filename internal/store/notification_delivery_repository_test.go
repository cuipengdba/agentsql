package store

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestPostgres18NotificationDeliveryLeaseE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("PostgreSQL 18 is required for cross-process cursor claims")
	}
	ctx := dockerTestContext(t)
	dsn := startPostgres18StoreContainer(t, ctx, "notification_delivery", "synthetic-delivery-password")
	database, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	defer database.Close()
	require.NoError(t, Migrate(ctx, database, DialectPostgres))
	repository := &NotificationDeliveryRepository{repositoryBase: repositoryBase{db: database, dialect: DialectPostgres}}
	digest := strings.Repeat("d", 64)
	require.NoError(t, repository.Ensure(ctx, "hook", digest, 3))
	now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	claim, err := repository.Claim(ctx, "hook", digest, now, 5*time.Second)
	require.NoError(t, err)
	require.NotNil(t, claim)
	blocked, err := repository.Claim(ctx, "hook", digest, now.Add(time.Second), 5*time.Second)
	require.NoError(t, err)
	require.Nil(t, blocked)
	changed, err := repository.Acknowledge(ctx, "hook", *claim, 4, now.Add(2*time.Second))
	require.NoError(t, err)
	require.True(t, changed)
	status, err := repository.Status(ctx, "hook")
	require.NoError(t, err)
	require.Equal(t, int64(4), status.LastSequence)
	// Three independent gateway connections race for the same tenant/channel.
	// The shared metadata lease must have exactly one winner.
	var databases []*sql.DB
	repositories := []*NotificationDeliveryRepository{repository}
	for range 2 {
		other, openErr := sql.Open("pgx", dsn)
		require.NoError(t, openErr)
		databases = append(databases, other)
		repositories = append(repositories, &NotificationDeliveryRepository{repositoryBase: repositoryBase{db: other, dialect: DialectPostgres}})
	}
	defer func() {
		for _, other := range databases {
			require.NoError(t, other.Close())
		}
	}()
	results := make(chan *NotificationDeliveryClaim, len(repositories))
	errors := make(chan error, len(repositories))
	start := make(chan struct{})
	var wait sync.WaitGroup
	for _, contender := range repositories {
		wait.Add(1)
		go func(candidate *NotificationDeliveryRepository) {
			defer wait.Done()
			<-start
			claimed, claimErr := candidate.Claim(ctx, "hook", digest, now.Add(3*time.Second), 5*time.Second)
			results <- claimed
			errors <- claimErr
		}(contender)
	}
	close(start)
	wait.Wait()
	close(results)
	close(errors)
	winners := 0
	for claimErr := range errors {
		require.NoError(t, claimErr)
	}
	for claimed := range results {
		if claimed != nil {
			winners++
			require.Equal(t, int64(4), claimed.LastSequence)
		}
	}
	require.Equal(t, 1, winners)
}

func TestNotificationDeliveryLeaseAndTenantIsolation(t *testing.T) {
	opened := openTestStore(t)
	base := context.Background()
	_, err := opened.RBAC().CreateTenant(base, model.Tenant{ID: "notify_a", Name: "A", Status: "active"})
	require.NoError(t, err)
	_, err = opened.RBAC().CreateTenant(base, model.Tenant{ID: "notify_b", Name: "B", Status: "active"})
	require.NoError(t, err)
	a, err := WithTenant(base, "notify_a")
	require.NoError(t, err)
	b, err := WithTenant(base, "notify_b")
	require.NoError(t, err)
	repository := opened.NotificationDelivery()
	digest := strings.Repeat("a", 64)
	require.NoError(t, repository.Ensure(a, "webhook", digest, 4))
	require.NoError(t, repository.Ensure(b, "webhook", digest, 1))
	require.NoError(t, repository.Ensure(a, "webhook", digest, 0), "an existing cursor must not rewind")
	require.ErrorContains(t, repository.Ensure(a, "webhook", strings.Repeat("b", 64), 0), "configuration changed")

	now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	first, err := repository.Claim(a, "webhook", digest, now, 2*time.Second)
	require.NoError(t, err)
	require.NotNil(t, first)
	require.Equal(t, int64(4), first.LastSequence)
	blocked, err := repository.Claim(a, "webhook", digest, now.Add(time.Second), 2*time.Second)
	require.NoError(t, err)
	require.Nil(t, blocked)
	other, err := repository.Claim(b, "webhook", digest, now, 2*time.Second)
	require.NoError(t, err)
	require.NotNil(t, other)
	require.Equal(t, int64(1), other.LastSequence)

	second, err := repository.Claim(a, "webhook", digest, now.Add(3*time.Second), 2*time.Second)
	require.NoError(t, err)
	require.NotNil(t, second)
	require.NotEqual(t, first.Token, second.Token)
	accepted, err := repository.Acknowledge(a, "webhook", *first, 5, now.Add(3*time.Second))
	require.NoError(t, err)
	require.False(t, accepted, "expired owner must not acknowledge after reassignment")
	accepted, err = repository.Acknowledge(a, "webhook", *second, 5, now.Add(4*time.Second))
	require.NoError(t, err)
	require.True(t, accepted)
	status, err := repository.Status(a, "webhook")
	require.NoError(t, err)
	require.Equal(t, int64(5), status.LastSequence)
	status, err = repository.Status(b, "webhook")
	require.NoError(t, err)
	require.Equal(t, int64(1), status.LastSequence)
}

func TestNotificationDeliveryRetryDeadLetterAndReplay(t *testing.T) {
	opened := openTestStore(t)
	ctx := context.Background()
	repository := opened.NotificationDelivery()
	digest := strings.Repeat("c", 64)
	require.NoError(t, repository.Ensure(ctx, "hook", digest, 0))
	now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	claim, err := repository.Claim(ctx, "hook", digest, now, 5*time.Second)
	require.NoError(t, err)
	require.NotNil(t, claim)
	_, err = repository.Fail(ctx, "hook", *claim, "https://secret.example", now.Add(time.Second), now.Add(10*time.Second), 2)
	require.ErrorContains(t, err, "invalid failure state")
	changed, err := repository.Fail(ctx, "hook", *claim, "timeout", now.Add(time.Second), now.Add(10*time.Second), 2)
	require.NoError(t, err)
	require.True(t, changed)
	blocked, err := repository.Claim(ctx, "hook", digest, now.Add(5*time.Second), 5*time.Second)
	require.NoError(t, err)
	require.Nil(t, blocked)
	claim, err = repository.Claim(ctx, "hook", digest, now.Add(10*time.Second), 5*time.Second)
	require.NoError(t, err)
	require.NotNil(t, claim)
	require.Equal(t, 1, claim.Failures)
	changed, err = repository.Fail(ctx, "hook", *claim, "http_status", now.Add(11*time.Second), now.Add(20*time.Second), 2)
	require.NoError(t, err)
	require.True(t, changed)
	status, err := repository.Status(ctx, "hook")
	require.NoError(t, err)
	require.True(t, status.DeadLetter)
	require.Equal(t, 2, status.Failures)
	require.Equal(t, int64(0), status.LastSequence)
	blocked, err = repository.Claim(ctx, "hook", digest, now.Add(time.Hour), 5*time.Second)
	require.NoError(t, err)
	require.Nil(t, blocked)
	changed, err = repository.ReplayDeadLetter(ctx, "hook")
	require.NoError(t, err)
	require.True(t, changed)
	claim, err = repository.Claim(ctx, "hook", digest, now.Add(time.Hour), 5*time.Second)
	require.NoError(t, err)
	require.NotNil(t, claim)
	changed, err = repository.Release(ctx, "hook", *claim, now.Add(time.Hour+time.Second))
	require.NoError(t, err)
	require.True(t, changed)
	claim, err = repository.Claim(ctx, "hook", digest, now.Add(time.Hour+2*time.Second), 5*time.Second)
	require.NoError(t, err)
	require.NotNil(t, claim)
	changed, err = repository.Acknowledge(ctx, "hook", *claim, 1, now.Add(time.Hour+3*time.Second))
	require.NoError(t, err)
	require.True(t, changed)
	status, err = repository.Status(ctx, "hook")
	require.NoError(t, err)
	require.Equal(t, int64(1), status.LastSequence)
	require.False(t, status.DeadLetter)
	require.Zero(t, status.Failures)
}
