// Package eventbus provides the in-process best-effort audit event stream.
package eventbus

import (
	"fmt"
	"sync"

	"github.com/cuipengdba/agentsql/internal/model"
)

const (
	defaultHistorySize      = 200
	defaultSubscriberBuffer = 64
)

// Options controls retained history and each subscriber's live-event backlog.
type Options struct {
	HistorySize      int
	SubscriberBuffer int
}

// Event is a successfully persisted audit record.
type Event struct {
	Audit model.AuditLog
}

// Hub retains recent events and fans new events out without blocking publishers.
type Hub struct {
	mu               sync.Mutex
	subscribers      map[uint64]chan Event
	nextSubscriberID uint64
	ring             []Event
	next             int
	size             int
	subscriberBuffer int
	closed           bool
}

// New creates a Hub. Zero-valued options use the production defaults.
func New(options Options) (*Hub, error) {
	if options.HistorySize < 0 {
		return nil, fmt.Errorf("create event hub: history size must not be negative")
	}
	if options.SubscriberBuffer < 0 {
		return nil, fmt.Errorf("create event hub: subscriber buffer must not be negative")
	}
	if options.HistorySize == 0 {
		options.HistorySize = defaultHistorySize
	}
	if options.SubscriberBuffer == 0 {
		options.SubscriberBuffer = defaultSubscriberBuffer
	}
	return &Hub{
		subscribers:      make(map[uint64]chan Event),
		ring:             make([]Event, options.HistorySize),
		subscriberBuffer: options.SubscriberBuffer,
	}, nil
}

// Subscribe returns retained history followed by live events and an idempotent cancel function.
func (hub *Hub) Subscribe() (<-chan Event, func()) {
	if hub == nil {
		closed := make(chan Event)
		close(closed)
		return closed, func() {}
	}
	hub.mu.Lock()
	if hub.closed {
		hub.mu.Unlock()
		closed := make(chan Event)
		close(closed)
		return closed, func() {}
	}
	history := hub.historyLocked()
	channel := make(chan Event, len(history)+hub.subscriberBuffer)
	for _, event := range history {
		channel <- event
	}
	hub.nextSubscriberID++
	id := hub.nextSubscriberID
	hub.subscribers[id] = channel
	hub.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			hub.mu.Lock()
			if registered, exists := hub.subscribers[id]; exists {
				delete(hub.subscribers, id)
				close(registered)
			}
			hub.mu.Unlock()
		})
	}
	return channel, cancel
}

// SubscribeLive returns only events published after the subscription is
// registered. Like Subscribe, delivery is best effort: a subscriber whose
// buffer fills is removed without blocking publishers. The returned cancel
// function is idempotent.
func (hub *Hub) SubscribeLive() (<-chan Event, func()) {
	if hub == nil {
		closed := make(chan Event)
		close(closed)
		return closed, func() {}
	}
	hub.mu.Lock()
	if hub.closed {
		hub.mu.Unlock()
		closed := make(chan Event)
		close(closed)
		return closed, func() {}
	}
	channel := make(chan Event, hub.subscriberBuffer)
	hub.nextSubscriberID++
	id := hub.nextSubscriberID
	hub.subscribers[id] = channel
	hub.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			hub.mu.Lock()
			if registered, exists := hub.subscribers[id]; exists {
				delete(hub.subscribers, id)
				close(registered)
			}
			hub.mu.Unlock()
		})
	}
	return channel, cancel
}

// Publish retains and broadcasts event without waiting for subscriber reads.
func (hub *Hub) Publish(event Event) {
	if hub == nil {
		return
	}
	event = cloneEvent(event)
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.closed {
		return
	}
	if len(hub.ring) > 0 {
		hub.ring[hub.next] = event
		hub.next = (hub.next + 1) % len(hub.ring)
		if hub.size < len(hub.ring) {
			hub.size++
		}
	}
	for id, subscriber := range hub.subscribers {
		select {
		case subscriber <- cloneEvent(event):
		default:
			delete(hub.subscribers, id)
			close(subscriber)
		}
	}
}

// SubscriberCount returns the number of currently registered subscribers.
func (hub *Hub) SubscriberCount() int {
	if hub == nil {
		return 0
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	return len(hub.subscribers)
}

// Close closes every subscriber and makes future operations inert.
func (hub *Hub) Close() {
	if hub == nil {
		return
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.closed {
		return
	}
	hub.closed = true
	for id, subscriber := range hub.subscribers {
		delete(hub.subscribers, id)
		close(subscriber)
	}
}

func (hub *Hub) historyLocked() []Event {
	history := make([]Event, 0, hub.size)
	if hub.size == 0 {
		return history
	}
	start := hub.next - hub.size
	if start < 0 {
		start += len(hub.ring)
	}
	for offset := 0; offset < hub.size; offset++ {
		history = append(history, cloneEvent(hub.ring[(start+offset)%len(hub.ring)]))
	}
	return history
}

func cloneEvent(event Event) Event {
	cloned := event
	cloned.Audit.AgentID = clonePointer(event.Audit.AgentID)
	cloned.Audit.DatasourceID = clonePointer(event.Audit.DatasourceID)
	cloned.Audit.SessionID = clonePointer(event.Audit.SessionID)
	cloned.Audit.ConversationID = clonePointer(event.Audit.ConversationID)
	cloned.Audit.MCPTool = clonePointer(event.Audit.MCPTool)
	cloned.Audit.DBType = clonePointer(event.Audit.DBType)
	cloned.Audit.SQLRaw = clonePointer(event.Audit.SQLRaw)
	cloned.Audit.SQLNorm = clonePointer(event.Audit.SQLNorm)
	cloned.Audit.StmtType = clonePointer(event.Audit.StmtType)
	cloned.Audit.Objects = clonePointer(event.Audit.Objects)
	cloned.Audit.RuleHits = clonePointer(event.Audit.RuleHits)
	cloned.Audit.RiskLevel = clonePointer(event.Audit.RiskLevel)
	cloned.Audit.EstRows = clonePointer(event.Audit.EstRows)
	cloned.Audit.RowsReturned = clonePointer(event.Audit.RowsReturned)
	cloned.Audit.LatencyMS = clonePointer(event.Audit.LatencyMS)
	cloned.Audit.ClientIP = clonePointer(event.Audit.ClientIP)
	cloned.Audit.ModelName = clonePointer(event.Audit.ModelName)
	cloned.Audit.ErrorMsg = clonePointer(event.Audit.ErrorMsg)
	return cloned
}

func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
