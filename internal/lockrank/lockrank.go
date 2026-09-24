// Package lockrank provides request-local, test-observable enforcement of the
// cross-database lock order. It never grants a lock itself; it proves that a
// caller cannot acquire a lower-ranked capability while retaining a higher one.
package lockrank

import (
	"context"
	"errors"
	"sync"
)

type Rank uint8

const (
	Control  Rank = 10
	Business Rank = 20
)

var ErrReverseOrder = errors.New("lock rank reverse order")

type trackerKey struct{}

type tracker struct {
	mu     sync.Mutex
	nextID uint64
	held   map[uint64]Rank
}

type Lease struct {
	tracker *tracker
	id      uint64
	once    sync.Once
}

func WithTracker(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Value(trackerKey{}) != nil {
		return ctx
	}
	return context.WithValue(ctx, trackerKey{}, &tracker{held: make(map[uint64]Rank)})
}

func Acquire(ctx context.Context, rank Rank) (*Lease, error) {
	if ctx == nil {
		return nil, ErrReverseOrder
	}
	tracked, _ := ctx.Value(trackerKey{}).(*tracker)
	if tracked == nil {
		return &Lease{}, nil
	}
	tracked.mu.Lock()
	defer tracked.mu.Unlock()
	for _, held := range tracked.held {
		if held > rank {
			return nil, ErrReverseOrder
		}
	}
	tracked.nextID++
	tracked.held[tracked.nextID] = rank
	return &Lease{tracker: tracked, id: tracked.nextID}, nil
}

func (lease *Lease) Release() {
	if lease == nil {
		return
	}
	lease.once.Do(func() {
		if lease.tracker == nil {
			return
		}
		lease.tracker.mu.Lock()
		delete(lease.tracker.held, lease.id)
		lease.tracker.mu.Unlock()
	})
}
