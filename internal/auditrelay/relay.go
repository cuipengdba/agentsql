// Package auditrelay delivers metadata-side management audit outbox events.
package auditrelay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
)

const (
	DefaultBatchSize = 100
	DefaultLease     = 2 * time.Minute
)

type Outbox interface {
	ClaimBatch(context.Context, string, int, time.Duration) ([]model.ManagementAuditOutbox, error)
	MarkDelivered(context.Context, string) error
	MarkFailed(context.Context, string, string, time.Duration) error
	PendingStats(context.Context) (int, time.Duration, error)
}

type Audit interface {
	AppendBatch(context.Context, []model.AuditLog) ([]model.AuditLog, error)
	FindByEventUUIDs(context.Context, []string) ([]model.AuditLog, error)
}

type Observer interface {
	SetOutboxPending(int, time.Duration)
}

type Relay struct {
	outbox   Outbox
	audit    Audit
	workerID string
	observer Observer
	commitMu sync.Mutex
}

func New(outbox Outbox, audit Audit, workerID string, observer Observer) (*Relay, error) {
	if outbox == nil || audit == nil || workerID == "" {
		return nil, errors.New("management audit relay requires outbox, audit, and worker ID")
	}
	return &Relay{outbox: outbox, audit: audit, workerID: workerID, observer: observer}, nil
}

// Backoff returns the bounded retry delay after an event's current attempt count.
func Backoff(attempts int) time.Duration {
	delays := [...]time.Duration{10 * time.Second, 30 * time.Second, time.Minute, 5 * time.Minute, 15 * time.Minute}
	if attempts < 0 {
		attempts = 0
	}
	if attempts >= len(delays) {
		return delays[len(delays)-1]
	}
	return delays[attempts]
}

// RunOnce claims and attempts one atomic audit append. On a failed or unknown
// outcome it reconciles by event UUID before updating each outbox row.
func (relay *Relay) RunOnce(ctx context.Context) (int, error) {
	events, err := relay.outbox.ClaimBatch(ctx, relay.workerID, DefaultBatchSize, DefaultLease)
	if err != nil {
		return 0, err
	}
	if len(events) == 0 {
		relay.observe(ctx)
		return 0, nil
	}
	logs := make([]model.AuditLog, len(events))
	uuids := make([]string, len(events))
	for index, event := range events {
		logs[index] = managementAuditLog(event)
		uuids[index] = event.EventUUID
	}
	relay.commitMu.Lock()
	_, deliveryErr := relay.audit.AppendBatch(ctx, logs)
	relay.commitMu.Unlock()
	persisted := make(map[string]struct{}, len(events))
	if deliveryErr == nil {
		for _, eventUUID := range uuids {
			persisted[eventUUID] = struct{}{}
		}
	} else {
		rows, findErr := relay.audit.FindByEventUUIDs(ctx, uuids)
		if findErr != nil {
			deliveryErr = errors.Join(deliveryErr, fmt.Errorf("reconcile audit batch: %w", findErr))
		} else {
			for _, row := range rows {
				if row.EventUUID != nil {
					persisted[*row.EventUUID] = struct{}{}
				}
			}
		}
	}
	delivered := 0
	var failures []error
	for _, event := range events {
		if _, found := persisted[event.EventUUID]; found {
			if markErr := relay.outbox.MarkDelivered(ctx, event.EventUUID); markErr != nil && !errors.Is(markErr, store.ErrNotFound) {
				failures = append(failures, fmt.Errorf("mark event %s delivered: %w", event.EventUUID, markErr))
				continue
			}
			delivered++
			continue
		}
		failureText := "audit batch unavailable"
		if deliveryErr != nil {
			failureText = deliveryErr.Error()
		}
		if markErr := relay.outbox.MarkFailed(ctx, event.EventUUID, failureText, Backoff(event.Attempts)); markErr != nil {
			failures = append(failures, fmt.Errorf("record event %s failure: %w", event.EventUUID, markErr))
		} else {
			failures = append(failures, fmt.Errorf("deliver event %s: audit unavailable", event.EventUUID))
		}
	}
	relay.observe(ctx)
	return delivered, errors.Join(failures...)
}

func (relay *Relay) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = 10 * time.Second
	}
	for {
		if _, err := relay.RunOnce(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("management audit relay round failed", "error", "delivery unavailable")
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (relay *Relay) observe(ctx context.Context) {
	count, age, err := relay.outbox.PendingStats(ctx)
	if err != nil {
		return
	}
	if relay.observer != nil {
		relay.observer.SetOutboxPending(count, age)
	}
	switch {
	case age > 30*time.Minute:
		slog.Error("management audit outbox backlog is critical", "pending", count, "oldest_age_seconds", int(age.Seconds()))
	case age > 5*time.Minute:
		slog.Warn("management audit outbox backlog is stale", "pending", count, "oldest_age_seconds", int(age.Seconds()))
	}
}

func managementAuditLog(event model.ManagementAuditOutbox) model.AuditLog {
	action, actorType, actorID, details, eventUUID := event.Action, event.ActorType, event.ActorID, event.DetailsJSON, event.EventUUID
	return model.AuditLog{
		Decision: "allow", Action: &action, ActorType: &actorType, ActorID: &actorID,
		DetailsJSON: &details, EventUUID: &eventUUID,
	}
}
