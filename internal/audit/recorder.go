package audit

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/cuipengdba/agentsql/internal/model"
)

var (
	// ErrInvalidDecision indicates an audit decision outside the frozen set.
	ErrInvalidDecision = errors.New("invalid audit decision")
	// ErrExportLimit indicates an export containing more than 100000 rows.
	ErrExportLimit = errors.New("audit export exceeds 100000 rows")
)

// Sink appends immutable audit events.
type Sink interface {
	Insert(ctx context.Context, log model.AuditLog) (model.AuditLog, error)
}

// Recorder synchronously records one validated audit event.
type Recorder interface {
	Record(ctx context.Context, log model.AuditLog) (model.AuditLog, error)
}

type recorder struct {
	sink Sink
}

// NewRecorder returns a synchronous recorder backed by sink.
func NewRecorder(sink Sink) Recorder {
	return &recorder{sink: sink}
}

func (recorder *recorder) Record(
	ctx context.Context,
	log model.AuditLog,
) (model.AuditLog, error) {
	if ctx == nil {
		return model.AuditLog{}, fmt.Errorf("record audit log: context is required")
	}
	if !validDecision(log.Decision) {
		return model.AuditLog{}, fmt.Errorf(
			"record audit log decision %q: %w",
			log.Decision,
			ErrInvalidDecision,
		)
	}
	if recorder == nil || isNilInterface(recorder.sink) {
		return model.AuditLog{}, fmt.Errorf("record audit log: sink is required")
	}
	return recorder.sink.Insert(ctx, log)
}

func validDecision(decision string) bool {
	switch decision {
	case "allow", "deny", "approve", "warn", "error":
		return true
	default:
		return false
	}
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

var _ Recorder = (*recorder)(nil)
