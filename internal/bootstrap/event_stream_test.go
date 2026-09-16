package bootstrap

import (
	"context"
	"errors"
	"testing"

	"github.com/cuipengdba/agentsql/internal/audit"
	"github.com/cuipengdba/agentsql/internal/eventbus"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/pipeline"
	"github.com/stretchr/testify/require"
)

func TestPublishingAuditSinkPublishesOnlySuccessfulInsert(t *testing.T) {
	publisher := &recordingEventPublisher{}
	sink := &fakeAuditSink{recorded: model.AuditLog{ID: 41, Decision: "allow"}}
	decorated := &publishingAuditSink{inner: sink, publisher: publisher}

	recorded, err := decorated.Insert(context.Background(), model.AuditLog{Decision: "allow"})
	require.NoError(t, err)
	require.Equal(t, int64(41), recorded.ID)
	require.Equal(t, []int64{41}, publisher.ids)

	sink.err = errors.New("insert failed")
	_, err = decorated.Insert(context.Background(), model.AuditLog{Decision: "deny"})
	require.Error(t, err)
	require.Equal(t, []int64{41}, publisher.ids)
}

func TestPublishingAuditSinkContainsPublisherPanic(t *testing.T) {
	sink := &fakeAuditSink{recorded: model.AuditLog{ID: 42, Decision: "deny"}}
	decorated := &publishingAuditSink{inner: sink, publisher: panickingEventPublisher{}}
	recorded, err := decorated.Insert(context.Background(), model.AuditLog{Decision: "deny"})
	require.NoError(t, err)
	require.Equal(t, int64(42), recorded.ID)
}

func TestPublishingApprovalWorkflowPublishesOnlyCommittedAudit(t *testing.T) {
	publisher := &recordingEventPublisher{}
	inner := &fakeApprovalWorkflow{
		created:  model.Approval{ID: "approval-1", Status: "pending"},
		recorded: model.AuditLog{ID: 43, Decision: "approve"},
	}
	decorated := &publishingApprovalWorkflow{inner: inner, publisher: publisher}
	var writer pipeline.ApprovalWriter = decorated
	var workflow pipeline.ApprovalWorkflow = decorated

	created, err := writer.Create(context.Background(), model.Approval{ID: "plain"})
	require.NoError(t, err)
	require.Equal(t, "plain", created.ID)
	require.Empty(t, publisher.ids)

	created, recorded, err := workflow.CreatePendingWithAudit(context.Background(), model.Approval{ID: "approval-1"}, model.AuditLog{Decision: "approve"})
	require.NoError(t, err)
	require.Equal(t, "approval-1", created.ID)
	require.Equal(t, int64(43), recorded.ID)
	require.Equal(t, []int64{43}, publisher.ids)

	inner.err = errors.New("rollback")
	_, _, err = workflow.CreatePendingWithAudit(context.Background(), model.Approval{ID: "approval-2"}, model.AuditLog{Decision: "approve"})
	require.Error(t, err)
	require.Equal(t, []int64{43}, publisher.ids)
}

func TestPublishingApprovalWorkflowContainsPublisherPanic(t *testing.T) {
	inner := &fakeApprovalWorkflow{
		created:  model.Approval{ID: "approval-panic", Status: "pending"},
		recorded: model.AuditLog{ID: 44, Decision: "approve"},
	}
	decorated := &publishingApprovalWorkflow{inner: inner, publisher: panickingEventPublisher{}}
	created, recorded, err := decorated.CreatePendingWithAudit(
		context.Background(),
		model.Approval{ID: "approval-panic"},
		model.AuditLog{Decision: "approve"},
	)
	require.NoError(t, err)
	require.Equal(t, "approval-panic", created.ID)
	require.Equal(t, int64(44), recorded.ID)
}

type fakeAuditSink struct {
	recorded model.AuditLog
	err      error
}

func (sink *fakeAuditSink) Insert(context.Context, model.AuditLog) (model.AuditLog, error) {
	return sink.recorded, sink.err
}

type fakeApprovalWorkflow struct {
	created  model.Approval
	recorded model.AuditLog
	err      error
}

func (workflow *fakeApprovalWorkflow) Create(_ context.Context, approval model.Approval) (model.Approval, error) {
	return approval, workflow.err
}

func (workflow *fakeApprovalWorkflow) CreatePendingWithAudit(context.Context, model.Approval, model.AuditLog) (model.Approval, model.AuditLog, error) {
	return workflow.created, workflow.recorded, workflow.err
}

type recordingEventPublisher struct{ ids []int64 }

func (publisher *recordingEventPublisher) Publish(event eventbus.Event) {
	publisher.ids = append(publisher.ids, event.Audit.ID)
}

type panickingEventPublisher struct{}

func (panickingEventPublisher) Publish(eventbus.Event) { panic("publisher failure") }

var _ audit.Sink = (*fakeAuditSink)(nil)
