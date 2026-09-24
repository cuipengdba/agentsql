package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
)

const (
	defaultQueueCapacity   = 4096
	defaultQueueMaxBytes   = 32 << 20
	defaultMaxEventBytes   = 64 << 10
	defaultMaxBatch        = 200
	defaultMaxWaiters      = 2048
	defaultRequestOverhead = 4 << 10
)

// BatchBackend durably commits one batch atomically. Returned rows may be in
// any order, but each row must carry the EventUUID from its input row.
type BatchBackend interface {
	InsertBatch(ctx context.Context, batch []model.AuditLog) ([]model.AuditLog, error)
}

// SinkError is the stable, transport-independent failure returned by the
// group-commit admission and flushing paths. HTTP mapping belongs to S5b.
type SinkError struct {
	Code         model.DBErrorCode `json:"code"`
	Reason       string            `json:"reason"`
	RetryAfterMS int               `json:"retry_after_ms,omitempty"`
	Retryable    bool              `json:"retryable"`
	Decision     string            `json:"decision"`
}

func (err *SinkError) Error() string {
	if err == nil {
		return "audit sink error"
	}
	return fmt.Sprintf("%s: %s", err.Code, err.Reason)
}

func sinkError(code model.DBErrorCode, reason string, retryable bool) error {
	retryAfter := 0
	if code == model.DBErrorCodeAuditOverloaded {
		retryAfter = 100
	}
	return &SinkError{
		Code:         code,
		Reason:       reason,
		RetryAfterMS: retryAfter,
		Retryable:    retryable,
		Decision:     "error",
	}
}

// BatchFailureKind tells the coordinator what is safe after a failed backend
// call. Backends must only report rolled-back when commit is known not to have
// happened.
type BatchFailureKind string

const (
	BatchFailureTransient     BatchFailureKind = "transient_rolled_back"
	BatchFailureDeterministic BatchFailureKind = "deterministic_rolled_back"
	BatchFailureGlobal        BatchFailureKind = "global"
	BatchFailureCommitUnknown BatchFailureKind = "commit_unknown"
)

// BatchError is the explicit error contract between an S5b adapter and this
// DB-independent coordinator. Retryable is honored only for transient and
// global failures; deterministic failures are isolated by stable bisection.
type BatchError struct {
	Kind      BatchFailureKind
	Retryable bool
	Cause     error
}

func (err *BatchError) Error() string {
	if err == nil || err.Cause == nil {
		return "audit batch failed"
	}
	return "audit batch failed: " + err.Cause.Error()
}

func (err *BatchError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Cause
}

// NewBatchError constructs an explicit backend outcome for the coordinator.
func NewBatchError(kind BatchFailureKind, retryable bool, cause error) error {
	return &BatchError{Kind: kind, Retryable: retryable, Cause: cause}
}

type canonicalDigestFunc func(model.AuditLog) (string, error)

type groupCommitOptions struct {
	queueCapacity     int
	queueMaxBytes     int64
	maxEventBytes     int
	maxBatch          int
	maxWaiters        int
	linger            time.Duration
	enqueueWait       time.Duration
	attemptTimeout    time.Duration
	rootTimeout       time.Duration
	drainTimeout      time.Duration
	canonicalDigest   canonicalDigestFunc
	flusherExitedHook func(string)
	initErr           error
}

// Option configures GroupCommitSink. Options are intentionally small and
// independently injectable so concurrency tests need not wait on production
// limits.
type Option func(*groupCommitOptions)

func WithQueueCapacity(value int) Option {
	return func(options *groupCommitOptions) {
		if value <= 0 {
			options.initErr = fmt.Errorf("group commit queue capacity must be positive")
			return
		}
		options.queueCapacity = value
	}
}

func WithQueueMaxBytes(value int64) Option {
	return func(options *groupCommitOptions) {
		if value <= 0 {
			options.initErr = fmt.Errorf("group commit queue bytes must be positive")
			return
		}
		options.queueMaxBytes = value
	}
}

func WithMaxEventBytes(value int) Option {
	return func(options *groupCommitOptions) {
		if value <= 0 {
			options.initErr = fmt.Errorf("group commit event bytes must be positive")
			return
		}
		options.maxEventBytes = value
	}
}

func WithMaxBatch(value int) Option {
	return func(options *groupCommitOptions) {
		if value < 1 || value > 500 {
			options.initErr = fmt.Errorf("group commit max batch must be in [1,500]")
			return
		}
		options.maxBatch = value
	}
}

func WithMaxWaiters(value int) Option {
	return func(options *groupCommitOptions) {
		if value <= 0 {
			options.initErr = fmt.Errorf("group commit max waiters must be positive")
			return
		}
		options.maxWaiters = value
	}
}

func WithLinger(value time.Duration) Option {
	return func(options *groupCommitOptions) { options.linger = value }
}

func WithEnqueueWaitTimeout(value time.Duration) Option {
	return func(options *groupCommitOptions) { options.enqueueWait = value }
}

func WithAttemptTimeout(value time.Duration) Option {
	return func(options *groupCommitOptions) { options.attemptTimeout = value }
}

func WithRootTimeout(value time.Duration) Option {
	return func(options *groupCommitOptions) { options.rootTimeout = value }
}

func WithDrainTimeout(value time.Duration) Option {
	return func(options *groupCommitOptions) { options.drainTimeout = value }
}

func WithCanonicalDigest(value func(model.AuditLog) (string, error)) Option {
	return func(options *groupCommitOptions) {
		if value == nil {
			options.initErr = fmt.Errorf("group commit canonical digest is required")
			return
		}
		options.canonicalDigest = value
	}
}

// WithFlusherExitedHook installs the ordering point after a chain flusher has
// completely stopped. An owner may close its backend only after Close returns
// (and therefore after this hook has run for every chain).
func WithFlusherExitedHook(value func(chainID string)) Option {
	return func(options *groupCommitOptions) { options.flusherExitedHook = value }
}

// SQLiteGroupCommitOptions returns the smaller S5 SQLite queue limits.
func SQLiteGroupCommitOptions() []Option {
	return []Option{WithQueueCapacity(1024), WithQueueMaxBytes(8 << 20)}
}

type groupCommitResult struct {
	row model.AuditLog
	err error
}

type groupCommitWaiter struct {
	result     chan groupCommitResult
	once       sync.Once
	reserved   int64
	admittedAt time.Time
}

func newGroupCommitWaiter(reserved int64, admittedAt time.Time) *groupCommitWaiter {
	return &groupCommitWaiter{
		result:     make(chan groupCommitResult, 1),
		reserved:   reserved,
		admittedAt: admittedAt,
	}
}

func (waiter *groupCommitWaiter) deliver(result groupCommitResult) {
	waiter.once.Do(func() {
		select {
		case waiter.result <- result:
		default:
		}
	})
}

type groupCommitEntity struct {
	key            string
	uuid           string
	digest         string
	log            model.AuditLog
	canonicalBytes int64
	sequence       uint64
	queuedAt       time.Time
	waiters        []*groupCommitWaiter
}

type admissionToken struct {
	id uint64
}

type capacityPool struct {
	mu       sync.Mutex
	count    int
	bytes    int64
	maxCount int
	maxBytes int64
	changed  chan struct{}
}

func newCapacityPool(maxCount int, maxBytes int64) *capacityPool {
	return &capacityPool{maxCount: maxCount, maxBytes: maxBytes, changed: make(chan struct{})}
}

func (pool *capacityPool) reserve(ctx context.Context, bytes int64, timeout time.Duration) error {
	if bytes > pool.maxBytes {
		return errCapacityUnavailable
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		pool.mu.Lock()
		if pool.count < pool.maxCount && pool.bytes+bytes <= pool.maxBytes {
			pool.count++
			pool.bytes += bytes
			pool.mu.Unlock()
			return nil
		}
		changed := pool.changed
		pool.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errCapacityUnavailable
		case <-changed:
		}
	}
}

func (pool *capacityPool) release(bytes int64) {
	pool.mu.Lock()
	pool.count--
	pool.bytes -= bytes
	if pool.count < 0 || pool.bytes < 0 {
		panic("audit group commit capacity accounting underflow")
	}
	close(pool.changed)
	pool.changed = make(chan struct{})
	pool.mu.Unlock()
}

func (pool *capacityPool) snapshot() (int, int64) {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	return pool.count, pool.bytes
}

var errCapacityUnavailable = errors.New("audit group commit capacity unavailable")

type groupCommitMetrics struct {
	batches           atomic.Uint64
	batchEvents       atomic.Uint64
	lastBatchSize     atomic.Int64
	transactionNanos  atomic.Uint64
	retries           atomic.Uint64
	ackNanos          atomic.Uint64
	acks              atomic.Uint64
	quarantined       atomic.Uint64
	closingNotStarted atomic.Uint64
	closingRolledBack atomic.Uint64
	closingCommitUnk  atomic.Uint64
	activeFlushers    atomic.Int64
}

// GroupCommitStats is an in-process snapshot. S5b can translate it to
// Prometheus without coupling this coordinator to a metrics implementation.
type GroupCommitStats struct {
	Batches                 uint64
	BatchEvents             uint64
	LastBatchSize           int64
	QueueDepth              int
	QueueBytes              int64
	OldestQueueAge          time.Duration
	TransactionTime         time.Duration
	Retries                 uint64
	Acknowledgements        uint64
	AcknowledgementTime     time.Duration
	Quarantined             uint64
	ClosingNotStarted       uint64
	ClosingRolledBack       uint64
	ClosingCommitUnknown    uint64
	AdmissionTokens         int
	Waiters                 int
	CapacityCount           int
	CapacityBytes           int64
	ActiveFlusherGoroutines int64
}

type chainCoordinator struct {
	sink       *GroupCommitSink
	chainID    string
	backendKey uintptr
	pool       *capacityPool
	wake       chan struct{}
	done       chan struct{}

	mu             sync.Mutex
	accepting      bool
	closing        bool
	drainDeadline  time.Time
	nextToken      uint64
	nextSequence   uint64
	admissions     map[uint64]admissionToken
	waiterCount    int
	queue          []*groupCommitEntity
	queuedBytes    int64
	inflight       map[string]*groupCommitEntity
	oldestQueuedAt time.Time
}

// GroupCommitSink multiplexes synchronous Insert calls onto one flusher per
// physical audit chain.
type GroupCommitSink struct {
	backend        BatchBackend
	resolveChainID func(model.AuditLog) string
	options        groupCommitOptions
	metrics        groupCommitMetrics

	mu           sync.Mutex
	accepting    bool
	closed       bool
	coordinators map[string]*chainCoordinator
	closeDone    chan struct{}
}

var coordinatorRegistry = struct {
	sync.Mutex
	entries map[registryKey]*chainCoordinator
}{entries: make(map[registryKey]*chainCoordinator)}

type registryKey struct {
	backend uintptr
	chainID string
}

// NewGroupCommitSink constructs an already-started, lazily sharded sink.
func NewGroupCommitSink(
	backend BatchBackend,
	resolveChainID func(model.AuditLog) string,
	optionValues ...Option,
) *GroupCommitSink {
	options := groupCommitOptions{
		queueCapacity:   defaultQueueCapacity,
		queueMaxBytes:   defaultQueueMaxBytes,
		maxEventBytes:   defaultMaxEventBytes,
		maxBatch:        defaultMaxBatch,
		maxWaiters:      defaultMaxWaiters,
		linger:          10 * time.Millisecond,
		enqueueWait:     100 * time.Millisecond,
		attemptTimeout:  2 * time.Second,
		rootTimeout:     5 * time.Second,
		drainTimeout:    30 * time.Second,
		canonicalDigest: defaultCanonicalDigest,
	}
	for _, apply := range optionValues {
		if apply != nil {
			apply(&options)
		}
	}
	return &GroupCommitSink{
		backend:        backend,
		resolveChainID: resolveChainID,
		options:        options,
		accepting:      true,
		coordinators:   make(map[string]*chainCoordinator),
		closeDone:      make(chan struct{}),
	}
}

// Start exists for lifecycle symmetry. Construction starts the sink, while
// this method reports configuration errors before the first Insert.
func (sink *GroupCommitSink) Start() error {
	if sink == nil {
		return fmt.Errorf("group commit sink is required")
	}
	return sink.options.initErr
}

func (sink *GroupCommitSink) Insert(ctx context.Context, log model.AuditLog) (model.AuditLog, error) {
	if sink == nil {
		return model.AuditLog{}, sinkError(model.DBErrorCodeAuditUnavailable, "audit_closing", true)
	}
	if ctx == nil {
		return model.AuditLog{}, fmt.Errorf("insert audit log: context is required")
	}
	if sink.options.initErr != nil {
		return model.AuditLog{}, sink.options.initErr
	}
	if isNilInterface(sink.backend) || sink.resolveChainID == nil {
		return model.AuditLog{}, sinkError(model.DBErrorCodeAuditUnavailable, "audit_backend_unavailable", true)
	}
	chainID := sink.resolveChainID(log)
	if chainID == "" {
		return model.AuditLog{}, sinkError(model.DBErrorCodeGatewayInternal, "audit_invariant_violation", false)
	}
	coordinator, err := sink.coordinator(chainID)
	if err != nil {
		return model.AuditLog{}, err
	}
	return coordinator.insert(ctx, log)
}

func (sink *GroupCommitSink) coordinator(chainID string) (*chainCoordinator, error) {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if !sink.accepting {
		return nil, sinkError(model.DBErrorCodeAuditUnavailable, "audit_closing", true)
	}
	if coordinator := sink.coordinators[chainID]; coordinator != nil {
		return coordinator, nil
	}
	backendKey := backendIdentity(sink.backend)
	key := registryKey{backend: backendKey, chainID: chainID}
	coordinatorRegistry.Lock()
	defer coordinatorRegistry.Unlock()
	if coordinatorRegistry.entries[key] != nil {
		return nil, sinkError(model.DBErrorCodeGatewayInternal, "duplicate_chain_coordinator", false)
	}
	coordinator := &chainCoordinator{
		sink:       sink,
		chainID:    chainID,
		backendKey: backendKey,
		pool:       newCapacityPool(sink.options.queueCapacity, sink.options.queueMaxBytes),
		wake:       make(chan struct{}, 1),
		done:       make(chan struct{}),
		accepting:  true,
		admissions: make(map[uint64]admissionToken),
		inflight:   make(map[string]*groupCommitEntity),
	}
	coordinatorRegistry.entries[key] = coordinator
	sink.coordinators[chainID] = coordinator
	sink.metrics.activeFlushers.Add(1)
	go func() {
		coordinator.run()
		coordinatorRegistry.Lock()
		delete(coordinatorRegistry.entries, registryKey{backend: coordinator.backendKey, chainID: coordinator.chainID})
		coordinatorRegistry.Unlock()
		sink.metrics.activeFlushers.Add(-1)
		close(coordinator.done)
	}()
	return coordinator, nil
}

func backendIdentity(backend BatchBackend) uintptr {
	value := reflect.ValueOf(backend)
	if value.IsValid() {
		switch value.Kind() {
		case reflect.Chan, reflect.Func, reflect.Map, reflect.Pointer, reflect.Slice, reflect.UnsafePointer:
			if !value.IsNil() {
				return value.Pointer()
			}
		}
	}
	// A non-pointer backend is copied into the interface and cannot reliably be
	// shared. Its interface address still keeps independent sinks independent.
	return reflect.ValueOf(&backend).Pointer()
}

func (coordinator *chainCoordinator) insert(ctx context.Context, input model.AuditLog) (model.AuditLog, error) {
	canonical, err := canonicalAuditBytes(input)
	if err != nil {
		return model.AuditLog{}, sinkError(model.DBErrorCodeGatewayInternal, "audit_canonicalization_failed", false)
	}
	if len(canonical) > coordinator.sink.options.maxEventBytes {
		return model.AuditLog{}, sinkError(model.DBErrorCodeGatewayInternal, "audit_event_too_large", false)
	}
	uuid := auditEventUUID(input)
	if uuid == "" {
		return model.AuditLog{}, sinkError(model.DBErrorCodeGatewayInternal, "audit_invariant_violation", false)
	}
	digest, tokenID, err := coordinator.beginAdmission(input, uuid)
	if err != nil {
		return model.AuditLog{}, err
	}
	reserved := int64(len(canonical) + defaultRequestOverhead)
	if err := coordinator.pool.reserve(ctx, reserved, coordinator.sink.options.enqueueWait); err != nil {
		coordinator.cancelAdmission(tokenID)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return model.AuditLog{}, err
		}
		return model.AuditLog{}, sinkError(model.DBErrorCodeAuditOverloaded, "audit_backpressure", true)
	}
	waiter := newGroupCommitWaiter(reserved, time.Now())
	if err := coordinator.finishAdmission(tokenID, uuid, digest, input, int64(len(canonical)), waiter); err != nil {
		coordinator.pool.release(reserved)
		return model.AuditLog{}, err
	}
	select {
	case result := <-waiter.result:
		return result.row, result.err
	case <-ctx.Done():
		return model.AuditLog{}, ctx.Err()
	}
}

func (coordinator *chainCoordinator) beginAdmission(log model.AuditLog, uuid string) (string, uint64, error) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if !coordinator.accepting {
		return "", 0, sinkError(model.DBErrorCodeAuditUnavailable, "audit_closing", true)
	}
	if coordinator.waiterCount >= coordinator.sink.options.maxWaiters {
		return "", 0, sinkError(model.DBErrorCodeAuditOverloaded, "audit_backpressure", true)
	}
	digest, err := coordinator.sink.options.canonicalDigest(log)
	if err != nil {
		return "", 0, sinkError(model.DBErrorCodeGatewayInternal, "audit_canonicalization_failed", false)
	}
	if entity := coordinator.inflight[uuid]; entity != nil && entity.digest != digest {
		return "", 0, sinkError(model.DBErrorCodeGatewayInternal, "audit_uuid_conflict", false)
	}
	coordinator.nextToken++
	tokenID := coordinator.nextToken
	coordinator.admissions[tokenID] = admissionToken{id: tokenID}
	coordinator.waiterCount++
	return digest, tokenID, nil
}

func (coordinator *chainCoordinator) cancelAdmission(tokenID uint64) {
	coordinator.mu.Lock()
	if _, exists := coordinator.admissions[tokenID]; exists {
		delete(coordinator.admissions, tokenID)
		coordinator.waiterCount--
	}
	coordinator.mu.Unlock()
	coordinator.signal()
}

func (coordinator *chainCoordinator) finishAdmission(
	tokenID uint64,
	uuid string,
	digest string,
	input model.AuditLog,
	canonicalBytes int64,
	waiter *groupCommitWaiter,
) error {
	coordinator.mu.Lock()
	if _, exists := coordinator.admissions[tokenID]; !exists {
		coordinator.mu.Unlock()
		return sinkError(model.DBErrorCodeGatewayInternal, "audit_admission_token_lost", false)
	}
	phaseTwoDigest, digestErr := coordinator.sink.options.canonicalDigest(input)
	if digestErr != nil {
		delete(coordinator.admissions, tokenID)
		coordinator.waiterCount--
		coordinator.mu.Unlock()
		coordinator.signal()
		return sinkError(model.DBErrorCodeGatewayInternal, "audit_canonicalization_failed", false)
	}
	if phaseTwoDigest != digest {
		delete(coordinator.admissions, tokenID)
		coordinator.waiterCount--
		coordinator.mu.Unlock()
		coordinator.signal()
		return sinkError(model.DBErrorCodeGatewayInternal, "audit_uuid_conflict", false)
	}
	entity := coordinator.inflight[uuid]
	if entity != nil && entity.digest != phaseTwoDigest {
		delete(coordinator.admissions, tokenID)
		coordinator.waiterCount--
		coordinator.mu.Unlock()
		coordinator.signal()
		return sinkError(model.DBErrorCodeGatewayInternal, "audit_uuid_conflict", false)
	}
	delete(coordinator.admissions, tokenID)
	if entity != nil {
		entity.waiters = append(entity.waiters, waiter)
		coordinator.mu.Unlock()
		coordinator.signal()
		return nil
	}
	now := time.Now()
	coordinator.nextSequence++
	entity = &groupCommitEntity{
		key:            uuid,
		uuid:           uuid,
		digest:         phaseTwoDigest,
		log:            cloneAuditLog(input),
		canonicalBytes: canonicalBytes,
		sequence:       coordinator.nextSequence,
		queuedAt:       now,
		waiters:        []*groupCommitWaiter{waiter},
	}
	wasEmpty := len(coordinator.queue) == 0
	coordinator.inflight[uuid] = entity
	coordinator.queue = append(coordinator.queue, entity)
	coordinator.queuedBytes += canonicalBytes + defaultRequestOverhead
	if wasEmpty {
		coordinator.oldestQueuedAt = now
	}
	coordinator.mu.Unlock()
	coordinator.signal()
	return nil
}

func (coordinator *chainCoordinator) signal() {
	select {
	case coordinator.wake <- struct{}{}:
	default:
	}
}

func (coordinator *chainCoordinator) run() {
	backlog := false
	for {
		batch, exit := coordinator.nextBatch(backlog)
		if exit {
			return
		}
		if len(batch) == 0 {
			continue
		}
		coordinator.mu.Lock()
		backlog = len(coordinator.queue) > 0
		coordinator.mu.Unlock()
		coordinator.flush(batch)
	}
}

func (coordinator *chainCoordinator) nextBatch(backlog bool) ([]*groupCommitEntity, bool) {
	for {
		coordinator.mu.Lock()
		queueLength := len(coordinator.queue)
		closing := coordinator.closing
		admissions := len(coordinator.admissions)
		deadline := coordinator.drainDeadline
		if closing && !deadline.IsZero() && !time.Now().Before(deadline) && queueLength > 0 {
			batch := coordinator.takeLocked(queueLength)
			coordinator.mu.Unlock()
			result := groupCommitResult{err: sinkError(model.DBErrorCodeAuditUnavailable, "audit_closing", true)}
			for _, entity := range batch {
				coordinator.finishEntity(entity, result, shutdownNotStarted)
			}
			continue
		}
		if queueLength > 0 && (closing || backlog || queueLength >= coordinator.sink.options.maxBatch) {
			batch := coordinator.takeLocked(coordinator.sink.options.maxBatch)
			coordinator.mu.Unlock()
			return batch, false
		}
		if queueLength > 0 {
			remaining := time.Until(coordinator.oldestQueuedAt.Add(coordinator.sink.options.linger))
			if remaining <= 0 {
				batch := coordinator.takeLocked(coordinator.sink.options.maxBatch)
				coordinator.mu.Unlock()
				return batch, false
			}
			coordinator.mu.Unlock()
			timer := time.NewTimer(remaining)
			select {
			case <-timer.C:
			case <-coordinator.wake:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
			}
			continue
		}
		if closing && admissions == 0 {
			coordinator.mu.Unlock()
			return nil, true
		}
		coordinator.mu.Unlock()
		<-coordinator.wake
	}
}

func (coordinator *chainCoordinator) takeLocked(limit int) []*groupCommitEntity {
	if limit > len(coordinator.queue) {
		limit = len(coordinator.queue)
	}
	batch := append([]*groupCommitEntity(nil), coordinator.queue[:limit]...)
	for _, entity := range batch {
		coordinator.queuedBytes -= entity.canonicalBytes + defaultRequestOverhead
	}
	copy(coordinator.queue, coordinator.queue[limit:])
	coordinator.queue = coordinator.queue[:len(coordinator.queue)-limit]
	if len(coordinator.queue) == 0 {
		coordinator.oldestQueuedAt = time.Time{}
	} else {
		coordinator.oldestQueuedAt = coordinator.queue[0].queuedAt
	}
	return batch
}

type shutdownOutcome uint8

const (
	shutdownNone shutdownOutcome = iota
	shutdownNotStarted
	shutdownRolledBack
	shutdownCommitUnknown
)

type rootJob struct {
	coordinator *chainCoordinator
	deadline    time.Time
	calls       int
	outcomes    map[*groupCommitEntity]entityOutcome
	attempted   map[*groupCommitEntity]bool
	closing     bool
	fatalStatus shutdownOutcome
}

type entityOutcome struct {
	result   groupCommitResult
	shutdown shutdownOutcome
}

var errRootBudget = errors.New("audit group commit root budget exhausted")

func (coordinator *chainCoordinator) flush(batch []*groupCommitEntity) {
	coordinator.sink.metrics.batches.Add(1)
	coordinator.sink.metrics.batchEvents.Add(uint64(len(batch)))
	coordinator.sink.metrics.lastBatchSize.Store(int64(len(batch)))
	now := time.Now()
	deadline := now.Add(coordinator.sink.options.rootTimeout)
	coordinator.mu.Lock()
	closing := coordinator.closing
	if closing && !coordinator.drainDeadline.IsZero() && coordinator.drainDeadline.Before(deadline) {
		deadline = coordinator.drainDeadline
	}
	coordinator.mu.Unlock()
	job := &rootJob{
		coordinator: coordinator,
		deadline:    deadline,
		outcomes:    make(map[*groupCommitEntity]entityOutcome, len(batch)),
		attempted:   make(map[*groupCommitEntity]bool, len(batch)),
		closing:     closing,
	}
	fatal := job.process(batch, 0)
	// Close may begin while a backend attempt is already in flight. Outcome
	// accounting and the error used for untouched work must observe that
	// transition even when this root job started during normal operation.
	job.closing = coordinator.closingSnapshot()
	for _, entity := range batch {
		outcome, done := job.outcomes[entity]
		if !done {
			if fatal != nil && !errors.Is(fatal, errRootBudget) {
				outcome = entityOutcome{result: groupCommitResult{err: fatal}, shutdown: job.fatalStatus}
			} else if job.closing {
				status := shutdownNotStarted
				if job.attempted[entity] {
					status = shutdownRolledBack
				}
				outcome = entityOutcome{
					result:   groupCommitResult{err: sinkError(model.DBErrorCodeAuditUnavailable, "audit_closing", true)},
					shutdown: status,
				}
			} else {
				outcome = entityOutcome{
					result: groupCommitResult{err: sinkError(model.DBErrorCodeAuditOverloaded, "audit_backpressure", true)},
				}
			}
		}
		coordinator.finishEntity(entity, outcome.result, outcome.shutdown)
	}
}

func (job *rootJob) process(batch []*groupCommitEntity, depth int) error {
	if len(batch) == 0 {
		return nil
	}
	var lastErr error
	for retry := 0; retry <= 5; retry++ {
		effectiveDeadline := job.effectiveDeadline()
		if job.calls >= 31 || !time.Now().Before(effectiveDeadline) {
			return errRootBudget
		}
		job.calls++
		for _, entity := range batch {
			job.attempted[entity] = true
		}
		logs := make([]model.AuditLog, len(batch))
		for index, entity := range batch {
			logs[index] = entity.log
		}
		attemptDeadline := time.Now().Add(job.coordinator.sink.options.attemptTimeout)
		if effectiveDeadline.Before(attemptDeadline) {
			attemptDeadline = effectiveDeadline
		}
		ctx, cancel := context.WithDeadline(context.Background(), attemptDeadline)
		started := time.Now()
		rows, err := job.coordinator.sink.backend.InsertBatch(ctx, logs)
		cancel()
		job.coordinator.sink.metrics.transactionNanos.Add(uint64(time.Since(started)))
		if err == nil {
			mapped, mapErr := mapBatchRows(batch, rows)
			if mapErr != nil {
				unknown := sinkError(model.DBErrorCodeAuditUnavailable, "commit_unknown", false)
				for _, entity := range batch {
					job.outcomes[entity] = entityOutcome{result: groupCommitResult{err: unknown}, shutdown: shutdownCommitUnknown}
				}
				return nil
			}
			for entity, row := range mapped {
				job.outcomes[entity] = entityOutcome{result: groupCommitResult{row: row}}
			}
			return nil
		}
		lastErr = err
		kind, retryable, explicit := classifyBatchFailure(err)
		if kind == BatchFailureCommitUnknown || (!explicit && errors.Is(ctx.Err(), context.DeadlineExceeded)) {
			unknown := sinkError(model.DBErrorCodeAuditUnavailable, "commit_unknown", false)
			for _, entity := range batch {
				job.outcomes[entity] = entityOutcome{result: groupCommitResult{err: unknown}, shutdown: shutdownCommitUnknown}
			}
			return nil
		}
		if retryable && retry < 5 && (kind == BatchFailureTransient || kind == BatchFailureGlobal) {
			job.coordinator.sink.metrics.retries.Add(1)
			continue
		}
		switch kind {
		case BatchFailureDeterministic:
			if len(batch) == 1 || depth >= 4 {
				failure := sinkError(model.DBErrorCodeGatewayInternal, "audit_event_rejected", false)
				job.coordinator.sink.metrics.quarantined.Add(uint64(len(batch)))
				for _, entity := range batch {
					job.outcomes[entity] = entityOutcome{result: groupCommitResult{err: failure}, shutdown: shutdownRolledBack}
				}
				return nil
			}
			middle := (len(batch) + 1) / 2
			if err := job.process(batch[:middle], depth+1); err != nil {
				return err
			}
			return job.process(batch[middle:], depth+1)
		case BatchFailureTransient:
			return errRootBudget
		default:
			job.fatalStatus = shutdownNotStarted
			return sinkError(model.DBErrorCodeAuditUnavailable, "audit_backend_unavailable", true)
		}
	}
	return lastErr
}

func (job *rootJob) effectiveDeadline() time.Time {
	deadline := job.deadline
	job.coordinator.mu.Lock()
	if job.coordinator.closing &&
		!job.coordinator.drainDeadline.IsZero() &&
		job.coordinator.drainDeadline.Before(deadline) {
		deadline = job.coordinator.drainDeadline
	}
	job.coordinator.mu.Unlock()
	return deadline
}

func classifyBatchFailure(err error) (BatchFailureKind, bool, bool) {
	var batchError *BatchError
	if errors.As(err, &batchError) {
		return batchError.Kind, batchError.Retryable, true
	}
	return BatchFailureGlobal, false, false
}

func mapBatchRows(batch []*groupCommitEntity, rows []model.AuditLog) (map[*groupCommitEntity]model.AuditLog, error) {
	if len(rows) != len(batch) {
		return nil, fmt.Errorf("backend returned %d rows for %d events", len(rows), len(batch))
	}
	byUUID := make(map[string]model.AuditLog, len(rows))
	for _, row := range rows {
		uuid := auditEventUUID(row)
		if uuid == "" {
			return nil, fmt.Errorf("backend row has no event UUID")
		}
		if _, duplicate := byUUID[uuid]; duplicate {
			return nil, fmt.Errorf("backend returned duplicate event UUID")
		}
		byUUID[uuid] = row
	}
	mapped := make(map[*groupCommitEntity]model.AuditLog, len(batch))
	for _, entity := range batch {
		row, exists := byUUID[entity.uuid]
		if !exists {
			return nil, fmt.Errorf("backend omitted event UUID")
		}
		mapped[entity] = row
	}
	return mapped, nil
}

func (coordinator *chainCoordinator) finishEntity(
	entity *groupCommitEntity,
	result groupCommitResult,
	shutdown shutdownOutcome,
) {
	coordinator.mu.Lock()
	if current := coordinator.inflight[entity.key]; current == entity {
		delete(coordinator.inflight, entity.key)
	}
	waiters := append([]*groupCommitWaiter(nil), entity.waiters...)
	coordinator.waiterCount -= len(waiters)
	coordinator.mu.Unlock()
	for _, waiter := range waiters {
		waiterResult := result
		waiterResult.row = cloneAuditLog(result.row)
		waiter.deliver(waiterResult)
		coordinator.pool.release(waiter.reserved)
		coordinator.sink.metrics.acks.Add(1)
		coordinator.sink.metrics.ackNanos.Add(uint64(time.Since(waiter.admittedAt)))
	}
	if coordinator.closingSnapshot() {
		switch shutdown {
		case shutdownNotStarted:
			coordinator.sink.metrics.closingNotStarted.Add(uint64(len(waiters)))
		case shutdownRolledBack:
			coordinator.sink.metrics.closingRolledBack.Add(uint64(len(waiters)))
		case shutdownCommitUnknown:
			coordinator.sink.metrics.closingCommitUnk.Add(uint64(len(waiters)))
		}
	}
	coordinator.signal()
}

func (coordinator *chainCoordinator) closingSnapshot() bool {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	return coordinator.closing
}

// Close stops admission, immediately flushes queued work, and waits for every
// chain flusher to leave. It never closes the backend itself.
func (sink *GroupCommitSink) Close() error {
	if sink == nil {
		return nil
	}
	sink.mu.Lock()
	if sink.closed {
		done := sink.closeDone
		sink.mu.Unlock()
		<-done
		return nil
	}
	sink.closed = true
	sink.accepting = false
	coordinators := make([]*chainCoordinator, 0, len(sink.coordinators))
	deadline := time.Now().Add(sink.options.drainTimeout)
	for _, coordinator := range sink.coordinators {
		coordinators = append(coordinators, coordinator)
		coordinator.mu.Lock()
		coordinator.accepting = false
		coordinator.closing = true
		coordinator.drainDeadline = deadline
		coordinator.mu.Unlock()
		coordinator.signal()
	}
	sink.mu.Unlock()
	for _, coordinator := range coordinators {
		<-coordinator.done
	}
	if hook := sink.options.flusherExitedHook; hook != nil {
		for _, coordinator := range coordinators {
			hook(coordinator.chainID)
		}
	}
	close(sink.closeDone)
	return nil
}

// Stats returns an aggregate point-in-time snapshot across all physical
// chains owned by this sink.
func (sink *GroupCommitSink) Stats() GroupCommitStats {
	if sink == nil {
		return GroupCommitStats{}
	}
	stats := GroupCommitStats{
		Batches:                 sink.metrics.batches.Load(),
		BatchEvents:             sink.metrics.batchEvents.Load(),
		LastBatchSize:           sink.metrics.lastBatchSize.Load(),
		TransactionTime:         time.Duration(sink.metrics.transactionNanos.Load()),
		Retries:                 sink.metrics.retries.Load(),
		Acknowledgements:        sink.metrics.acks.Load(),
		AcknowledgementTime:     time.Duration(sink.metrics.ackNanos.Load()),
		Quarantined:             sink.metrics.quarantined.Load(),
		ClosingNotStarted:       sink.metrics.closingNotStarted.Load(),
		ClosingRolledBack:       sink.metrics.closingRolledBack.Load(),
		ClosingCommitUnknown:    sink.metrics.closingCommitUnk.Load(),
		ActiveFlusherGoroutines: sink.metrics.activeFlushers.Load(),
	}
	sink.mu.Lock()
	coordinators := make([]*chainCoordinator, 0, len(sink.coordinators))
	for _, coordinator := range sink.coordinators {
		coordinators = append(coordinators, coordinator)
	}
	sink.mu.Unlock()
	now := time.Now()
	for _, coordinator := range coordinators {
		coordinator.mu.Lock()
		stats.QueueDepth += len(coordinator.queue)
		stats.QueueBytes += coordinator.queuedBytes
		stats.AdmissionTokens += len(coordinator.admissions)
		stats.Waiters += coordinator.waiterCount
		if !coordinator.oldestQueuedAt.IsZero() {
			age := now.Sub(coordinator.oldestQueuedAt)
			if age > stats.OldestQueueAge {
				stats.OldestQueueAge = age
			}
		}
		coordinator.mu.Unlock()
		count, bytes := coordinator.pool.snapshot()
		stats.CapacityCount += count
		stats.CapacityBytes += bytes
	}
	return stats
}

func defaultCanonicalDigest(log model.AuditLog) (string, error) {
	canonical, err := canonicalAuditBytes(log)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

func canonicalAuditBytes(log model.AuditLog) ([]byte, error) {
	// AuditLog is a field-ordered struct, so encoding/json provides a stable
	// representation while preserving nil versus present-zero pointer fields.
	return json.Marshal(log)
}

func auditEventUUID(log model.AuditLog) string {
	if log.EventUUID == nil {
		return ""
	}
	return *log.EventUUID
}

func cloneAuditLog(log model.AuditLog) model.AuditLog {
	cloneString := func(value *string) *string {
		if value == nil {
			return nil
		}
		copyValue := strings.Clone(*value)
		return &copyValue
	}
	cloneInt := func(value *int) *int {
		if value == nil {
			return nil
		}
		copyValue := *value
		return &copyValue
	}
	cloneInt64 := func(value *int64) *int64 {
		if value == nil {
			return nil
		}
		copyValue := *value
		return &copyValue
	}
	log.AgentID = cloneString(log.AgentID)
	log.DatasourceID = cloneString(log.DatasourceID)
	log.SessionID = cloneString(log.SessionID)
	log.ConversationID = cloneString(log.ConversationID)
	log.MCPTool = cloneString(log.MCPTool)
	log.DBType = cloneString(log.DBType)
	log.SQLRaw = cloneString(log.SQLRaw)
	log.SQLNorm = cloneString(log.SQLNorm)
	log.StmtType = cloneString(log.StmtType)
	log.Objects = cloneString(log.Objects)
	log.RuleHits = cloneString(log.RuleHits)
	log.RiskLevel = cloneInt(log.RiskLevel)
	log.EstRows = cloneInt64(log.EstRows)
	log.RowsReturned = cloneInt(log.RowsReturned)
	log.LatencyMS = cloneInt64(log.LatencyMS)
	log.ClientIP = cloneString(log.ClientIP)
	log.ModelName = cloneString(log.ModelName)
	log.ErrorMsg = cloneString(log.ErrorMsg)
	log.ErrorCode = cloneString(log.ErrorCode)
	log.Action = cloneString(log.Action)
	log.ActorType = cloneString(log.ActorType)
	log.ActorID = cloneString(log.ActorID)
	log.DetailsJSON = cloneString(log.DetailsJSON)
	log.EventUUID = cloneString(log.EventUUID)
	return log
}

var _ Sink = (*GroupCommitSink)(nil)
