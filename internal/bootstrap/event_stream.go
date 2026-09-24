package bootstrap

import (
	"context"

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
