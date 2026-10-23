package bootstrap

import (
	"context"
	"time"

	"github.com/cuipengdba/agentsql/internal/audit"
	"github.com/cuipengdba/agentsql/internal/eventbus"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/pipeline"
	"github.com/google/uuid"
)

type eventPublisher interface {
	Publish(eventbus.Event)
}

type publishingAuditSink struct {
	inner     audit.Sink
	publisher eventPublisher
}

func (sink *publishingAuditSink) Insert(ctx context.Context, log model.AuditLog) (model.AuditLog, error) {
	// Group commit uses the event UUID both for result correlation and for
	// in-flight idempotency. Older recorder callers legitimately omit it, so
	// assign it at the production sink boundary before durable admission.
	if log.EventUUID == nil || *log.EventUUID == "" {
		eventUUID := uuid.NewString()
		log.EventUUID = &eventUUID
	}
	recorded, err := sink.inner.Insert(ctx, log)
	if err != nil {
		return recorded, err
	}
	bestEffortPublish(sink.publisher, recorded)
	return recorded, nil
}

type approvalWorkflowWriter interface {
	pipeline.ApprovalWriter
	pipeline.ApprovalWorkflow
	ClaimApproved(context.Context, string, string, time.Time) (model.Approval, error)
	ClaimApprovedForRequester(context.Context, string, string, string, time.Time) (model.Approval, error)
	CompleteClaim(context.Context, string, string, string, *int64, *string, time.Time) (model.Approval, error)
}

type publishingApprovalWorkflow struct {
	inner     approvalWorkflowWriter
	publisher eventPublisher
}

func (workflow *publishingApprovalWorkflow) Create(ctx context.Context, approval model.Approval) (model.Approval, error) {
	return workflow.inner.Create(ctx, approval)
}

func (workflow *publishingApprovalWorkflow) CreatePendingWithAudit(
	ctx context.Context,
	approval model.Approval,
	log model.AuditLog,
) (model.Approval, model.AuditLog, error) {
	created, recorded, err := workflow.inner.CreatePendingWithAudit(ctx, approval, log)
	if err != nil {
		return created, recorded, err
	}
	bestEffortPublish(workflow.publisher, recorded)
	return created, recorded, nil
}

func (workflow *publishingApprovalWorkflow) ClaimApproved(ctx context.Context, id, agentID string, now time.Time) (model.Approval, error) {
	return workflow.inner.ClaimApproved(ctx, id, agentID, now)
}

func (workflow *publishingApprovalWorkflow) ClaimApprovedForRequester(ctx context.Context, id, agentID, requesterUserID string, now time.Time) (model.Approval, error) {
	return workflow.inner.ClaimApprovedForRequester(ctx, id, agentID, requesterUserID, now)
}

func (workflow *publishingApprovalWorkflow) CompleteClaim(ctx context.Context, id, agentID, status string, auditID *int64, outcomeCode *string, now time.Time) (model.Approval, error) {
	return workflow.inner.CompleteClaim(ctx, id, agentID, status, auditID, outcomeCode, now)
}

func bestEffortPublish(publisher eventPublisher, recorded model.AuditLog) {
	if publisher == nil {
		return
	}
	defer func() {
		_ = recover()
	}()
	publisher.Publish(eventbus.Event{Audit: recorded})
}

var (
	_ audit.Sink                = (*publishingAuditSink)(nil)
	_ pipeline.ApprovalWriter   = (*publishingApprovalWorkflow)(nil)
	_ pipeline.ApprovalWorkflow = (*publishingApprovalWorkflow)(nil)
)
