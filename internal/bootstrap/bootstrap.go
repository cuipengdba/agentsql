package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cuipengdba/agentsql/internal/audit"
	"github.com/cuipengdba/agentsql/internal/auditrelay"
	"github.com/cuipengdba/agentsql/internal/auth"
	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/controlledread"
	"github.com/cuipengdba/agentsql/internal/eventbus"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/metrics"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/notify"
	"github.com/cuipengdba/agentsql/internal/pipeline"
	redactionstate "github.com/cuipengdba/agentsql/internal/redaction"
	"github.com/cuipengdba/agentsql/internal/rules"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/google/uuid"
)

// Runtime owns the production pipeline, business connection manager, and
// metadata store for one AgentSQL process.
type Runtime struct {
	Pipeline        *pipeline.Pipeline
	ControlledRead  *controlledread.Service
	ManagementAudit audit.Recorder
	Store           *store.Store
	Metrics         *metrics.Metrics
	ChainMonitor    *ChainMonitor
	// Events is the in-process stream of successfully persisted audits.
	Events *eventbus.Hub
	// Notifications is the process-local best-effort notification manager.
	Notifications *notify.Manager

	mu           sync.Mutex
	secret       []byte
	redactors    *redactorBuilder
	names        *notificationNameResolver
	redaction    *redactionRuntime
	groupSink    *audit.GroupCommitSink
	business     *executor.Gateway
	businessRead *executor.Gateway
	b2           *b2Runtime
	b5Probe      func(context.Context) (B5Status, error)
	relayCancel  context.CancelFunc
	relayWait    sync.WaitGroup
	closed       bool
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
	resolvedRedaction, err := config.ResolveRedaction(&cfg, os.LookupEnv)
	if err != nil {
		return nil, fmt.Errorf("assemble runtime configuration: %w", err)
	}
	defer resolvedRedaction.Clear()
	metadataStore, err := store.OpenMetadata(ctx, resolvedStore.MetadataOptions(), secret)
	if err != nil {
		return nil, fmt.Errorf("assemble metadata store: %w", err)
	}
	redactors := &redactorBuilder{repository: metadataStore.MaskRules()}
	assembly, err := config.BuildRedactionAssembly(resolvedRedaction)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("assemble redaction: %w", err), metadataStore.Close())
	}
	cfg.Redaction.HashKey = ""
	cfg.Redaction.HashKeys = nil
	redactors.options = append(append([]mask.Option(nil), redactors.options...), assembly.PlanOptions...)
	redactors.hashAvailable = len(assembly.PlanOptions) != 0
	redactionRuntime, err := newRedactionRuntime(ctx, assembly.Observed, metadataStore.RedactionKeys())
	if err != nil {
		return nil, errors.Join(fmt.Errorf("assemble redaction registry reconciliation: %w", err), metadataStore.Close())
	}
	if err := redactors.validateEnabledRules(ctx, metadataStore.MaskRules()); err != nil {
		cause := fmt.Errorf(
			"assemble runtime: validate enabled redaction rules: %w; set AGENTSQL_REDACTION_HASH_KEY or redaction.hash_key",
			err,
		)
		return nil, errors.Join(cause, metadataStore.Close())
	}
	manager := executor.NewGateway(false)
	readOnlyManager := executor.NewGateway(true)
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
	metricsHub.SetB2State(B2StateFeatureOff)
	executorPort := pipeline.ExecutorProvider(manager)
	if executorOverride != nil {
		executorPort = executorOverride
	}
	events, err := eventbus.New(eventbus.Options{HistorySize: 200, SubscriberBuffer: 64})
	if err != nil {
		_ = readOnlyManager.CloseAll()
		return nil, closeAfterAssemblyError(manager, metadataStore, nil, fmt.Errorf("assemble event stream: %w", err))
	}
	chainAwareRepo := metadataStore.AuditLogs()
	var groupSink *audit.GroupCommitSink
	auditSink := audit.Sink(chainAwareRepo)
	if resolvedStore.Audit.ReuseMetadata {
		groupOptions := []audit.Option(nil)
		if resolvedStore.Metadata.Driver == store.DialectSQLite {
			groupOptions = audit.SQLiteGroupCommitOptions()
		}
		backend := newAuditBatchBackend(chainAwareRepo)
		groupSink = audit.NewGroupCommitSink(
			backend,
			func(model.AuditLog) string { return "management" },
			groupOptions...,
		)
		if err := groupSink.Start(); err != nil {
			events.Close()
			_ = readOnlyManager.CloseAll()
			return nil, closeAfterAssemblyError(manager, metadataStore, groupSink, fmt.Errorf("start audit group commit sink: %w", err))
		}
		auditSink = groupSink
	}
	approvals := pipeline.ApprovalWriter(metadataStore.Approvals())
	auditSink = &publishingAuditSink{inner: auditSink, publisher: events}
	approvals = &publishingApprovalWorkflow{inner: metadataStore.Approvals(), publisher: events}
	auditRecorder := audit.NewRecorder(auditSink)
	managementRecorder := auditRecorder
	if !resolvedStore.Audit.ReuseMetadata {
		managementRecorder = audit.NewRecorder(&publishingAuditSink{
			inner:     metadataStore.ManagementAuditLogs(),
			publisher: events,
		})
	}
	pipelineOptions := []pipeline.Option{pipeline.WithObserver(metricsHub), pipeline.WithDemoConfig(cfg.Demo)}
	var columnController *columnAuthorizationController
	var b2Manager *b2Runtime
	b2HandedOff := false
	defer func() {
		if b2Manager != nil && !b2HandedOff {
			b2Manager.close()
		}
	}()
	if cfg.ColumnAuthorization.Enabled || cfg.ColumnAuthorization.DryRun {
		var activationErr error
		if cfg.ColumnAuthorization.DryRun {
			b2Manager, activationErr = dryRunB2Runtime(ctx, metadataStore, manager, secret,
				strings.TrimSpace(cfg.ColumnAuthorization.InstanceID),
				time.Duration(cfg.ColumnAuthorization.LeaseMS)*time.Millisecond,
				time.Duration(cfg.ColumnAuthorization.HeartbeatIntervalMS)*time.Millisecond)
		} else {
			b2Manager, activationErr = activateB2Runtime(ctx, metadataStore, manager, secret,
				strings.TrimSpace(cfg.ColumnAuthorization.InstanceID),
				time.Duration(cfg.ColumnAuthorization.LeaseMS)*time.Millisecond,
				time.Duration(cfg.ColumnAuthorization.HeartbeatIntervalMS)*time.Millisecond)
		}
		b2Manager.attachStateObserver(metricsHub.SetB2State)
		status := b2Manager.snapshot()
		details, _ := json.Marshal(struct {
			B2 B2Status `json:"b2"`
		}{B2: status})
		action, actorType, actorID := "b2_activation", "runtime", status.InstanceID
		if cfg.ColumnAuthorization.DryRun {
			action = "b2_dry_run"
		}
		decision := string(model.DecisionWarn)
		if activationErr == nil && cfg.ColumnAuthorization.Enabled {
			decision = string(model.DecisionAllow)
		}
		text := string(details)
		if _, auditErr := managementRecorder.Record(ctx, model.AuditLog{Decision: decision, Action: &action,
			ActorType: &actorType, ActorID: &actorID, DetailsJSON: &text}); auditErr != nil {
			b2Manager.close()
			events.Close()
			_ = readOnlyManager.CloseAll()
			return nil, closeAfterAssemblyError(manager, metadataStore, groupSink, fmt.Errorf("audit B2 activation: %w", auditErr))
		}
		// When enforcement is enabled, install the controller even if activation
		// failed. PostgreSQL requests must remain on the B2 route and fail closed;
		// only explicit feature-off or dry-run uses the legacy table path.
		if cfg.ColumnAuthorization.Enabled {
			columnController = &columnAuthorizationController{
				fence: metadataStore.Fence(), redactors: redactors, instanceID: strings.TrimSpace(cfg.ColumnAuthorization.InstanceID), runtime: b2Manager,
			}
			pipelineOptions = append(pipelineOptions, pipeline.WithColumnAuthorization(columnController))
			_ = readOnlyManager.CloseAll()
			readOnlyManager = executor.NewGateway(true, executor.WithColumnAuthorizationProvider(&controlledReadColumnAuthorizationProvider{
				controller: columnController, recorder: auditRecorder,
			}))
		}
	}
	flow, err := pipeline.New(pipeline.Ports{
		Authenticator: auth.NewAuthenticator(metadataStore.Agents()),
		Datasources:   metadataStore.Datasources(),
		Policies:      metadataStore.Policies(),
		Executors:     executorPort,
		Approvals:     approvals,
		Audit:         auditRecorder,
		Redactors:     redactors,
		RuleOverrides: metadataStore.Rules(),
	}, secret, pipelineOptions...)
	if err != nil {
		events.Close()
		_ = readOnlyManager.CloseAll()
		return nil, closeAfterAssemblyError(manager, metadataStore, groupSink, err)
	}
	controlled, err := controlledread.NewService(
		metadataStore.Datasources(), readOnlyManager, secret, rules.NewDefaultTokenBucketLimiter(),
	)
	if err != nil {
		events.Close()
		_ = readOnlyManager.CloseAll()
		return nil, closeAfterAssemblyError(manager, metadataStore, groupSink, err)
	}
	names := newNotificationNameResolver(metadataStore)
	// Name enrichment is best effort. A failed warm-up leaves an empty cache and
	// must not stop the SQL control plane or notification delivery.
	_ = names.Refresh(ctx)
	notificationConfig, err := metadataStore.Notifications().Get(ctx)
	if err != nil {
		events.Close()
		_ = controlled.Close()
		return nil, closeAfterAssemblyError(manager, metadataStore, groupSink, fmt.Errorf("load notification configuration: %w", err))
	}
	notifications := notify.NewManager(events, notify.WithMetrics(metricsHub), notify.WithNameResolver(names))
	if err := notifications.Start(ctx, notificationConfig); err != nil {
		events.Close()
		_ = controlled.Close()
		return nil, closeAfterAssemblyError(manager, metadataStore, groupSink, fmt.Errorf("start notification manager: %w", err))
	}
	runtime := &Runtime{
		Pipeline:        flow,
		ControlledRead:  controlled,
		ManagementAudit: managementRecorder,
		Store:           metadataStore,
		Metrics:         metricsHub,
		Events:          events,
		Notifications:   notifications,
		secret:          append([]byte(nil), secret...),
		redactors:       redactors,
		names:           names,
		redaction:       redactionRuntime,
		groupSink:       groupSink,
		business:        manager,
		businessRead:    readOnlyManager,
		b2:              b2Manager,
	}
	runtime.ChainMonitor = NewChainMonitor(
		metadataStore,
		store.NewKeylessChainManifestProvider(),
		metricsHub,
		configuredChainDomains(!resolvedStore.Audit.ReuseMetadata),
		chainVerificationInterval,
	)
	redactionRuntime.attachMetrics(metricsHub)
	redactionRuntime.start(ctx, time.Minute)
	runtime.ChainMonitor.start(ctx)
	if !resolvedStore.Audit.ReuseMetadata {
		relay, relayErr := auditrelay.New(metadataStore.Outbox(), metadataStore.ManagementAuditLogs(), "gateway-"+uuid.NewString(), metricsHub)
		if relayErr != nil {
			return nil, errors.Join(relayErr, runtime.Close())
		}
		relayContext, cancelRelay := context.WithCancel(ctx)
		runtime.relayCancel = cancelRelay
		runtime.relayWait.Add(1)
		go func() {
			defer runtime.relayWait.Done()
			_ = relay.Run(relayContext, 10*time.Second)
		}()
	}
	b2HandedOff = true
	return runtime, nil
}

// RedactionReconciliation returns a concurrency-safe, non-secret snapshot for
// readiness and management observation.
func (runtime *Runtime) RedactionReconciliation() (redactionstate.Result, bool) {
	if runtime == nil || runtime.redaction == nil {
		return redactionstate.Result{}, false
	}
	return runtime.redaction.snapshot()
}

// B2Status returns the protocol-3 activation/lease state without exposing
// credentials or probe diagnostics.
func (runtime *Runtime) B2Status() B2Status {
	if runtime == nil || runtime.b2 == nil {
		return featureOffB2Status()
	}
	return runtime.b2.snapshot()
}

// B2Ready is false whenever enabled enforcement cannot admit protocol-3 work.
// Feature-off and dry-run do not gate the existing table-level service.
func (runtime *Runtime) B2Ready() bool {
	if runtime != nil && runtime.b2 != nil && runtime.b2.enabled {
		status := runtime.B2Status()
		if status.Reason == B2ReasonNoPostgresDatasource {
			return true
		}
		return status.Protocol == 3 && status.State == B2StateActive
	}
	status := runtime.B2Status()
	return status.Protocol != 3 || status.State == B2StateActive
}

// RedactionReady returns the last newly computed reconciliation gate. It never
// becomes true merely because time passed or another command reported success.
func (runtime *Runtime) RedactionReady() (bool, []int) {
	result, available := runtime.RedactionReconciliation()
	if !available {
		return true, nil
	}
	return result.Ready, append([]int(nil), result.Unsatisfied...)
}

// RerunRedactionReconciliation performs one strong read and atomically replaces
// readiness with the result from this round's observed snapshot.
func (runtime *Runtime) RerunRedactionReconciliation(ctx context.Context) error {
	if runtime == nil || runtime.redaction == nil {
		return fmt.Errorf("redaction reconciliation is unavailable")
	}
	return runtime.redaction.runStrong(ctx)
}

// RunPeriodicRedactionReconciliationOnce exposes the production periodic
// read-only behavior for deterministic scheduling tests and future orchestration.
func (runtime *Runtime) RunPeriodicRedactionReconciliationOnce(ctx context.Context) error {
	if runtime == nil || runtime.redaction == nil {
		return nil
	}
	return runtime.redaction.runPeriodic(ctx)
}

// RunChainVerificationOnce attempts one audit-chain verification round without
// changing process-wide readiness.
func (runtime *Runtime) RunChainVerificationOnce(ctx context.Context) {
	if runtime == nil || runtime.ChainMonitor == nil {
		return
	}
	runtime.ChainMonitor.RunChainVerificationOnce(ctx)
}

// AuditWriterReady reports only whether the expected HMAC key is currently
// available for writes in domain. Historical chain validity is not a gate.
func (runtime *Runtime) AuditWriterReady(domain string) bool {
	if runtime == nil || runtime.ChainMonitor == nil {
		return true
	}
	return runtime.ChainMonitor.AuditWriterReady(domain)
}

// ValidateRedactionActivation checks whether this process can execute an
// enabled rule without exposing the configured hash key to API handlers.
func (runtime *Runtime) ValidateRedactionActivation(rule mask.Rule) error {
	if runtime == nil || runtime.redactors == nil {
		if err := mask.ValidateRule(rule); err != nil {
			return err
		}
		if rule.Algorithm == mask.AlgoHash {
			return mask.ErrHashKeyRequired
		}
		return nil
	}
	return runtime.redactors.ValidateRedactionActivation(rule)
}

// RefreshNotificationNames atomically refreshes the notification display-name
// cache. Delivery keeps using the previous snapshot if refresh fails.
func (runtime *Runtime) RefreshNotificationNames(ctx context.Context) error {
	if runtime == nil || runtime.names == nil {
		return nil
	}
	return runtime.names.Refresh(ctx)
}

// PublishManagementAudit emits an already-persisted management audit. It is
// best effort and intentionally does not affect the committed operation.
func (runtime *Runtime) PublishManagementAudit(recorded model.AuditLog) {
	if runtime == nil {
		return
	}
	bestEffortPublish(runtime.Events, recorded)
}

// PingDatasource performs a fixed health operation without exporting a
// database handle or accepting SQL.
func (runtime *Runtime) PingDatasource(ctx context.Context, datasource model.Datasource) error {
	if runtime == nil {
		return fmt.Errorf("runtime is nil")
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.closed || runtime.businessRead == nil {
		return fmt.Errorf("runtime is closed")
	}
	return runtime.businessRead.Ping(ctx, datasource, append([]byte(nil), runtime.secret...))
}

// ListDatasourceSchema performs only the fixed typed information-schema read.
func (runtime *Runtime) ListDatasourceSchema(
	ctx context.Context,
	datasource model.Datasource,
	tables []executor.TableRef,
) ([]executor.SchemaColumn, error) {
	if runtime == nil {
		return nil, fmt.Errorf("runtime is nil")
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.closed || runtime.businessRead == nil {
		return nil, fmt.Errorf("runtime is closed")
	}
	return runtime.businessRead.ListSchema(ctx, datasource, append([]byte(nil), runtime.secret...), tables)
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
	// Stop audit admission first. Close performs the bounded drain and joins
	// every flusher before any store connection can be closed below.
	if runtime.groupSink != nil {
		if err := runtime.groupSink.Close(); err != nil {
			closeErrors = append(closeErrors, err)
		}
	}
	if runtime.relayCancel != nil {
		runtime.relayCancel()
		runtime.relayWait.Wait()
	}
	if runtime.redaction != nil {
		runtime.redaction.close()
	}
	if runtime.ChainMonitor != nil {
		runtime.ChainMonitor.close()
	}
	if runtime.b2 != nil {
		runtime.b2.close()
	}
	if runtime.Notifications != nil {
		if err := runtime.Notifications.Close(); err != nil {
			closeErrors = append(closeErrors, err)
		}
	}
	if runtime.business != nil {
		if err := runtime.business.CloseAll(); err != nil {
			closeErrors = append(closeErrors, err)
		}
	}
	if runtime.ControlledRead != nil {
		if err := runtime.ControlledRead.Close(); err != nil {
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
	manager *executor.Gateway,
	metadataStore *store.Store,
	groupSink *audit.GroupCommitSink,
	cause error,
) error {
	var groupErr error
	if groupSink != nil {
		groupErr = groupSink.Close()
	}
	return errors.Join(cause, manager.CloseAll(), groupErr, metadataStore.Close())
}

type maskRuleReader interface {
	ListEnabledByDatasource(ctx context.Context, datasourceID string) ([]model.MaskRule, error)
}

type redactorBuilder struct {
	repository    maskRuleReader
	options       []mask.Option
	hashAvailable bool
}

func (builder *redactorBuilder) RedactorFor(
	ctx context.Context,
	datasourceID string,
) (mask.Redactor, error) {
	if builder == nil || isNilBootstrapDependency(builder.repository) || ctx == nil {
		return nil, fmt.Errorf("build redactor: dependency or context is unavailable")
	}
	storedRules, err := builder.repository.ListEnabledByDatasource(ctx, datasourceID)
	if err != nil {
		return nil, fmt.Errorf("load mask rules for datasource %q: %w", datasourceID, err)
	}
	redactor, err := builder.compile(storedRules, datasourceID)
	if err != nil {
		return nil, fmt.Errorf("compile mask rules for datasource %q: %w", datasourceID, err)
	}
	return redactor, nil
}

func (builder *redactorBuilder) ValidateRedactionActivation(rule mask.Rule) error {
	if err := mask.ValidateRule(rule); err != nil {
		return err
	}
	if rule.Algorithm == mask.AlgoHash && !builder.hashAvailable {
		return mask.ErrHashKeyRequired
	}
	return nil
}

func (builder *redactorBuilder) validateEnabledRules(ctx context.Context, repository *store.MaskRuleRepository) error {
	storedRules, err := repository.List(ctx)
	if err != nil {
		return fmt.Errorf("list mask rules: %w", err)
	}
	byScope := make(map[string][]model.MaskRule)
	for _, stored := range storedRules {
		if !stored.Enabled {
			continue
		}
		scope := maskRuleScope(stored)
		byScope[scope] = append(byScope[scope], stored)
	}
	scopes := make([]string, 0, len(byScope))
	for scope := range byScope {
		scopes = append(scopes, scope)
	}
	if _, exists := byScope[""]; !exists {
		scopes = append(scopes, "")
	}
	sort.Strings(scopes)
	global := byScope[""]
	for _, scope := range scopes {
		rules := append([]model.MaskRule(nil), global...)
		if scope != "" {
			rules = append(rules, byScope[scope]...)
		}
		if _, err := builder.compile(rules, scope); err != nil {
			return fmt.Errorf("compile scope %q: %w", scope, err)
		}
	}
	return nil
}

func (builder *redactorBuilder) compile(storedRules []model.MaskRule, datasourceID string) (mask.Redactor, error) {
	global := make(map[string]mask.Rule)
	scoped := make(map[string]mask.Rule)
	for _, stored := range storedRules {
		rule := mask.Rule{
			Schema:        stored.SchemaName,
			Table:         stored.TableName,
			Column:        stored.ColumnName,
			SensitiveType: mask.SensitiveType(stored.SensitiveType),
			Algorithm:     mask.Algorithm(stored.Algo),
			Range:         rangeParamsFromStored(stored),
		}
		if err := mask.ValidateRule(rule); err != nil {
			return nil, err
		}
		scope := maskRuleScope(stored)
		if scope != "" && scope != datasourceID {
			continue
		}
		column := mask.NormalizeColumnName(rule.Column)
		key := maskRuleCompileKey(rule.Schema, rule.Table, column)
		target := global
		if scope != "" {
			target = scoped
		}
		if _, duplicate := target[key]; duplicate {
			return nil, fmt.Errorf("mask rule %q: %w", column, mask.ErrDuplicateMaskColumn)
		}
		rule.Column = column
		target[key] = rule
	}
	for key, rule := range scoped {
		global[key] = rule
	}
	keys := make([]string, 0, len(global))
	for key := range global {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	rules := make([]mask.Rule, 0, len(keys))
	for _, key := range keys {
		rules = append(rules, global[key])
	}
	return mask.NewRedactor(rules, builder.options...)
}

func maskRuleCompileKey(schema, table, column string) string {
	return schema + "\x00" + table + "\x00" + column
}

func rangeParamsFromStored(stored model.MaskRule) *mask.RangeParams {
	if stored.Algo != string(mask.AlgoRange) && stored.RangeBucketWidth == nil &&
		stored.RangeBucketOffset == nil && stored.RangeGranularity == nil {
		return nil
	}
	params := &mask.RangeParams{}
	if stored.RangeBucketWidth != nil {
		value := *stored.RangeBucketWidth
		params.BucketWidth = &value
	}
	if stored.RangeBucketOffset != nil {
		value := *stored.RangeBucketOffset
		params.BucketOffset = &value
	}
	if stored.RangeGranularity != nil {
		value := mask.RangeGranularity(*stored.RangeGranularity)
		params.Granularity = &value
	}
	return params
}

func maskRuleScope(rule model.MaskRule) string {
	if rule.DatasourceID == nil {
		return ""
	}
	return strings.TrimSpace(*rule.DatasourceID)
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
