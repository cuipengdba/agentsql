package mcpserver

import (
	"context"
	"fmt"
	"iter"
	"time"

	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type streamEventRepository interface {
	OpenStream(context.Context, string, string) error
	AppendStreamEvent(context.Context, string, string, []byte, int, time.Duration) error
	StreamEventsAfter(context.Context, string, string, int) (int, [][]byte, error)
	DeleteSessionStreams(context.Context, string) error
	PurgeExpiredStreamEvents(context.Context, int, time.Duration) error
}

type persistentEventStore struct {
	repository streamEventRepository
	maxBytes   int
	ttl        time.Duration
}

func newPersistentEventStore(repository *store.MCPStreamEventRepository, maxBytes int, ttl time.Duration) *persistentEventStore {
	return &persistentEventStore{repository: repository, maxBytes: maxBytes, ttl: ttl}
}

func (eventStore *persistentEventStore) Open(ctx context.Context, sessionID, streamID string) error {
	if err := eventStore.repository.PurgeExpiredStreamEvents(ctx, eventStore.maxBytes, eventStore.ttl); err != nil {
		return fmt.Errorf("purge persistent MCP events while opening stream: %w", err)
	}
	if err := eventStore.repository.OpenStream(ctx, sessionID, streamID); err != nil {
		return fmt.Errorf("open persistent MCP stream: %w", err)
	}
	return nil
}

func (eventStore *persistentEventStore) Append(ctx context.Context, sessionID, streamID string, data []byte) error {
	if err := eventStore.repository.AppendStreamEvent(ctx, sessionID, streamID, data, eventStore.maxBytes, eventStore.ttl); err != nil {
		return fmt.Errorf("append persistent MCP event: %w", err)
	}
	return nil
}

func (eventStore *persistentEventStore) After(ctx context.Context, sessionID, streamID string, index int) iter.Seq2[[]byte, error] {
	return func(yield func([]byte, error) bool) {
		_, events, err := eventStore.repository.StreamEventsAfter(ctx, sessionID, streamID, index)
		if err != nil {
			yield(nil, fmt.Errorf("replay persistent MCP events: %w", err))
			return
		}
		for _, event := range events {
			if !yield(event, nil) {
				return
			}
		}
	}
}

func (eventStore *persistentEventStore) SessionClosed(ctx context.Context, sessionID string) error {
	if err := eventStore.repository.DeleteSessionStreams(ctx, sessionID); err != nil {
		return fmt.Errorf("delete persistent MCP session events: %w", err)
	}
	return nil
}

var _ mcp.EventStore = (*persistentEventStore)(nil)
