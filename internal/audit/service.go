package audit

import (
	"context"
	"fmt"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
)

// Reader returns immutable filtered audit pages.
type Reader interface {
	FilteredPage(
		ctx context.Context,
		filter model.AuditFilter,
		page int,
		size int,
	) (store.AuditPage, error)
}

// Service combines synchronous recording, filtered reads, and export.
type Service struct {
	reader   Reader
	recorder Recorder
}

// NewService returns an audit service backed by repository-shaped interfaces.
func NewService(reader Reader, sink Sink) *Service {
	return &Service{reader: reader, recorder: NewRecorder(sink)}
}

// Record synchronously persists one validated audit event.
func (service *Service) Record(
	ctx context.Context,
	log model.AuditLog,
) (model.AuditLog, error) {
	if service == nil || isNilInterface(service.recorder) {
		return model.AuditLog{}, fmt.Errorf("record audit log: service is not initialized")
	}
	return service.recorder.Record(ctx, log)
}

// Page returns one filtered immutable audit page.
func (service *Service) Page(
	ctx context.Context,
	filter model.AuditFilter,
	page int,
	size int,
) (store.AuditPage, error) {
	if service == nil || isNilInterface(service.reader) {
		return store.AuditPage{}, fmt.Errorf("page audit logs: reader is required")
	}
	if ctx == nil {
		return store.AuditPage{}, fmt.Errorf("page audit logs: context is required")
	}
	return service.reader.FilteredPage(ctx, filter, page, size)
}

var (
	_ Sink   = (*store.AuditLogRepository)(nil)
	_ Reader = (*store.AuditLogRepository)(nil)
)
