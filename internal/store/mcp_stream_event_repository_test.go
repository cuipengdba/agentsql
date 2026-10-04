package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMCPStreamEventRepositorySQLiteReplayPurgeAndDelete(t *testing.T) {
	opened := openTestStore(t)
	repository := opened.MCPStreamEvents()
	ctx := context.Background()
	const maxBytes = 6
	ttl := time.Hour

	require.NoError(t, repository.OpenStream(ctx, "session", "stream"))
	for _, data := range [][]byte{[]byte("aaa"), []byte("bbb"), []byte("ccc")} {
		require.NoError(t, repository.AppendStreamEvent(ctx, "session", "stream", data, maxBytes, ttl))
	}
	_, events, err := repository.StreamEventsAfter(ctx, "session", "stream", -1)
	require.ErrorIs(t, err, ErrStreamEventsPurged)
	require.Empty(t, events, "purged replay must never return a partial suffix")
	first, events, err := repository.StreamEventsAfter(ctx, "session", "stream", 0)
	require.NoError(t, err)
	require.Equal(t, 1, first)
	require.Equal(t, [][]byte{[]byte("bbb"), []byte("ccc")}, events)

	require.NoError(t, repository.DeleteSessionStreams(ctx, "session"))
	_, _, err = repository.StreamEventsAfter(ctx, "session", "stream", 0)
	require.ErrorIs(t, err, ErrMCPStreamNotFound)
}

func TestMCPStreamEventRepositorySQLiteConcurrentIndexes(t *testing.T) {
	opened := openTestStore(t)
	repository := opened.MCPStreamEvents()
	ctx := context.Background()
	require.NoError(t, repository.OpenStream(ctx, "concurrent-session", "stream"))

	const count = 32
	errorsFound := make(chan error, count)
	var wait sync.WaitGroup
	for index := range count {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			errorsFound <- repository.AppendStreamEvent(
				ctx, "concurrent-session", "stream", []byte(fmt.Sprintf("event-%02d", index)), 1<<20, time.Hour,
			)
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		require.NoError(t, err)
	}
	first, events, err := repository.StreamEventsAfter(ctx, "concurrent-session", "stream", -1)
	require.NoError(t, err)
	require.Zero(t, first)
	require.Len(t, events, count)

	var rows, distinctIndexes, minimum, maximum int
	require.NoError(t, opened.metaDB.QueryRowContext(ctx, `
SELECT COUNT(*),COUNT(DISTINCT event_index),MIN(event_index),MAX(event_index)
FROM mcp_stream_events WHERE session_id=? AND stream_id=?`, "concurrent-session", "stream").Scan(
		&rows, &distinctIndexes, &minimum, &maximum,
	))
	require.Equal(t, count, rows)
	require.Equal(t, count, distinctIndexes)
	require.Zero(t, minimum)
	require.Equal(t, count-1, maximum)
}

func TestMCPStreamEventRepositorySQLiteTTLAndCapacityPurge(t *testing.T) {
	opened := openTestStore(t)
	repository := opened.MCPStreamEvents()
	ctx := context.Background()
	require.NoError(t, repository.OpenStream(ctx, "ttl-session", "stream"))
	require.NoError(t, repository.AppendStreamEvent(ctx, "ttl-session", "stream", []byte("old"), 1<<20, time.Hour))
	time.Sleep(5 * time.Millisecond)
	require.NoError(t, repository.PurgeExpiredStreamEvents(ctx, 1<<20, time.Millisecond))

	_, events, err := repository.StreamEventsAfter(ctx, "ttl-session", "stream", -1)
	require.ErrorIs(t, err, ErrMCPStreamNotFound)
	require.Empty(t, events)

	// Capacity purge retains the active cursor, so a fully purged stream does
	// not recycle event index 0.
	require.NoError(t, repository.OpenStream(ctx, "capacity-session", "stream"))
	require.NoError(t, repository.AppendStreamEvent(ctx, "capacity-session", "stream", []byte("old"), 1, time.Hour))
	require.NoError(t, repository.AppendStreamEvent(ctx, "capacity-session", "stream", []byte("new"), 1<<20, time.Hour))
	first, events, err := repository.StreamEventsAfter(ctx, "capacity-session", "stream", 0)
	require.NoError(t, err)
	require.Equal(t, 1, first)
	require.Equal(t, [][]byte{[]byte("new")}, events)
}

func TestMCPStreamEventRepositoryPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("PostgreSQL MCP stream repository is an integration test")
	}
	ctx := dockerTestContext(t)
	dsn := startPostgres18StoreContainer(t, ctx, "agentsql_mcp_stream_events", "mcp-stream-password")
	database, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	require.NoError(t, MigrateMetadata(ctx, database, DialectPostgres, false))
	repository := &MCPStreamEventRepository{repositoryBase: repositoryBase{db: database, dialect: DialectPostgres}}
	require.NoError(t, repository.OpenStream(ctx, "pg-session", "pg-stream"))
	require.NoError(t, repository.AppendStreamEvent(ctx, "pg-session", "pg-stream", []byte("zero"), 1<<20, time.Hour))
	require.NoError(t, repository.AppendStreamEvent(ctx, "pg-session", "pg-stream", []byte("one"), 1<<20, time.Hour))
	first, events, err := repository.StreamEventsAfter(ctx, "pg-session", "pg-stream", 0)
	require.NoError(t, err)
	require.Equal(t, 1, first)
	require.Equal(t, [][]byte{[]byte("one")}, events)
	require.NoError(t, repository.PurgeExpiredStreamEvents(ctx, 1, time.Hour))
	_, _, err = repository.StreamEventsAfter(ctx, "pg-session", "pg-stream", 0)
	require.True(t, errors.Is(err, ErrStreamEventsPurged))
}

// TestMCPStreamEventRepositoryPostgresExternal lets the release verification
// exercise both migration layouts against short-lived PostgreSQL databases
// without requiring a Docker socket inside the Go build container.
func TestMCPStreamEventRepositoryPostgresExternal(t *testing.T) {
	dsn := os.Getenv("AGENTSQL_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("AGENTSQL_TEST_POSTGRES_DSN is not set")
	}
	separate := os.Getenv("AGENTSQL_TEST_POSTGRES_SEPARATE") == "1"
	ctx := context.Background()
	database, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	require.NoError(t, database.PingContext(ctx))

	require.NoError(t, MigrateMetadata(ctx, database, DialectPostgres, separate))
	require.NoError(t, MigrateMetadata(ctx, database, DialectPostgres, separate), "full up chain must be idempotent")
	wantLatest := 13
	if separate {
		wantLatest = 12
	}
	current, latest, err := MetadataMigrationVersions(ctx, database, DialectPostgres, separate)
	require.NoError(t, err)
	require.Equal(t, wantLatest, current)
	require.Equal(t, wantLatest, latest)
	t.Logf("PostgreSQL migration up and repeated up passed (separate=%t current=%d latest=%d)", separate, current, latest)

	for _, object := range []string{
		"mcp_stream_event_cursors",
		"mcp_stream_events",
		"idx_mcp_stream_event_cursors_updated_at",
		"idx_mcp_stream_events_session",
		"idx_mcp_stream_events_created_at",
	} {
		var exists bool
		require.NoError(t, database.QueryRowContext(ctx, `SELECT to_regclass('public.' || $1) IS NOT NULL`, object).Scan(&exists))
		require.True(t, exists, object)
	}

	repository := &MCPStreamEventRepository{repositoryBase: repositoryBase{db: database, dialect: DialectPostgres}}
	require.NoError(t, repository.OpenStream(ctx, "external-session", "stream"))
	require.NoError(t, repository.AppendStreamEvent(ctx, "external-session", "stream", []byte("zero"), 1<<20, time.Hour))
	require.NoError(t, repository.AppendStreamEvent(ctx, "external-session", "stream", []byte("one"), 1<<20, time.Hour))
	first, events, err := repository.StreamEventsAfter(ctx, "external-session", "stream", 0)
	require.NoError(t, err)
	require.Equal(t, 1, first)
	require.Equal(t, [][]byte{[]byte("one")}, events)
	require.NoError(t, repository.PurgeExpiredStreamEvents(ctx, 1, time.Hour))
	_, events, err = repository.StreamEventsAfter(ctx, "external-session", "stream", 0)
	require.ErrorIs(t, err, ErrStreamEventsPurged)
	require.Empty(t, events)
	t.Log("PostgreSQL append/after/capacity-purge behavior passed")

	require.NoError(t, RollbackMetadataMigration(ctx, database, DialectPostgres, separate, wantLatest))
	require.NoError(t, RollbackMetadataMigration(ctx, database, DialectPostgres, separate, wantLatest), "latest down must be idempotent")
	for _, table := range []string{"mcp_stream_event_cursors", "mcp_stream_events"} {
		var exists bool
		require.NoError(t, database.QueryRowContext(ctx, `SELECT to_regclass('public.' || $1) IS NOT NULL`, table).Scan(&exists))
		require.False(t, exists, table)
	}
	current, latest, err = MetadataMigrationVersions(ctx, database, DialectPostgres, separate)
	require.NoError(t, err)
	require.Equal(t, wantLatest-1, current)
	require.Equal(t, wantLatest, latest)
	t.Logf("PostgreSQL latest down and repeated down passed (tables absent, current=%d)", current)
	require.NoError(t, MigrateMetadata(ctx, database, DialectPostgres, separate))
	t.Log("PostgreSQL latest migration reapplied successfully")
}
