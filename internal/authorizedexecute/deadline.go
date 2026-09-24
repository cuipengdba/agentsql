package authorizedexecute

import (
	"context"
	"time"
)

const (
	DefaultRequestWall   = 10 * time.Second
	DefaultStatementWall = 5 * time.Second
	DefaultLockWall      = 2 * time.Second
)

func boundedDuration(configured, maximum time.Duration) time.Duration {
	if configured <= 0 || configured > maximum {
		return maximum
	}
	return configured
}

// WithRequestDeadline creates the sole request parent deadline.
func WithRequestDeadline(parent context.Context, configured time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, boundedDuration(configured, DefaultRequestWall))
}

// WithStatementDeadline derives execution from the request/lock parent.
func WithStatementDeadline(parent context.Context, datasource time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, boundedDuration(datasource, DefaultStatementWall))
}

// WithLockDeadline creates one shared wall budget for lock, execute, read,
// mask, encode, post-check, audit and transaction cleanup.
func WithLockDeadline(parent context.Context, configuredRequest time.Duration) (context.Context, context.CancelFunc) {
	requestWall := boundedDuration(configuredRequest, DefaultRequestWall)
	lockWall := requestWall / 2
	if lockWall < 500*time.Millisecond {
		lockWall = 500 * time.Millisecond
	}
	if lockWall > DefaultLockWall {
		lockWall = DefaultLockWall
	}
	return context.WithTimeout(parent, lockWall)
}
