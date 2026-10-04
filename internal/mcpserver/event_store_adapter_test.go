package mcpserver

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/stretchr/testify/require"
)

type fakeStreamEventRepository struct {
	events       [][]byte
	afterErr     error
	opened       bool
	deleted      bool
	purgeCalls   int
	appendLimits struct {
		maxBytes int
		ttl      time.Duration
	}
}

func (repository *fakeStreamEventRepository) OpenStream(context.Context, string, string) error {
	repository.opened = true
	return nil
}

func (repository *fakeStreamEventRepository) AppendStreamEvent(_ context.Context, _, _ string, data []byte, maxBytes int, ttl time.Duration) error {
	repository.events = append(repository.events, append([]byte(nil), data...))
	repository.appendLimits.maxBytes = maxBytes
	repository.appendLimits.ttl = ttl
	return nil
}

func (repository *fakeStreamEventRepository) StreamEventsAfter(context.Context, string, string, int) (int, [][]byte, error) {
	if repository.afterErr != nil {
		return 0, nil, repository.afterErr
	}
	return 0, repository.events, nil
}

func (repository *fakeStreamEventRepository) DeleteSessionStreams(context.Context, string) error {
	repository.deleted = true
	return nil
}

func (repository *fakeStreamEventRepository) PurgeExpiredStreamEvents(context.Context, int, time.Duration) error {
	repository.purgeCalls++
	return nil
}

func TestPersistentEventStoreAdapterLifecycleAndReplay(t *testing.T) {
	repository := &fakeStreamEventRepository{}
	eventStore := &persistentEventStore{repository: repository, maxBytes: 123, ttl: 45 * time.Second}
	ctx := context.Background()
	require.NoError(t, eventStore.Open(ctx, "session", "stream"))
	require.True(t, repository.opened)
	require.Equal(t, 1, repository.purgeCalls)
	require.NoError(t, eventStore.Append(ctx, "session", "stream", []byte("zero")))
	require.NoError(t, eventStore.Append(ctx, "session", "stream", []byte("one")))
	require.Equal(t, 123, repository.appendLimits.maxBytes)
	require.Equal(t, 45*time.Second, repository.appendLimits.ttl)

	var replayed [][]byte
	for data, err := range eventStore.After(ctx, "session", "stream", -1) {
		require.NoError(t, err)
		replayed = append(replayed, data)
	}
	require.Equal(t, [][]byte{[]byte("zero"), []byte("one")}, replayed)
	require.NoError(t, eventStore.SessionClosed(ctx, "session"))
	require.True(t, repository.deleted)
}

func TestPersistentEventStoreAdapterPurgeErrorIsImmediate(t *testing.T) {
	repository := &fakeStreamEventRepository{
		events:   [][]byte{[]byte("must-not-be-returned")},
		afterErr: store.ErrStreamEventsPurged,
	}
	eventStore := &persistentEventStore{repository: repository, maxBytes: 1, ttl: time.Second}
	yields := 0
	for data, err := range eventStore.After(context.Background(), "session", "stream", -1) {
		yields++
		require.Nil(t, data)
		require.ErrorIs(t, err, store.ErrStreamEventsPurged)
	}
	require.Equal(t, 1, yields)

	repository.afterErr = errors.New("database unavailable")
	for _, err := range eventStore.After(context.Background(), "session", "stream", -1) {
		require.ErrorContains(t, err, "database unavailable")
	}
}
