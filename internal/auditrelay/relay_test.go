package auditrelay

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestBackoffSequenceAndCap(t *testing.T) {
	require.Equal(t, 10*time.Second, Backoff(0))
	require.Equal(t, 30*time.Second, Backoff(1))
	require.Equal(t, time.Minute, Backoff(2))
	require.Equal(t, 5*time.Minute, Backoff(3))
	require.Equal(t, 15*time.Minute, Backoff(4))
	require.Equal(t, 15*time.Minute, Backoff(99))
}

func TestRunOnceTreatsDuplicateAsDeliveredAndSchedulesFailures(t *testing.T) {
	duplicate := model.ManagementAuditOutbox{EventUUID: "duplicate", Action: "test", ActorType: "cli", ActorID: "ctl", DetailsJSON: `{}`, Attempts: 2}
	failing := model.ManagementAuditOutbox{EventUUID: "failing", Action: "test", ActorType: "cli", ActorID: "ctl", DetailsJSON: `{}`, Attempts: 3}
	outbox := &fakeOutbox{events: []model.ManagementAuditOutbox{duplicate, failing}}
	audit := &fakeAudit{
		appendErr: errors.New("postgres://secret@audit unavailable"),
		persisted: []model.AuditLog{{EventUUID: &duplicate.EventUUID}},
	}
	relay, err := New(outbox, audit, "worker", nil)
	require.NoError(t, err)
	delivered, err := relay.RunOnce(context.Background())
	require.Error(t, err)
	require.Equal(t, 1, delivered)
	require.Equal(t, []string{"duplicate"}, outbox.delivered)
	require.Equal(t, "failing", outbox.failedID)
	require.Equal(t, 5*time.Minute, outbox.backoff)
	require.Equal(t, 1, audit.appendCalls)
	require.Equal(t, []string{"duplicate", "failing"}, audit.appendedUUIDs)
}

func TestRunOnceAppendsClaimedEventsInOneBatch(t *testing.T) {
	events := []model.ManagementAuditOutbox{
		{EventUUID: "one", Action: "a", ActorType: "cli", ActorID: "ctl", DetailsJSON: `{}`},
		{EventUUID: "two", Action: "b", ActorType: "cli", ActorID: "ctl", DetailsJSON: `{}`},
	}
	outbox := &fakeOutbox{events: events}
	audit := &fakeAudit{}
	relay, err := New(outbox, audit, "worker", nil)
	require.NoError(t, err)
	delivered, err := relay.RunOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, delivered)
	require.Equal(t, 1, audit.appendCalls)
	require.Equal(t, []string{"one", "two"}, audit.appendedUUIDs)
	require.Equal(t, []string{"one", "two"}, outbox.delivered)
}

type fakeOutbox struct {
	events    []model.ManagementAuditOutbox
	delivered []string
	failedID  string
	backoff   time.Duration
}

func (fake *fakeOutbox) ClaimBatch(context.Context, string, int, time.Duration) ([]model.ManagementAuditOutbox, error) {
	return fake.events, nil
}
func (fake *fakeOutbox) MarkDelivered(_ context.Context, id string) error {
	fake.delivered = append(fake.delivered, id)
	return nil
}
func (fake *fakeOutbox) MarkFailed(_ context.Context, id, _ string, backoff time.Duration) error {
	fake.failedID, fake.backoff = id, backoff
	return nil
}
func (fake *fakeOutbox) PendingStats(context.Context) (int, time.Duration, error) { return 0, 0, nil }

type fakeAudit struct {
	appendErr     error
	persisted     []model.AuditLog
	appendCalls   int
	appendedUUIDs []string
}

func (fake *fakeAudit) AppendBatch(_ context.Context, logs []model.AuditLog) ([]model.AuditLog, error) {
	fake.appendCalls++
	for _, log := range logs {
		fake.appendedUUIDs = append(fake.appendedUUIDs, *log.EventUUID)
	}
	if fake.appendErr != nil {
		return nil, fake.appendErr
	}
	return logs, nil
}

func (fake *fakeAudit) FindByEventUUIDs(context.Context, []string) ([]model.AuditLog, error) {
	return fake.persisted, nil
}
