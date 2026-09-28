package bootstrap

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/b5"
	"github.com/cuipengdba/agentsql/internal/b5coordinator"
	"github.com/cuipengdba/agentsql/internal/b5dml"
	"github.com/cuipengdba/agentsql/internal/b5session"
	"github.com/cuipengdba/agentsql/internal/b5wal"
	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/store"
)

// B5DatasourceAuthority is derived exclusively from metadata and a live
// read-only capability probe. MCP request dialect/version fields never become
// authority.
type B5DatasourceAuthority struct {
	Dialect, Mode                                   string
	ServerMajor                                     int
	KeyRevision, DatasourceRevision, PolicyRevision uint64
}

// B5Runtime owns the complete production B5 graph. Protocol packages receive
// only the narrow directory/coordinator/analyzer surfaces below.
type B5Runtime struct {
	Directory   *b5session.Directory
	Coordinator *b5coordinator.Coordinator
	Analyzer    b5coordinator.PlanAnalyzerResolver
	InstanceID  string
	StickyRoute string
	Limits      b5coordinator.ResourceLimits

	postgresEnabled bool
	router          *b5DatasourceRouter
	gateway         *executor.Gateway
	wal             *b5wal.Manager
	drain           time.Duration
	admitting       atomic.Bool
	cancel          context.CancelFunc
	wait            sync.WaitGroup
	closeOnce       sync.Once
	closeErr        error
	startupFailure  error
}

func (runtime *B5Runtime) Enabled() bool         { return runtime != nil }
func (runtime *B5Runtime) PostgresEnabled() bool { return runtime != nil && runtime.postgresEnabled }

func (runtime *B5Runtime) Admit() error {
	if runtime != nil && runtime.startupFailure != nil {
		return &b5coordinator.Failure{Code: b5.ErrorAuditEmergencyWALUnavailable, Cause: errors.New("B5 审计/WAL 持久化能力不可用，未开始事务且未取得可写连接")}
	}
	if runtime == nil || !runtime.admitting.Load() {
		return &b5coordinator.Failure{Code: b5.ErrorSessionRouteUnavailable, Cause: errors.New("B5 正在关闭，已停止接收新会话或事务")}
	}
	return nil
}

func (runtime *B5Runtime) ResolveDatasource(ctx context.Context, id string) (B5DatasourceAuthority, error) {
	if runtime != nil && runtime.router != nil {
		if err := runtime.router.checkDialect(ctx, id); err != nil {
			return B5DatasourceAuthority{}, err
		}
	}
	if err := runtime.Admit(); err != nil {
		return B5DatasourceAuthority{}, err
	}
	if !runtime.postgresEnabled {
		return B5DatasourceAuthority{}, &b5coordinator.Failure{Code: b5.ErrorDialectTransactionUnsupported, Cause: errors.New("PostgreSQL 事务已被显式关闭")}
	}
	return runtime.router.resolve(ctx, id)
}

// CheckDatasourceDialect performs only metadata lookup. It is safe to run
// before parsing a client plan and guarantees that MySQL is rejected without
// opening a business pool or probing a server.
func (runtime *B5Runtime) CheckDatasourceDialect(ctx context.Context, id string) error {
	if runtime == nil || runtime.router == nil {
		return &b5coordinator.Failure{Code: b5.ErrorTxPlanUnproven, Cause: errors.New("B5 数据源目录不可用")}
	}
	if err := runtime.router.checkDialect(ctx, id); err != nil {
		return err
	}
	return runtime.Admit()
}

func (runtime *B5Runtime) FinalFence(ctx context.Context, result b5coordinator.Result) error {
	if runtime == nil || runtime.Coordinator == nil || result.TransactionID == "" || result.Status != b5.TransactionTerminal {
		return errors.New("B5 最终栅栏缺少终态事务")
	}
	return runtime.Coordinator.FinalFence(ctx, result.TransactionID)
}

func (runtime *B5Runtime) Status(_ context.Context) (B5Status, error) {
	if runtime == nil {
		return B5Status{Enabled: false, State: "FEATURE_OFF", Reason: "B5_SESSIONS_FEATURE_OFF", Ready: true}, nil
	}
	if runtime.startupFailure != nil {
		return B5Status{Enabled: true, State: "DEGRADED", Reason: "B5_AUDIT_WAL_UNAVAILABLE", Ready: false}, nil
	}
	failures := runtime.router.failureCount()
	state, reason := "READY", "B5_READY"
	if failures > 0 {
		state, reason = "READY_WITH_DATASOURCE_ERRORS", fmt.Sprintf("B5_DATASOURCE_ERRORS_%d", failures)
	}
	if !runtime.admitting.Load() {
		state, reason = "DRAINING", "B5_SHUTDOWN_DRAINING"
		return B5Status{Enabled: true, State: state, Reason: reason, Ready: false}, nil
	}
	return B5Status{Enabled: true, State: state, Reason: reason, Ready: true}, nil
}

func (runtime *B5Runtime) close() error {
	if runtime == nil {
		return nil
	}
	runtime.closeOnce.Do(func() {
		// Refuse admission first, then drain/rollback active transactions while
		// primary audit, receipts and WAL are still writable.
		runtime.admitting.Store(false)
		// Stop the expiry worker before taking the deterministic shutdown drain;
		// this leaves exactly one owner of terminal transitions.
		if runtime.cancel != nil {
			runtime.cancel()
		}
		runtime.wait.Wait()
		ctx, cancel := context.WithTimeout(context.Background(), runtime.drain)
		defer cancel()
		if runtime.Coordinator != nil {
			runtime.closeErr = errors.Join(runtime.closeErr, runtime.Coordinator.Shutdown(ctx))
		}
		if runtime.wal != nil {
			runtime.closeErr = errors.Join(runtime.closeErr, runtime.wal.Close())
		}
		// Business pools are released only after every terminal audit/WAL fence.
		if runtime.gateway != nil {
			runtime.closeErr = errors.Join(runtime.closeErr, runtime.gateway.CloseAll())
		}
	})
	return runtime.closeErr
}

func assembleB5Runtime(ctx context.Context, cfg config.Config, resolved *config.ResolvedStore, metadata *store.Store, secret []byte) (*B5Runtime, error) {
	mcp := cfg.EffectiveMCP()
	if !mcp.Sessions.Enabled {
		return nil, nil
	}
	sealerKey := sha256.Sum256(append(append([]byte(nil), secret...), []byte("agentsql.b5.continuation-sealer.v1")...))
	sealer, err := b5session.NewAESGCMSealer("agentsql-b5-local-v1", sealerKey[:])
	if err != nil {
		return nil, fmt.Errorf("assemble B5 continuation sealer: %w", err)
	}
	directory, err := b5session.NewDirectory(b5session.DirectoryConfig{Store: metadata.B5Sessions(), Sealer: sealer,
		IdleTTL: time.Duration(mcp.Sessions.IdleTTLMS) * time.Millisecond, AbsoluteTTL: time.Duration(mcp.Sessions.AbsoluteTTLMS) * time.Millisecond})
	if err != nil {
		return nil, fmt.Errorf("assemble B5 session directory: %w", err)
	}
	instanceID := strings.TrimSpace(mcp.Sessions.InstanceID)
	if instanceID == "" {
		instanceID = strings.TrimSpace(cfg.ColumnAuthorization.InstanceID)
	}
	if instanceID == "" {
		host, _ := os.Hostname()
		digest := sha256.Sum256(append(append([]byte(host), 0), secret...))
		instanceID = "agentsql-b5-" + hex.EncodeToString(digest[:8])
	}
	sticky := strings.TrimSpace(mcp.Sessions.StickyRoute)
	if sticky == "" {
		sticky = instanceID
	}
	ownerDigest := sha256.Sum256([]byte("agentsql.b5.wal-owner.v1\x00" + instanceID))
	walOwner := binary.BigEndian.Uint64(ownerDigest[:8])
	if walOwner == 0 {
		walOwner = 1
	}
	walDirectory := strings.TrimSpace(mcp.Transactions.WALDirectory)
	if walDirectory == "" && resolved != nil && resolved.Metadata.SQLitePath != "" {
		walDirectory = filepath.Join(filepath.Dir(resolved.Metadata.SQLitePath), "b5-wal")
	}
	if walDirectory == "" {
		walDirectory = filepath.Join("data", "b5-wal")
	}
	walDirectory = filepath.Clean(walDirectory)
	if err := os.MkdirAll(walDirectory, 0o700); err != nil {
		return nil, fmt.Errorf("assemble B5 WAL directory: %w", err)
	}
	kms, err := newLocalB5KMS(secret)
	if err != nil {
		return nil, err
	}
	factory := b5wal.SegmentFactory{MasterRevision: "local-v1", Registry: b5wal.FileKeyRegistry{Directory: walDirectory},
		Wrapper: b5wal.KMSKeyWrapper{KMS: kms, MasterRevision: "local-v1"}, Manifests: b5wal.FileManifestStore{Directory: walDirectory}}
	manager, walErr := b5wal.NewFileManager(ctx, factory, walOwner, walDirectory, directoryFsyncAttestor{})
	var auditor b5coordinator.Auditor
	if walErr != nil {
		auditor = unavailableB5Auditor{cause: walErr}
	} else {
		primary, primaryErr := b5wal.NewPGPrimaryStore(metadata.B5TxEvents())
		if primaryErr != nil {
			_ = manager.Close()
			return nil, primaryErr
		}
		receipts, receiptErr := b5wal.NewPGReceiptStore(metadata.B5ResultReceipts())
		if receiptErr != nil {
			_ = manager.Close()
			return nil, receiptErr
		}
		auditor, err = b5coordinator.NewWALAuditor(&b5wal.Service{Primary: primary, Receipts: receipts, WAL: manager})
		if err != nil {
			_ = manager.Close()
			return nil, err
		}
	}
	// B5 owns a dedicated pool manager. This prevents datasource revision and
	// shutdown decisions from racing the legacy request pipeline's shared pools.
	b5Gateway := executor.NewGateway(false)
	router := &b5DatasourceRouter{metadata: metadata, gateway: b5Gateway, secret: append([]byte(nil), secret...),
		postgresEnabled: mcp.Transactions.Postgres, capabilities: make(map[string]B5DatasourceAuthority), failures: make(map[string]string),
		observedRevisions: make(map[string]uint64), engines: make(map[string]executor.B5PostgresRuntime)}
	coordinator, err := b5coordinator.New(b5coordinator.Config{Transactions: metadata.B5Transactions(), Sessions: b5coordinator.DirectorySessionGate{Directory: directory}, Engine: router, Audit: auditor})
	if err != nil {
		_ = b5Gateway.CloseAll()
		if manager != nil {
			_ = manager.Close()
		}
		return nil, fmt.Errorf("assemble B5 coordinator: %w", err)
	}
	runtime := &B5Runtime{Directory: directory, Coordinator: coordinator, Analyzer: router, InstanceID: instanceID, StickyRoute: sticky,
		Limits: b5coordinator.ResourceLimits{MaxStatements: 16, MaxSQLBytes: 256 << 10, MaxPlanBytes: 1 << 20, MaxAffectedRows: 10_000,
			MaxEstimatedWork: 1_000_000, IdleTimeout: time.Duration(mcp.Transactions.IdleTimeoutMS) * time.Millisecond,
			WallTimeout: time.Duration(mcp.Transactions.WallTimeoutMS) * time.Millisecond, StatementTimeout: time.Duration(mcp.Transactions.StatementTimeoutMS) * time.Millisecond,
			OperationWatchdog: 300 * time.Millisecond, QuiesceGrace: 25 * time.Millisecond},
		postgresEnabled: mcp.Transactions.Postgres, router: router, gateway: b5Gateway, wal: manager, drain: time.Duration(mcp.Transactions.ShutdownDrainMS) * time.Millisecond,
		startupFailure: walErr}
	runtime.admitting.Store(true)
	watchCtx, cancel := context.WithCancel(context.Background())
	runtime.cancel = cancel
	runtime.wait.Add(1)
	go runtime.watchdog(watchCtx)
	// Probe each configured datasource independently. Failure is retained as a
	// per-datasource denial and never grants another datasource more authority.
	rows, listErr := metadata.Datasources().List(ctx)
	if listErr != nil {
		_ = runtime.close()
		return nil, fmt.Errorf("assemble B5 datasource inventory: %w", listErr)
	}
	for _, datasource := range rows {
		if datasource.DBType == "mysql" {
			router.recordFailure(datasource.ID, string(b5.ErrorDialectTransactionUnsupported))
			continue
		}
		if datasource.DBType == "postgres" && mcp.Transactions.Postgres && walErr == nil {
			_, _ = router.resolve(ctx, datasource.ID)
		}
	}
	return runtime, nil
}

func (runtime *B5Runtime) watchdog(ctx context.Context) {
	defer runtime.wait.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			expireCtx, cancel := context.WithTimeout(context.Background(), 900*time.Millisecond)
			_, _ = runtime.Coordinator.Expire(expireCtx, 100)
			_, _ = runtime.Directory.Expire(expireCtx, 100)
			cancel()
		}
	}
}

type b5DatasourceRouter struct {
	metadata          *store.Store
	gateway           *executor.Gateway
	secret            []byte
	postgresEnabled   bool
	mu                sync.Mutex
	capabilities      map[string]B5DatasourceAuthority
	failures          map[string]string
	observedRevisions map[string]uint64
	engines           map[string]executor.B5PostgresRuntime
}

func (router *b5DatasourceRouter) resolve(ctx context.Context, id string) (B5DatasourceAuthority, error) {
	datasource, err := router.metadata.Datasources().Get(ctx, id)
	if err != nil {
		router.recordFailure(id, "DATASOURCE_NOT_FOUND")
		return B5DatasourceAuthority{}, &b5coordinator.Failure{Code: b5.ErrorTxPlanUnproven, Cause: errors.New("数据源不存在或不可访问，无法证明事务计划")}
	}
	if datasource.DBType == "mysql" {
		router.recordFailure(id, string(b5.ErrorDialectTransactionUnsupported))
		return B5DatasourceAuthority{}, &b5coordinator.Failure{Code: b5.ErrorDialectTransactionUnsupported, Cause: errors.New("MySQL 多语句事务在 AgentSQL v0.4 中不受支持；请改用 PostgreSQL")}
	}
	if datasource.DBType != "postgres" || !router.postgresEnabled {
		router.recordFailure(id, string(b5.ErrorDialectTransactionUnsupported))
		return B5DatasourceAuthority{}, &b5coordinator.Failure{Code: b5.ErrorDialectTransactionUnsupported, Cause: errors.New("该数据源方言未启用 B5 事务；请使用已启用的 PostgreSQL 数据源")}
	}
	revision := uint64(datasource.UpdatedAt.UnixNano())
	if revision == 0 {
		revision = 1
	}
	router.mu.Lock()
	observedRevision, observed := router.observedRevisions[id]
	if !observed {
		router.observedRevisions[id] = revision
	}
	router.mu.Unlock()
	if observed && observedRevision != revision {
		router.recordFailure(id, string(b5.ErrorTxPlanUnproven))
		return B5DatasourceAuthority{}, &b5coordinator.Failure{Code: b5.ErrorTxPlanUnproven, Cause: errors.New("数据源配置或凭据版本已变化；请先 drain 并重启 B5，旧连接不会被复用")}
	}
	capability, err := router.gateway.ProbePostgresB2Modes(ctx, datasource, router.secret)
	if err != nil {
		code := b5.ErrorPostgresCapabilityUnavailable
		cause := errors.New("PostgreSQL 能力探测失败，未开始事务；请检查连接、权限和封闭 binder 能力")
		var authErr *executor.AuthError
		if errors.As(err, &authErr) && authErr.Reason == executor.ReasonBinderModeUnsupported {
			code = b5.ErrorPostgresVersionUnsupported
			cause = errors.New("PostgreSQL 主版本不在受支持的 14 到 18 范围内，未开始事务")
		}
		router.recordFailure(id, string(code))
		return B5DatasourceAuthority{}, &b5coordinator.Failure{Code: code, Cause: cause}
	}
	if capability.ServerMajor < 14 || capability.ServerMajor > 18 {
		router.recordFailure(id, string(b5.ErrorPostgresVersionUnsupported))
		return B5DatasourceAuthority{}, &b5coordinator.Failure{Code: b5.ErrorPostgresVersionUnsupported, Cause: fmt.Errorf("PostgreSQL 主版本 %d 不受支持；请使用 14 到 18", capability.ServerMajor)}
	}
	mode := "CATALOG_CLOSED_V1"
	if capability.NativeAvailable {
		mode = "NATIVE_C_V1"
	}
	value := B5DatasourceAuthority{Dialect: "postgres", Mode: mode, ServerMajor: capability.ServerMajor, KeyRevision: 1, DatasourceRevision: revision, PolicyRevision: 1}
	router.mu.Lock()
	router.capabilities[id] = value
	delete(router.failures, id)
	router.mu.Unlock()
	return value, nil
}

func (router *b5DatasourceRouter) checkDialect(ctx context.Context, id string) error {
	datasource, err := router.metadata.Datasources().Get(ctx, id)
	if err != nil {
		router.recordFailure(id, "DATASOURCE_NOT_FOUND")
		return &b5coordinator.Failure{Code: b5.ErrorTxPlanUnproven, Cause: errors.New("数据源不存在或不可访问，无法证明事务计划")}
	}
	if datasource.DBType == "mysql" {
		router.recordFailure(id, string(b5.ErrorDialectTransactionUnsupported))
		return &b5coordinator.Failure{Code: b5.ErrorDialectTransactionUnsupported, Cause: errors.New("MySQL 多语句事务在 AgentSQL v0.4 中不受支持；未开始事务且未取得可写连接")}
	}
	if datasource.DBType != "postgres" || !router.postgresEnabled {
		router.recordFailure(id, string(b5.ErrorDialectTransactionUnsupported))
		return &b5coordinator.Failure{Code: b5.ErrorDialectTransactionUnsupported, Cause: errors.New("该数据源方言未启用 B5 事务；请使用已启用的 PostgreSQL 数据源")}
	}
	return nil
}

func (router *b5DatasourceRouter) AnalyzerFor(ctx context.Context, request b5coordinator.PlanRequest) (b5coordinator.Analyzer, error) {
	authority, err := router.resolve(ctx, request.DatasourceID)
	if err != nil {
		return nil, err
	}
	if request.Dialect != authority.Dialect || request.ServerMajor != authority.ServerMajor {
		return nil, &b5coordinator.Failure{Code: b5.ErrorTxPlanUnproven, Cause: errors.New("请求声明与服务端数据源能力不一致，事务计划不可证明")}
	}
	key := b5EngineKey(request.PrincipalID, request.DatasourceID, authority)
	router.mu.Lock()
	if existing, ok := router.engines[key]; ok {
		router.mu.Unlock()
		return existing.Analyzer, nil
	}
	router.mu.Unlock()
	datasource, err := router.metadata.Datasources().Get(ctx, request.DatasourceID)
	if err != nil {
		return nil, err
	}
	provider := router.policyProvider(request.PrincipalID, request.DatasourceID)
	var runtime executor.B5PostgresRuntime
	if authority.Mode == "NATIVE_C_V1" {
		runtime, err = router.gateway.NewB5PostgresRuntimeWithPolicyProvider(ctx, datasource, router.secret, provider)
	} else {
		runtime, err = router.gateway.NewB5ClosedPostgresRuntimeWithPolicyProvider(ctx, datasource, router.secret, provider)
	}
	if err != nil {
		router.recordFailure(request.DatasourceID, string(b5.ErrorPostgresCapabilityUnavailable))
		return nil, &b5coordinator.Failure{Code: b5.ErrorPostgresCapabilityUnavailable, Cause: errors.New("PostgreSQL 事务运行时核验失败，未取得可写连接")}
	}
	router.mu.Lock()
	if existing, ok := router.engines[key]; ok {
		router.mu.Unlock()
		return existing.Analyzer, nil
	}
	router.engines[key] = runtime
	router.mu.Unlock()
	return runtime.Analyzer, nil
}

func (router *b5DatasourceRouter) Pin(ctx context.Context, plan b5coordinator.Plan, begin *b5dml.BeginMachine) (b5coordinator.BeginCapability, b5coordinator.BackendIdentity, error) {
	authority, err := router.resolve(ctx, plan.DatasourceID)
	if err != nil {
		return nil, b5coordinator.BackendIdentity{}, err
	}
	key := b5EngineKey(plan.PrincipalID, plan.DatasourceID, authority)
	router.mu.Lock()
	runtime, ok := router.engines[key]
	router.mu.Unlock()
	if !ok || plan.ServerMajor != authority.ServerMajor || plan.ClosurePolicy != authority.Mode ||
		plan.KeyRevision != authority.KeyRevision || plan.DatasourceRevision != authority.DatasourceRevision ||
		plan.PolicyRevision != authority.PolicyRevision || runtime.Engine == nil {
		return nil, b5coordinator.BackendIdentity{}, &b5coordinator.Failure{Code: b5.ErrorTxPlanUnproven, Cause: errors.New("事务计划能力快照已变化，未取得可写连接")}
	}
	return runtime.Engine.Pin(ctx, plan, begin)
}

func b5EngineKey(principal, datasource string, authority B5DatasourceAuthority) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%d\x00%d\x00%d\x00%d", principal, datasource, authority.Mode,
		authority.ServerMajor, authority.KeyRevision, authority.DatasourceRevision, authority.PolicyRevision)
}

func (router *b5DatasourceRouter) policyProvider(principal, datasource string) executor.B5DMLPolicyProvider {
	return func(ctx context.Context, facts b5dml.StatementFacts, _ b5coordinator.StatementRequest, _ int) (executor.B5DMLAuthorizationConfig, error) {
		rows, err := router.metadata.B5DMLGrants().List(ctx, principal, datasource, facts.Action, 1000)
		if err != nil {
			return executor.B5DMLAuthorizationConfig{}, err
		}
		policies, digest, err := b5Policies(rows)
		if err != nil {
			return executor.B5DMLAuthorizationConfig{}, err
		}
		return executor.B5DMLAuthorizationConfig{PrincipalID: principal, DatasourceID: datasource, Policies: policies,
			Decision: b5coordinator.DecisionAllow, PreliminaryAllowed: true, DatasourceSupported: true, PolicySnapshotDigest: digest}, nil
	}
}

func b5Policies(rows []store.B5DMLGrant) ([]b5dml.Policy, string, error) {
	sort.Slice(rows, func(i, j int) bool { return rows[i].GrantID < rows[j].GrantID })
	encoded, _ := json.Marshal(rows)
	digest := sha256.Sum256(encoded)
	byID := make(map[string]*b5dml.Policy)
	order := make([]string, 0)
	for _, row := range rows {
		if row.PolicyID == "" || row.PolicyRevision <= 0 || row.PrincipalID == "" || row.DatasourceID == "" || len(row.RelationKind) != 1 ||
			row.ProofSchemaID != b5.DMLGrantProofSchemaID || row.ProofSchemaVersion != b5.DMLGrantProofSchemaVersion || len(row.ProofDigest) != sha256.Size {
			return nil, "", errors.New("B5 DML 授权记录不完整，计划不可证明")
		}
		policy := byID[row.PolicyID]
		if policy == nil {
			value := &b5dml.Policy{ID: row.PolicyID, Revision: uint64(row.PolicyRevision), PrincipalID: row.PrincipalID, DatasourceID: row.DatasourceID, Effect: row.Effect}
			byID[row.PolicyID], policy = value, value
			order = append(order, row.PolicyID)
		} else if policy.Revision != uint64(row.PolicyRevision) || policy.PrincipalID != row.PrincipalID || policy.DatasourceID != row.DatasourceID || policy.Effect != row.Effect {
			return nil, "", errors.New("B5 DML 授权策略版本冲突，计划不可证明")
		}
		relation := b5dml.RelationIdentity{DatasourceID: row.DatasourceID, DatabaseOID: row.DatabaseOID, RelationOID: row.RelationOID,
			RelationKind: row.RelationKind[0], Schema: row.SchemaName, Name: row.RelationName, CatalogFingerprint: row.CatalogFingerprint}
		grant := b5dml.Grant{Element: row.Element, Action: row.Action, Relation: relation}
		switch row.Element {
		case b5.GrantElementAction:
		case b5.GrantElementWriteTarget:
			if row.WriteTargetKind == nil {
				return nil, "", errors.New("B5 写目标授权缺少类型")
			}
			switch strings.ToUpper(*row.WriteTargetKind) {
			case "ROW":
				grant.WriteKind = b5dml.WriteTargetRow
			case "COLUMN":
				grant.WriteKind = b5dml.WriteTargetColumn
				column, err := b5GrantColumn(row, relation)
				if err != nil {
					return nil, "", err
				}
				grant.Column = column
			default:
				return nil, "", errors.New("B5 写目标授权类型不受支持")
			}
		case b5.GrantElementReference:
			if row.ReferenceKind == nil || strings.ToUpper(*row.ReferenceKind) != "COLUMN" {
				return nil, "", errors.New("B5 引用授权类型不受支持")
			}
			grant.ReferenceKind = b5dml.ReferenceColumn
			column, err := b5GrantColumn(row, relation)
			if err != nil {
				return nil, "", err
			}
			grant.Column = column
		default:
			return nil, "", errors.New("B5 授权元素不受支持")
		}
		policy.Grants = append(policy.Grants, grant)
	}
	sort.Strings(order)
	result := make([]b5dml.Policy, 0, len(order))
	for _, id := range order {
		result = append(result, *byID[id])
	}
	return result, hex.EncodeToString(digest[:]), nil
}

func b5GrantColumn(row store.B5DMLGrant, relation b5dml.RelationIdentity) (b5dml.ColumnIdentity, error) {
	if row.ColumnAttnum == nil || row.ColumnName == nil || row.ColumnTypeOID == nil || row.ColumnTypeModifier == nil || row.ColumnCollationOID == nil || *row.ColumnAttnum <= 0 {
		return b5dml.ColumnIdentity{}, errors.New("B5 列授权身份不完整")
	}
	return b5dml.ColumnIdentity{Relation: relation, Attnum: int16(*row.ColumnAttnum), Name: *row.ColumnName, TypeOID: *row.ColumnTypeOID,
		TypeModifier: int32(*row.ColumnTypeModifier), CollationOID: *row.ColumnCollationOID}, nil
}

func (router *b5DatasourceRouter) recordFailure(id, reason string) {
	router.mu.Lock()
	router.failures[id] = reason
	router.mu.Unlock()
}
func (router *b5DatasourceRouter) failureCount() int {
	router.mu.Lock()
	defer router.mu.Unlock()
	return len(router.failures)
}

type localB5KMS struct{ aead cipher.AEAD }

func newLocalB5KMS(secret []byte) (*localB5KMS, error) {
	key := sha256.Sum256(append(append([]byte(nil), secret...), []byte("agentsql.b5.wal-kek.v1")...))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &localB5KMS{aead: aead}, nil
}
func (kms *localB5KMS) Encrypt(_ context.Context, _ string, plaintext, aad []byte) ([]byte, error) {
	nonce := make([]byte, kms.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return kms.aead.Seal(nonce, nonce, plaintext, aad), nil
}
func (kms *localB5KMS) Decrypt(_ context.Context, _ string, ciphertext, aad []byte) ([]byte, error) {
	if len(ciphertext) < kms.aead.NonceSize() {
		return nil, errors.New("B5 WAL 密钥密文无效")
	}
	return kms.aead.Open(nil, ciphertext[:kms.aead.NonceSize()], ciphertext[kms.aead.NonceSize():], aad)
}

type directoryFsyncAttestor struct{}

func (directoryFsyncAttestor) Attest(ctx context.Context, directory string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := os.Open(directory)
	if err != nil {
		return err
	}
	return errors.Join(file.Sync(), file.Close())
}

type unavailableB5Auditor struct{ cause error }

func (auditor unavailableB5Auditor) Barrier(context.Context, b5coordinator.AuditEvent) (b5coordinator.AuditResult, error) {
	return b5coordinator.AuditResult{}, errors.Join(errors.New("B5 审计/WAL 屏障不可用"), auditor.cause)
}

var _ b5coordinator.PlanAnalyzerResolver = (*b5DatasourceRouter)(nil)
var _ b5coordinator.Engine = (*b5DatasourceRouter)(nil)
var _ b5wal.EnvelopeKMS = (*localB5KMS)(nil)
