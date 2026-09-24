package bootstrap

import (
	"context"
	"fmt"

	"github.com/cuipengdba/agentsql/internal/audit"
	"github.com/cuipengdba/agentsql/internal/model"
)

type auditBatchAppender interface {
	AppendBatch(context.Context, []model.AuditLog) ([]model.AuditLog, error)
}

// auditBatchBackend is deliberately kept in bootstrap so audit can remain
// independent from the concrete store package.
type auditBatchBackend struct {
	appender auditBatchAppender
}

func newAuditBatchBackend(appender auditBatchAppender) *auditBatchBackend {
	return &auditBatchBackend{appender: appender}
}

func (backend *auditBatchBackend) InsertBatch(
	ctx context.Context,
	batch []model.AuditLog,
) ([]model.AuditLog, error) {
	if backend == nil || isNilBootstrapDependency(backend.appender) {
		return nil, fmt.Errorf("append audit batch: backend is unavailable")
	}
	return backend.appender.AppendBatch(ctx, batch)
}

var _ audit.BatchBackend = (*auditBatchBackend)(nil)
