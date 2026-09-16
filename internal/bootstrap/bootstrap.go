package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/cuipengdba/agentsql/internal/audit"
	"github.com/cuipengdba/agentsql/internal/auth"
	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/eventbus"
	"github.com/cuipengdba/agentsql/internal/executor"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/metrics"
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
	Metrics   *metrics.Metrics
	// Events is the in-process stream of successfully persisted audits.
	Events *eventbus.Hub

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
	resolvedStore, err := config.ResolveStore(&cfg, os.LookupEnv)
	if err != nil {
		return nil, fmt.Errorf("assemble runtime configuration: %w", err)
	}
	metadataStore, err := store.OpenMetadata(ctx, resolvedStore.MetadataOptions(), secret)
	if err != nil {
		return nil, fmt.Errorf("assemble metadata store: %w", err)
	}
	manager := executor.NewManager(false)
	metricsHub := metrics.New(func() []metrics.PoolStat {
		snapshots := manager.SnapshotPools()
		result := make([]metrics.PoolStat, 0, len(snapshots))
		for _, snapshot := range snapshots {
			result = append(result, metrics.PoolStat{
				DatasourceID: snapshot.DatasourceID,
				Dialect:      snapshot.Dialect,
				MaxOpen:      snapshot.MaxOpen,
				InUse:        snapshot.InUse,
				Idle:         snapshot.Idle,
			})
		}
		return result
	})
	executorPort := pipeline.ExecutorProvider(manager)
	if executorOverride != nil {
		executorPort = executorOverride
	}
	redactors := &redactorBuilder{repository: metadataStore.MaskRules()}
	var events *eventbus.Hub
	auditSink := audit.Sink(metadataStore.AuditLogs())
	approvals := pipeline.ApprovalWriter(metadataStore.Approvals())
	if cfg.Server.ConsoleEnabled && cfg.Server.EventStream {
		events, err = eventbus.New(eventbus.Options{HistorySize: 200, SubscriberBuffer: 64})
		if err != nil {
			return nil, closeAfterAssemblyError(manager, metadataStore, fmt.Errorf("assemble event stream: %w", err))
		}
		auditSink = &publishingAuditSink{inner: auditSink, publisher: events}
		approvals = &publishingApprovalWorkflow{inner: metadataStore.Approvals(), publisher: events}
	}
	flow, err := pipeline.New(pipeline.Ports{
		Authenticator: auth.NewAuthenticator(metadataStore.Agents()),
		Datasources:   metadataStore.Datasources(),
		Policies:      metadataStore.Policies(),
		Executors:     executorPort,
		Approvals:     approvals,
		Audit:         audit.NewRecorder(auditSink),
		Redactors:     redactors,
		RuleOverrides: metadataStore.Rules(),
	}, secret, pipeline.WithObserver(metricsHub))
	if err != nil {
		events.Close()
		return nil, closeAfterAssemblyError(manager, metadataStore, err)
	}
	return &Runtime{
		Pipeline:  flow,
		Executors: manager,
		Store:     metadataStore,
		Metrics:   metricsHub,
		Events:    events,
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
	if runtime.Events != nil {
		runtime.Events.Close()
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
	allRules := make([]mask.Rule, 0, len(storedRules))
	for _, stored := range storedRules {
		allRules = append(allRules, mask.Rule{
			Column:        stored.ColumnName,
			SensitiveType: mask.SensitiveType(stored.SensitiveType),
			Algorithm:     mask.Algorithm(stored.Algo),
		})
	}
	// Validate every raw rule before precedence can hide it. This preserves the
	// fail-closed contract for malformed persisted configuration.
	if _, err := mask.NewRedactor(allRules); err != nil {
		return nil, fmt.Errorf("compile mask rules for datasource %q: %w", datasourceID, err)
	}
	sort.SliceStable(storedRules, func(left, right int) bool {
		leftColumn := mask.NormalizeColumnName(storedRules[left].ColumnName)
		rightColumn := mask.NormalizeColumnName(storedRules[right].ColumnName)
		if leftColumn != rightColumn {
			return leftColumn < rightColumn
		}
		leftBound := maskRuleBoundToDatasource(storedRules[left], datasourceID)
		rightBound := maskRuleBoundToDatasource(storedRules[right], datasourceID)
		if leftBound != rightBound {
			return leftBound
		}
		leftPhone := storedRules[left].SensitiveType == string(mask.TypePhone)
		rightPhone := storedRules[right].SensitiveType == string(mask.TypePhone)
		if leftPhone != rightPhone {
			return leftPhone
		}
		if storedRules[left].SensitiveType != storedRules[right].SensitiveType {
			return storedRules[left].SensitiveType < storedRules[right].SensitiveType
		}
		if storedRules[left].Algo != storedRules[right].Algo {
			return storedRules[left].Algo < storedRules[right].Algo
		}
		return storedRules[left].ID < storedRules[right].ID
	})
	rules := make([]mask.Rule, 0, len(storedRules))
	seenColumns := make(map[string]struct{}, len(storedRules))
	for _, stored := range storedRules {
		column := mask.NormalizeColumnName(stored.ColumnName)
		if _, exists := seenColumns[column]; exists {
			continue
		}
		seenColumns[column] = struct{}{}
		rules = append(rules, mask.Rule{
			Column:        column,
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

func maskRuleBoundToDatasource(rule model.MaskRule, datasourceID string) bool {
	return rule.DatasourceID != nil && strings.TrimSpace(*rule.DatasourceID) != "" &&
		*rule.DatasourceID == datasourceID
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
	_ pipeline.DecisionObserver   = (*metrics.Metrics)(nil)
)
