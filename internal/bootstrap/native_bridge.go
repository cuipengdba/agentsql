package bootstrap

import (
	"context"
	"fmt"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
)

// These methods keep the runtime secret and read-only pool behind the runtime.
func (runtime *Runtime) NativeQueryRead(ctx context.Context, datasource model.Datasource, request executor.NativeQueryRequest) (executor.NativeQueryResult, error) {
	if runtime == nil {
		return executor.NativeQueryResult{}, fmt.Errorf("runtime is nil")
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.closed || runtime.businessRead == nil {
		return executor.NativeQueryResult{}, fmt.Errorf("runtime is closed")
	}
	return runtime.businessRead.NativeQueryRead(ctx, datasource, append([]byte(nil), runtime.secret...), request)
}

func (runtime *Runtime) MongoFindControlled(ctx context.Context, datasource model.Datasource, request executor.MongoFindRequest) (model.QueryResult, error) {
	if runtime == nil {
		return model.QueryResult{}, fmt.Errorf("runtime is nil")
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.closed || runtime.businessRead == nil {
		return model.QueryResult{}, fmt.Errorf("runtime is closed")
	}
	return runtime.businessRead.MongoFindControlled(ctx, datasource, append([]byte(nil), runtime.secret...), request)
}

func (runtime *Runtime) NativeRedactorFor(ctx context.Context, datasourceID string) (mask.Redactor, error) {
	if runtime == nil {
		return nil, fmt.Errorf("runtime is nil")
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.closed || runtime.redactors == nil {
		return nil, fmt.Errorf("runtime is closed")
	}
	return runtime.redactors.RedactorFor(ctx, datasourceID)
}

func (runtime *Runtime) NativeDiscover(ctx context.Context, datasource model.Datasource) (executor.NativeCatalog, error) {
	if runtime == nil {
		return executor.NativeCatalog{}, fmt.Errorf("runtime is nil")
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.closed || runtime.businessRead == nil {
		return executor.NativeCatalog{}, fmt.Errorf("runtime is closed")
	}
	return runtime.businessRead.NativeDiscover(ctx, datasource, append([]byte(nil), runtime.secret...))
}

func (runtime *Runtime) NativeServerVersion(ctx context.Context, datasource model.Datasource) (string, error) {
	if runtime == nil {
		return "", fmt.Errorf("runtime is nil")
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.closed || runtime.businessRead == nil {
		return "", fmt.Errorf("runtime is closed")
	}
	return runtime.businessRead.NativeServerVersion(ctx, datasource, append([]byte(nil), runtime.secret...))
}
