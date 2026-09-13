package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"

	"github.com/cuipengdba/agentsql/internal/audit"
	"github.com/cuipengdba/agentsql/internal/auth"
	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/executor"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/pipeline"
	"github.com/cuipengdba/agentsql/internal/store"
)

// Runtime owns the production pipeline, business connection manager, and
// metadata store for one AgentSQL process.
type Runtime struct {
	Pipeline  *pipeline.Pipeline
	Executors *executor.Manager
	Store     *store.Store

	mu        sync.Mutex
	secret    []byte
	redactors *redactorBuilder
	closed    bool
}

// Assemble validates configuration and wires the required pipeline ports and
// the optional store-backed runtime rule overrides.
func Assemble(ctx context.Context, cfg config.Config, secret []byte) (*Runtime, error) {
	return assembleWithExecutorProvider(ctx, cfg, secret, nil)
}

func assembleWithExecutorProvider(
	ctx context.Context,
	cfg config.Config,
	secret []byte,
	executorOverride pipeline.ExecutorProvider,
) (*Runtime, error) {
	if ctx == nil {
		return nil, fmt.Errorf("assemble runtime: context is required")
	}
	if len(secret) != 32 {
		return nil, fmt.Errorf("assemble runtime: secret must contain exactly 32 bytes")
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("assemble runtime configuration: %w", err)
	}
	metadataStore, err := store.OpenWithSecret(ctx, cfg.Store.SQLitePath, secret)
	if err != nil {
		return nil, fmt.Errorf("assemble metadata store: %w", err)
	}
	manager := executor.NewManager(false)
	executorPort := pipeline.ExecutorProvider(manager)
	if executorOverride != nil {
		executorPort = executorOverride
	}
	redactors := &redactorBuilder{repository: metadataStore.MaskRules()}
	flow, err := pipeline.New(pipeline.Ports{
		Authenticator: auth.NewAuthenticator(metadataStore.Agents()),
		Datasources:   metadataStore.Datasources(),
		Policies:      metadataStore.Policies(),
		Executors:     executorPort,
		Approvals:     metadataStore.Approvals(),
		Audit:         audit.NewRecorder(metadataStore.AuditLogs()),
		Redactors:     redactors,
		RuleOverrides: metadataStore.Rules(),
	}, secret)
	if err != nil {
		return nil, closeAfterAssemblyError(manager, metadataStore, err)
	}
	return &Runtime{
		Pipeline:  flow,
		Executors: manager,
		Store:     metadataStore,
		secret:    append([]byte(nil), secret...),
		redactors: redactors,
	}, nil
}

// ExecutorFor returns the managed executor for trusted metadata operations.
func (runtime *Runtime) ExecutorFor(datasource model.Datasource) (executor.Executor, error) {
	if runtime == nil {
		return nil, fmt.Errorf("runtime is nil")
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.closed || runtime.Executors == nil {
		return nil, fmt.Errorf("runtime is closed")
	}
	return runtime.Executors.GetOrOpen(datasource, append([]byte(nil), runtime.secret...))
}

// Close releases all business pools before closing metadata storage.
func (runtime *Runtime) Close() error {
	if runtime == nil {
		return nil
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.closed {
		return nil
	}
	runtime.closed = true
	var closeErrors []error
	if runtime.Executors != nil {
		if err := runtime.Executors.CloseAll(); err != nil {
			closeErrors = append(closeErrors, err)
		}
	}
	if runtime.Store != nil {
		if err := runtime.Store.Close(); err != nil {
			closeErrors = append(closeErrors, err)
		}
	}
	if len(closeErrors) > 0 {
		return fmt.Errorf("close runtime: %w", errors.Join(closeErrors...))
	}
	return nil
}

func closeAfterAssemblyError(
	manager *executor.Manager,
	metadataStore *store.Store,
	cause error,
) error {
	return errors.Join(cause, manager.CloseAll(), metadataStore.Close())
}

type maskRuleReader interface {
	ListByDatasource(ctx context.Context, datasourceID string) ([]model.MaskRule, error)
}

type redactorBuilder struct {
	repository maskRuleReader
}

func (builder *redactorBuilder) RedactorFor(
	ctx context.Context,
	datasourceID string,
) (mask.Redactor, error) {
	if builder == nil || isNilBootstrapDependency(builder.repository) || ctx == nil {
		return nil, fmt.Errorf("build redactor: dependency or context is unavailable")
	}
	storedRules, err := builder.repository.ListByDatasource(ctx, datasourceID)
	if err != nil {
		return nil, fmt.Errorf("load mask rules for datasource %q: %w", datasourceID, err)
	}
	rules := make([]mask.Rule, 0, len(storedRules))
	for _, stored := range storedRules {
		rules = append(rules, mask.Rule{
			Column:        stored.ColumnName,
			SensitiveType: mask.SensitiveType(stored.SensitiveType),
			Algorithm:     mask.Algorithm(stored.Algo),
		})
	}
	redactor, err := mask.NewRedactor(rules)
	if err != nil {
		return nil, fmt.Errorf("compile mask rules for datasource %q: %w", datasourceID, err)
	}
	return redactor, nil
}

func isNilBootstrapDependency(value any) bool {
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

var (
	_ pipeline.RedactorBuilder    = (*redactorBuilder)(nil)
	_ maskRuleReader              = (*store.MaskRuleRepository)(nil)
	_ pipeline.RuleOverrideReader = (*store.RuleRepository)(nil)
)
