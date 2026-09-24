package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	containerapi "github.com/docker/docker/api/types/container"
	"github.com/testcontainers/testcontainers-go"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/cuipengdba/agentsql/internal/audit"
	"github.com/cuipengdba/agentsql/internal/model"
)

const (
	chainLoadGate = "AGENTSQL_CHAIN_LOAD"

	chainLoadPGImage       = "postgres:18"
	chainLoadCPUNano       = int64(2_000_000_000)
	chainLoadMemoryBytes   = int64(2 * 1024 * 1024 * 1024)
	chainLoadP95Limit      = 250 * time.Millisecond
	chainLoadP99Limit      = time.Second
	chainLoadLockP95Target = 100 * time.Millisecond
	chainLoadLockP99Target = 250 * time.Millisecond
)

type chainLoadConfig struct {
	sustainSeconds int
	sustainRate    int
	burstSeconds   int
	burstRate      int
	sqliteSeconds  int
	sqliteRate     int
	backfillRows   int
	sustainWorkers int
	burstWorkers   int
	sqliteWorkers  int
	baselineRows   int
	lockSample     time.Duration
}

type chainLoadResources struct {
	Image           string `json:"image"`
	RequestedCPUs   int64  `json:"requested_cpus"`
	RequestedMemory int64  `json:"requested_memory_bytes"`
	ActualCPUNano   int64  `json:"actual_cpu_nano"`
	ActualMemory    int64  `json:"actual_memory_bytes"`
	LimitsApplied   bool   `json:"limits_applied"`
	UnlimitedReason string `json:"unlimited_reason,omitempty"`
	HostGOMAXPROCS  int    `json:"host_gomaxprocs"`
	Testcontainers  string `json:"testcontainers"`
}

type chainLoadResult struct {
	Scenario              string  `json:"scenario"`
	Passed                bool    `json:"passed"`
	Advisory              bool    `json:"advisory_only,omitempty"`
	TargetRate            float64 `json:"target_events_per_second,omitempty"`
	OfferedEvents         int64   `json:"offered_events,omitempty"`
	CompletedEvents       int64   `json:"completed_events,omitempty"`
	Errors                int64   `json:"errors"`
	OfferedWindowSeconds  float64 `json:"offered_window_seconds,omitempty"`
	WallSeconds           float64 `json:"wall_seconds,omitempty"`
	AchievedRate          float64 `json:"achieved_events_per_second,omitempty"`
	WallRate              float64 `json:"wall_events_per_second,omitempty"`
	RateMeasurement       string  `json:"rate_measurement,omitempty"`
	RateThresholdPassed   *bool   `json:"rate_threshold_passed,omitempty"`
	LatencyP50MS          float64 `json:"append_latency_p50_ms,omitempty"`
	LatencyP95MS          float64 `json:"append_latency_p95_ms,omitempty"`
	LatencyP99MS          float64 `json:"append_latency_p99_ms,omitempty"`
	LatencyP95Passed      *bool   `json:"append_latency_p95_passed,omitempty"`
	LatencyP99Passed      *bool   `json:"append_latency_p99_passed,omitempty"`
	BodyMedianBytes       int     `json:"event_body_median_bytes,omitempty"`
	BodyP95Bytes          int     `json:"event_body_p95_bytes,omitempty"`
	BodyDistributionPass  bool    `json:"event_body_distribution_passed"`
	ChainValid            bool    `json:"chain_valid"`
	ChainContiguous       bool    `json:"chain_seq_contiguous_unique"`
	QueueDrained          bool    `json:"queue_drained_after_load"`
	VerificationResult    string  `json:"verification_result,omitempty"`
	BackfillRows          int64   `json:"backfill_rows,omitempty"`
	BackfillSeconds       float64 `json:"backfill_seconds,omitempty"`
	BackfillRowsPerSecond float64 `json:"backfill_rows_per_second,omitempty"`
	LockSamples           int     `json:"approx_lock_wait_samples"`
	LockSampleErrors      int     `json:"approx_lock_sample_errors"`
	LockWaitP95MS         float64 `json:"approx_lock_wait_p95_ms"`
	LockWaitP99MS         float64 `json:"approx_lock_wait_p99_ms"`
	LockP95ReferencePass  *bool   `json:"approx_lock_p95_reference_passed,omitempty"`
	LockP99ReferencePass  *bool   `json:"approx_lock_p99_reference_passed,omitempty"`
	BaselineP95MS         float64 `json:"uncontended_insert_p95_ms"`
	ContentionDeltaP95MS  float64 `json:"contention_delta_p95_ms"`
	LockObservationNote   string  `json:"lock_observation_note,omitempty"`
	Failure               string  `json:"failure,omitempty"`
}

type chainLoadRun struct {
	offered        int64
	completed      int64
	errors         int64
	duration       time.Duration
	wall           time.Duration
	steadyRate     float64
	latencies      []time.Duration
	bodySizes      []int
	firstErrorText string
}

type chainLoadLockObservation struct {
	waits  []time.Duration
	errors int
}

type chainLoadManifest struct{}

func (chainLoadManifest) ExpectedMode(context.Context) (string, error) { return "keyless", nil }
func (chainLoadManifest) CurrentKeyVersion(context.Context) (int, error) {
	return 0, nil
}
func (chainLoadManifest) ChainKeyForVersion(context.Context, int) ([]byte, error) {
	return nil, errors.New("keyless load-test manifest has no HMAC keys")
}

type chainLoadBatchBackend struct {
	repository *AuditLogRepository
}

func (backend *chainLoadBatchBackend) InsertBatch(
	ctx context.Context,
	batch []model.AuditLog,
) ([]model.AuditLog, error) {
	return backend.repository.AppendBatch(ctx, batch)
}

func newChainLoadGroupSink(t *testing.T, repository *AuditLogRepository, dialect Dialect) *audit.GroupCommitSink {
	t.Helper()
	options := []audit.Option(nil)
	if dialect == DialectSQLite {
		options = audit.SQLiteGroupCommitOptions()
	}
	sink := audit.NewGroupCommitSink(
		&chainLoadBatchBackend{repository: repository},
		func(model.AuditLog) string { return repository.chainID },
		options...,
	)
	if err := sink.Start(); err != nil {
		t.Fatalf("start chain load group commit sink: %v", err)
	}
	return sink
}

func waitForChainLoadQueueDrain(sink *audit.GroupCommitSink) bool {
	deadline := time.Now().Add(2 * time.Second)
	for {
		stats := sink.Stats()
		if stats.QueueDepth == 0 && stats.QueueBytes == 0 && stats.AdmissionTokens == 0 && stats.Waiters == 0 {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

var _ audit.BatchBackend = (*chainLoadBatchBackend)(nil)

// TestChainLoadBaseline is deliberately a Test (not a Benchmark) so that it is
// both easy to invoke explicitly and impossible to trigger through -bench.
func TestChainLoadBaseline(t *testing.T) {
	if os.Getenv(chainLoadGate) != "1" {
		t.Skip("set AGENTSQL_CHAIN_LOAD=1 to run the write-mode chain capacity baseline")
	}
	if testing.Short() {
		t.Skip("chain capacity baseline requires PostgreSQL and is disabled by -short")
	}

	cfg := readChainLoadConfig(t)
	fixture, err := loadChainLoadFixture()
	if err != nil {
		t.Fatalf("load deterministic chain fixture: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	logChainLoadJSON(t, "config", struct {
		SustainSeconds int `json:"sustain_seconds"`
		SustainRate    int `json:"sustain_events_per_second"`
		BurstSeconds   int `json:"burst_seconds"`
		BurstRate      int `json:"burst_events_per_second"`
		SQLiteSeconds  int `json:"sqlite_seconds"`
		SQLiteRate     int `json:"sqlite_events_per_second"`
		BackfillRows   int `json:"backfill_rows"`
		SustainWorkers int `json:"sustain_workers"`
		BurstWorkers   int `json:"burst_workers"`
		SQLiteWorkers  int `json:"sqlite_workers"`
		BaselineRows   int `json:"uncontended_baseline_rows"`
		LockSampleMS   int `json:"lock_sample_ms"`
	}{
		SustainSeconds: cfg.sustainSeconds, SustainRate: cfg.sustainRate,
		BurstSeconds: cfg.burstSeconds, BurstRate: cfg.burstRate,
		SQLiteSeconds: cfg.sqliteSeconds, SQLiteRate: cfg.sqliteRate,
		BackfillRows: cfg.backfillRows, SustainWorkers: cfg.sustainWorkers,
		BurstWorkers:  cfg.burstWorkers,
		SQLiteWorkers: cfg.sqliteWorkers, BaselineRows: cfg.baselineRows,
		LockSampleMS: int(cfg.lockSample / time.Millisecond),
	})

	opened, resources := openChainLoadPostgres(t)
	logChainLoadJSON(t, "resources", resources)
	if resources.LimitsApplied {
		t.Logf("CHAIN LOAD resources: image=%s CPU=%.2f memory=%dMiB GOMAXPROCS=%d",
			resources.Image, float64(resources.ActualCPUNano)/1e9,
			resources.ActualMemory/(1024*1024), resources.HostGOMAXPROCS)
	} else {
		t.Logf("CHAIN LOAD resources: image=%s CPU/memory UNLIMITED; results are optimistic (%s); GOMAXPROCS=%d",
			resources.Image, resources.UnlimitedReason, resources.HostGOMAXPROCS)
	}

	manifest := chainLoadManifest{}
	backfill := runChainLoadBackfill(t, ctx, opened, manifest, fixture, cfg.backfillRows)
	if !backfill.Passed {
		printChainLoadResults(t, []chainLoadResult{backfill})
		t.FailNow()
	}

	pgSink := newChainLoadGroupSink(t, opened.AuditLogs(), DialectPostgres)
	defer func() {
		if err := pgSink.Close(); err != nil {
			t.Errorf("close PostgreSQL group commit sink: %v", err)
		}
	}()
	baseline, baselineErr := measureUncontendedChainInserts(ctx, pgSink, fixture, cfg.baselineRows)
	if baselineErr != nil {
		backfill.Passed = false
		backfill.Failure = "uncontended baseline: " + baselineErr.Error()
		printChainLoadResults(t, []chainLoadResult{backfill})
		t.FailNow()
	}

	lockStop := make(chan struct{})
	lockDone := make(chan chainLoadLockObservation, 1)
	go samplePostgresChainLockWaits(ctx, opened.metaDB, cfg.lockSample, lockStop, lockDone)
	sustainedRun := runOpenLoopChainLoad(
		ctx, pgSink, fixture, "pg-sustained", cfg.sustainRate,
		time.Duration(cfg.sustainSeconds)*time.Second, cfg.sustainWorkers,
	)
	sustainedQueueDrained := waitForChainLoadQueueDrain(pgSink)
	close(lockStop)
	lockObservation := <-lockDone
	sustained := evaluateChainLoadRun(
		ctx, opened.metaDB, DialectPostgres, manifest, "pg_sustained", sustainedRun,
		float64(cfg.sustainRate), true, true, sustainedQueueDrained,
	)
	lockResult := evaluateLockObservation(
		lockObservation, percentileDuration(baseline, 0.95),
		time.Duration(sustained.LatencyP95MS*float64(time.Millisecond)),
		sustained.ChainValid, sustained.ChainContiguous,
	)

	burstRun := runOpenLoopChainLoad(
		ctx, pgSink, fixture, "pg-burst", cfg.burstRate,
		time.Duration(cfg.burstSeconds)*time.Second, cfg.burstWorkers,
	)
	burstQueueDrained := waitForChainLoadQueueDrain(pgSink)
	burst := evaluateChainLoadRun(
		ctx, opened.metaDB, DialectPostgres, manifest, "pg_burst", burstRun,
		float64(cfg.burstRate), false, true, burstQueueDrained,
	)

	sqlite := runSQLiteChainLoad(t, ctx, manifest, fixture, cfg)
	results := []chainLoadResult{sustained, burst, sqlite, backfill, lockResult}
	printChainLoadResults(t, results)

	allPassed := true
	for _, result := range results {
		if !result.Passed && !result.Advisory {
			allPassed = false
		}
	}
	summary := struct {
		Passed    bool `json:"passed"`
		Scenarios int  `json:"scenarios"`
	}{Passed: allPassed, Scenarios: len(results)}
	logChainLoadJSON(t, "summary", summary)
	if !allPassed {
		t.Fail()
	}
}

func readChainLoadConfig(t *testing.T) chainLoadConfig {
	t.Helper()
	return chainLoadConfig{
		sustainSeconds: chainLoadPositiveEnv(t, "AGENTSQL_LOAD_SUSTAIN_SECONDS", 30*60),
		sustainRate:    chainLoadPositiveEnv(t, "AGENTSQL_LOAD_SUSTAIN_RATE", 2_000),
		burstSeconds:   chainLoadPositiveEnv(t, "AGENTSQL_LOAD_BURST_SECONDS", 60),
		burstRate:      chainLoadPositiveEnv(t, "AGENTSQL_LOAD_BURST_RATE", 5_000),
		sqliteSeconds:  chainLoadPositiveEnv(t, "AGENTSQL_LOAD_SQLITE_SECONDS", 60),
		sqliteRate:     chainLoadPositiveEnv(t, "AGENTSQL_LOAD_SQLITE_RATE", 500),
		backfillRows:   chainLoadPositiveEnv(t, "AGENTSQL_LOAD_BACKFILL_ROWS", 200_000),
		sustainWorkers: chainLoadPositiveEnv(t, "AGENTSQL_LOAD_SUSTAIN_WORKERS", 128),
		burstWorkers:   chainLoadPositiveEnv(t, "AGENTSQL_LOAD_BURST_WORKERS", 256),
		sqliteWorkers:  chainLoadPositiveEnv(t, "AGENTSQL_LOAD_SQLITE_WORKERS", 64),
		baselineRows:   chainLoadPositiveEnv(t, "AGENTSQL_LOAD_BASELINE_ROWS", 100),
		lockSample: time.Duration(chainLoadPositiveEnv(
			t, "AGENTSQL_LOAD_LOCK_SAMPLE_MS", 10,
		)) * time.Millisecond,
	}
}

func chainLoadPositiveEnv(t *testing.T, name string, fallback int) int {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		t.Fatalf("%s must be a positive integer, got %q", name, raw)
	}
	return value
}

func openChainLoadPostgres(t *testing.T) (*Store, chainLoadResources) {
	t.Helper()
	startupCtx := dockerTestContext(t)
	const (
		databaseName = "agentsql"
		username     = "agentsql"
		password     = "agentsql-password"
	)
	resources := chainLoadResources{
		Image:           chainLoadPGImage,
		RequestedCPUs:   2,
		RequestedMemory: chainLoadMemoryBytes,
		HostGOMAXPROCS:  runtime.GOMAXPROCS(0),
		Testcontainers:  "v0.37.0",
	}
	common := []testcontainers.ContainerCustomizer{
		postgrescontainer.WithDatabase(databaseName),
		postgrescontainer.WithUsername(username),
		postgrescontainer.WithPassword(password),
		postgrescontainer.BasicWaitStrategies(),
	}
	limited := append(common, testcontainers.WithHostConfigModifier(func(host *containerapi.HostConfig) {
		host.Resources.NanoCPUs = chainLoadCPUNano
		host.Resources.Memory = chainLoadMemoryBytes
	}))
	container, limitedErr := postgrescontainer.Run(startupCtx, chainLoadPGImage, limited...)
	if limitedErr != nil {
		if container != nil {
			testcontainers.CleanupContainer(t, container)
		}
		resources.UnlimitedReason = "2C2G container start failed: " + limitedErr.Error()
		container, limitedErr = postgrescontainer.Run(startupCtx, chainLoadPGImage, common...)
	}
	if limitedErr != nil {
		if container != nil {
			testcontainers.CleanupContainer(t, container)
		}
		t.Fatalf("start %s load-test container: %v", chainLoadPGImage, limitedErr)
	}
	testcontainers.CleanupContainer(t, container)

	inspect, err := container.Inspect(startupCtx)
	if err != nil {
		if resources.UnlimitedReason == "" {
			resources.UnlimitedReason = "container inspect failed: " + err.Error()
		}
	} else if inspect.HostConfig == nil {
		if resources.UnlimitedReason == "" {
			resources.UnlimitedReason = "container inspect returned no HostConfig"
		}
	} else {
		resources.ActualCPUNano = inspect.HostConfig.Resources.NanoCPUs
		resources.ActualMemory = inspect.HostConfig.Resources.Memory
		resources.LimitsApplied = resources.ActualCPUNano == chainLoadCPUNano &&
			resources.ActualMemory == chainLoadMemoryBytes
		if !resources.LimitsApplied && resources.UnlimitedReason == "" {
			resources.UnlimitedReason = fmt.Sprintf(
				"daemon reported NanoCPUs=%d Memory=%d", resources.ActualCPUNano, resources.ActualMemory,
			)
		}
	}

	host, err := container.Host(startupCtx)
	if err != nil {
		t.Fatalf("read postgres container host: %v", err)
	}
	port, err := container.MappedPort(startupCtx, "5432/tcp")
	if err != nil {
		t.Fatalf("read postgres container port: %v", err)
	}
	dsn := fmt.Sprintf(
		"postgres://%s:%s@%s:%s/%s?sslmode=disable",
		username, password, host, port.Port(), databaseName,
	)
	opened, err := OpenMetadata(context.Background(), MetadataOptions{
		Driver:          DialectPostgres,
		PostgresDSN:     dsn,
		MaxOpenConns:    64,
		MaxIdleConns:    64,
		ConnMaxLifetime: 30 * time.Minute,
		AutoMigrate:     true,
	}, []byte(testSecret))
	if err != nil {
		t.Fatalf("open postgres load-test store: %v", err)
	}
	t.Cleanup(func() {
		if err := opened.Close(); err != nil {
			t.Errorf("close postgres load-test store: %v", err)
		}
	})
	return opened, resources
}

func runChainLoadBackfill(
	t *testing.T,
	ctx context.Context,
	opened *Store,
	manifest ChainManifest,
	fixture chainLoadFixture,
	rows int,
) chainLoadResult {
	t.Helper()
	result := chainLoadResult{Scenario: "pg_backfill", BackfillRows: int64(rows)}
	bodySizes, err := seedBareChainLoadRows(ctx, opened.metaDB, fixture, rows)
	if err != nil {
		result.Failure = "seed bare historical rows: " + err.Error()
		return result
	}
	result.BodyMedianBytes = percentileInt(bodySizes, 0.50)
	result.BodyP95Bytes = percentileInt(bodySizes, 0.95)
	result.BodyDistributionPass = result.BodyMedianBytes <= 2*1024 && result.BodyP95Bytes <= 8*1024

	const owner = "chain-load-backfill"
	buildRepository := &ChainBuildRepository{repositoryBase: repositoryBase{
		db: opened.metaDB, dialect: DialectPostgres,
	}}
	lease, err := buildRepository.BeginBuild(ctx, "management", BuildRequest{
		Mode: "keyless", Owner: owner, Lease: 2 * time.Minute,
	})
	if err != nil {
		result.Failure = "begin BUILDING: " + err.Error()
		return result
	}
	service := NewBackfillService(
		opened.metaDB, DialectPostgres, "management", manifest, BackfillConfig{},
	)
	started := time.Now()
	backfilled, err := service.Run(ctx, lease.Owner, lease.Epoch)
	result.BackfillSeconds = time.Since(started).Seconds()
	if result.BackfillSeconds > 0 {
		result.BackfillRowsPerSecond = float64(backfilled.Linked) / result.BackfillSeconds
	}
	result.CompletedEvents = backfilled.Linked
	if err != nil {
		result.Failure = "RunBackfill: " + err.Error()
		return result
	}
	if !backfilled.Complete || backfilled.Linked != int64(rows) || backfilled.RemainingUnchained != 0 {
		result.Failure = fmt.Sprintf("incomplete backfill: %+v", backfilled)
		return result
	}
	if err := service.Activate(ctx, lease.Owner, lease.Epoch, manifest); err != nil {
		result.Failure = "activate: " + err.Error()
		return result
	}
	result.ChainValid, result.ChainContiguous, result.VerificationResult, err = verifyChainLoad(
		ctx, opened.metaDB, DialectPostgres, manifest,
	)
	if err != nil {
		result.Failure = "verify: " + err.Error()
		return result
	}
	result.Passed = result.BodyDistributionPass && result.ChainValid && result.ChainContiguous
	if !result.Passed {
		result.Failure = "body distribution or post-activation chain invariant failed"
	}
	return result
}

func seedBareChainLoadRows(ctx context.Context, database *sql.DB, fixture chainLoadFixture, count int) ([]int, error) {
	bodySizes := make([]int, count)
	const chunkSize = 5_000
	for start := 0; start < count; start += chunkSize {
		end := start + chunkSize
		if end > count {
			end = count
		}
		transaction, err := database.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		statement, err := transaction.PrepareContext(ctx, `
INSERT INTO audit_logs (decision, details_json, event_uuid)
VALUES ($1, $2, $3)`)
		if err != nil {
			_ = transaction.Rollback()
			return nil, err
		}
		for index := start; index < end; index++ {
			auditLog, bodySize := fixture.auditLog("pg-backfill", int64(index))
			bodySizes[index] = bodySize
			if _, err := statement.ExecContext(
				ctx, auditLog.Decision, optionalString(auditLog.DetailsJSON), optionalString(auditLog.EventUUID),
			); err != nil {
				_ = statement.Close()
				_ = transaction.Rollback()
				return nil, err
			}
		}
		if err := statement.Close(); err != nil {
			_ = transaction.Rollback()
			return nil, err
		}
		if err := transaction.Commit(); err != nil {
			return nil, err
		}
	}
	return bodySizes, nil
}

func measureUncontendedChainInserts(
	ctx context.Context,
	sink audit.Sink,
	fixture chainLoadFixture,
	count int,
) ([]time.Duration, error) {
	latencies := make([]time.Duration, 0, count)
	for index := 0; index < count; index++ {
		auditLog, _ := fixture.auditLog("pg-uncontended", int64(index))
		started := time.Now()
		if _, err := sink.Insert(ctx, auditLog); err != nil {
			return nil, err
		}
		latencies = append(latencies, time.Since(started))
	}
	return latencies, nil
}

func runOpenLoopChainLoad(
	parent context.Context,
	sink audit.Sink,
	fixture chainLoadFixture,
	scenario string,
	rate int,
	duration time.Duration,
	workers int,
) chainLoadRun {
	total := int64(math.Round(float64(rate) * duration.Seconds()))
	if total < 1 {
		total = 1
	}
	latencies := make([]time.Duration, total)
	completionOffsets := make([]time.Duration, total)
	bodySizes := make([]int, total)
	var next atomic.Int64
	var completed atomic.Int64
	var errorCount atomic.Int64
	var firstErrorOnce sync.Once
	var firstErrorText string

	drainAllowance := duration / 2
	if drainAllowance < 10*time.Second {
		drainAllowance = 10 * time.Second
	}
	if drainAllowance > 5*time.Minute {
		drainAllowance = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(parent, duration+drainAllowance)
	defer cancel()
	start := time.Now().Add(100 * time.Millisecond)
	var waitGroup sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			for {
				index := next.Add(1) - 1
				if index >= total {
					return
				}
				scheduledAt := start.Add(time.Duration(index) * time.Second / time.Duration(rate))
				if delay := time.Until(scheduledAt); delay > 0 {
					timer := time.NewTimer(delay)
					select {
					case <-ctx.Done():
						if !timer.Stop() {
							select {
							case <-timer.C:
							default:
							}
						}
						return
					case <-timer.C:
					}
				}
				auditLog, bodySize := fixture.auditLog(scenario, index)
				bodySizes[index] = bodySize
				_, err := sink.Insert(ctx, auditLog)
				latencies[index] = time.Since(scheduledAt)
				if err != nil {
					errorCount.Add(1)
					firstErrorOnce.Do(func() { firstErrorText = err.Error() })
					if ctx.Err() != nil {
						return
					}
					continue
				}
				completionOffsets[index] = time.Since(start)
				completed.Add(1)
			}
		}()
	}
	waitGroup.Wait()
	ended := time.Now()

	completedCount := completed.Load()
	notCompleted := total - completedCount - errorCount.Load()
	if notCompleted > 0 {
		errorCount.Add(notCompleted)
		firstErrorOnce.Do(func() { firstErrorText = "open-loop drain deadline exceeded" })
	}
	validLatencies := make([]time.Duration, 0, completedCount)
	validSizes := make([]int, 0, completedCount)
	firstCompletion := time.Duration(math.MaxInt64)
	lastCompletion := time.Duration(0)
	for index, latency := range latencies {
		if completionOffsets[index] > 0 && latency > 0 && bodySizes[index] > 0 {
			validLatencies = append(validLatencies, latency)
			validSizes = append(validSizes, bodySizes[index])
			if completionOffsets[index] < firstCompletion {
				firstCompletion = completionOffsets[index]
			}
			if completionOffsets[index] > lastCompletion {
				lastCompletion = completionOffsets[index]
			}
		}
	}
	steadyRate := float64(completedCount) / duration.Seconds()
	if completedCount > 1 && lastCompletion > firstCompletion {
		steadyRate = float64(completedCount-1) / (lastCompletion - firstCompletion).Seconds()
	}
	wall := ended.Sub(start)
	if wall < 0 {
		wall = 0
	}
	return chainLoadRun{
		offered: total, completed: completedCount, errors: errorCount.Load(), duration: duration,
		wall: wall, steadyRate: steadyRate, latencies: validLatencies,
		bodySizes: validSizes, firstErrorText: firstErrorText,
	}
}

func evaluateChainLoadRun(
	ctx context.Context,
	database *sql.DB,
	dialect Dialect,
	manifest ChainManifest,
	scenario string,
	run chainLoadRun,
	targetRate float64,
	enforceRate bool,
	enforceLatency bool,
	queueDrained bool,
) chainLoadResult {
	result := chainLoadResult{
		Scenario:             scenario,
		TargetRate:           targetRate,
		OfferedEvents:        run.offered,
		CompletedEvents:      run.completed,
		Errors:               run.errors,
		OfferedWindowSeconds: run.duration.Seconds(),
		WallSeconds:          run.wall.Seconds(),
		LatencyP50MS:         durationMilliseconds(percentileDuration(run.latencies, 0.50)),
		LatencyP95MS:         durationMilliseconds(percentileDuration(run.latencies, 0.95)),
		LatencyP99MS:         durationMilliseconds(percentileDuration(run.latencies, 0.99)),
		BodyMedianBytes:      percentileInt(run.bodySizes, 0.50),
		BodyP95Bytes:         percentileInt(run.bodySizes, 0.95),
		RateMeasurement:      "successful completion span (first-to-last), rounded to 0.1 events/s",
		Failure:              run.firstErrorText,
		QueueDrained:         queueDrained,
	}
	result.AchievedRate = math.Round(run.steadyRate*10) / 10
	if run.wall > 0 {
		result.WallRate = float64(run.completed) / run.wall.Seconds()
	}
	result.BodyDistributionPass = result.BodyMedianBytes <= 2*1024 && result.BodyP95Bytes <= 8*1024
	if enforceRate {
		passed := result.AchievedRate >= targetRate
		result.RateThresholdPassed = &passed
	}
	if enforceLatency {
		p95Passed := time.Duration(result.LatencyP95MS*float64(time.Millisecond)) <= chainLoadP95Limit
		p99Passed := time.Duration(result.LatencyP99MS*float64(time.Millisecond)) <= chainLoadP99Limit
		result.LatencyP95Passed = &p95Passed
		result.LatencyP99Passed = &p99Passed
	}
	var verifyErr error
	result.ChainValid, result.ChainContiguous, result.VerificationResult, verifyErr = verifyChainLoad(
		ctx, database, dialect, manifest,
	)
	if verifyErr != nil {
		if result.Failure != "" {
			result.Failure += "; "
		}
		result.Failure += "verify: " + verifyErr.Error()
	}
	result.Passed = result.Errors == 0 && result.BodyDistributionPass && result.QueueDrained &&
		result.ChainValid && result.ChainContiguous
	if result.RateThresholdPassed != nil {
		result.Passed = result.Passed && *result.RateThresholdPassed
	}
	if result.LatencyP95Passed != nil {
		result.Passed = result.Passed && *result.LatencyP95Passed && *result.LatencyP99Passed
	}
	if !result.Passed && result.Failure == "" {
		result.Failure = "one or more hard thresholds failed"
	}
	return result
}

func runSQLiteChainLoad(
	t *testing.T,
	ctx context.Context,
	manifest ChainManifest,
	fixture chainLoadFixture,
	cfg chainLoadConfig,
) chainLoadResult {
	t.Helper()
	opened, err := OpenWithSecret(
		ctx, filepath.Join(t.TempDir(), "chain-load.db"), []byte(testSecret),
	)
	if err != nil {
		return chainLoadResult{Scenario: "sqlite_sustained", Failure: "open SQLite: " + err.Error()}
	}
	defer func() {
		if err := opened.Close(); err != nil {
			t.Errorf("close SQLite load-test store: %v", err)
		}
	}()
	provisioner := NewChainProvisioner(
		opened.metaDB, DialectSQLite, "management", manifest, BackfillConfig{},
	)
	if err := provisioner.Provision(ctx, "sqlite-chain-load"); err != nil {
		return chainLoadResult{Scenario: "sqlite_sustained", Failure: "provision SQLite chain: " + err.Error()}
	}
	sink := newChainLoadGroupSink(t, opened.AuditLogs(), DialectSQLite)
	defer func() {
		if err := sink.Close(); err != nil {
			t.Errorf("close SQLite group commit sink: %v", err)
		}
	}()
	run := runOpenLoopChainLoad(
		ctx, sink, fixture, "sqlite-sustained", cfg.sqliteRate,
		time.Duration(cfg.sqliteSeconds)*time.Second, cfg.sqliteWorkers,
	)
	queueDrained := waitForChainLoadQueueDrain(sink)
	return evaluateChainLoadRun(
		ctx, opened.metaDB, DialectSQLite, manifest, "sqlite_sustained", run,
		float64(cfg.sqliteRate), true, true, queueDrained,
	)
}

func verifyChainLoad(
	ctx context.Context,
	database *sql.DB,
	dialect Dialect,
	manifest ChainManifest,
) (valid bool, contiguous bool, verificationResult string, resultErr error) {
	outcome, err := NewChainVerifier(database, dialect, "management", manifest).Verify(ctx)
	if err != nil {
		return false, false, outcome.Result, err
	}
	valid = outcome.Valid && outcome.Result == verificationValid
	query := `
SELECT COUNT(*), COUNT(DISTINCT chain_seq),
       COALESCE(MIN(chain_seq), 0), COALESCE(MAX(chain_seq), 0),
       COALESCE(SUM(CASE WHEN chain_seq IS NULL THEN 1 ELSE 0 END), 0)
FROM audit_logs`
	var total, distinctSequences, minimum, maximum, nullSequences int64
	if err := database.QueryRowContext(ctx, query).Scan(
		&total, &distinctSequences, &minimum, &maximum, &nullSequences,
	); err != nil {
		return valid, false, outcome.Result, err
	}
	contiguous = total == distinctSequences && nullSequences == 0
	if total == 0 {
		contiguous = contiguous && minimum == 0 && maximum == 0
	} else {
		contiguous = contiguous && minimum == 1 && maximum == total
	}
	return valid, contiguous, outcome.Result, nil
}

func samplePostgresChainLockWaits(
	ctx context.Context,
	database *sql.DB,
	interval time.Duration,
	stop <-chan struct{},
	done chan<- chainLoadLockObservation,
) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	active := make(map[int]time.Time)
	observation := chainLoadLockObservation{}
	finish := func(now time.Time) {
		for _, started := range active {
			observation.waits = append(observation.waits, now.Sub(started))
		}
		done <- observation
	}
	for {
		select {
		case <-ctx.Done():
			finish(time.Now())
			return
		case <-stop:
			finish(time.Now())
			return
		case now := <-ticker.C:
			rows, err := database.QueryContext(ctx, `
SELECT a.pid
FROM pg_stat_activity AS a
WHERE a.datname = current_database()
  AND a.pid <> pg_backend_pid()
  AND (a.wait_event_type = 'Lock' OR EXISTS (
        SELECT 1 FROM pg_locks AS l WHERE l.pid = a.pid AND NOT l.granted
      ))`)
			if err != nil {
				observation.errors++
				continue
			}
			seen := make(map[int]struct{})
			for rows.Next() {
				var pid int
				if err := rows.Scan(&pid); err != nil {
					observation.errors++
					continue
				}
				seen[pid] = struct{}{}
				if _, exists := active[pid]; !exists {
					active[pid] = now.Add(-interval)
				}
			}
			if err := rows.Err(); err != nil {
				observation.errors++
			}
			_ = rows.Close()
			for pid, started := range active {
				if _, exists := seen[pid]; !exists {
					observation.waits = append(observation.waits, now.Sub(started))
					delete(active, pid)
				}
			}
		}
	}
}

func evaluateLockObservation(
	observation chainLoadLockObservation,
	baselineP95 time.Duration,
	concurrentP95 time.Duration,
	chainValid bool,
	chainContiguous bool,
) chainLoadResult {
	p95 := percentileDuration(observation.waits, 0.95)
	p99 := percentileDuration(observation.waits, 0.99)
	p95Passed := p95 <= chainLoadLockP95Target
	p99Passed := p99 <= chainLoadLockP99Target
	result := chainLoadResult{
		Scenario:             "pg_state_lock_observation",
		Passed:               p95Passed && p99Passed,
		Advisory:             true,
		BodyDistributionPass: true,
		ChainValid:           chainValid,
		ChainContiguous:      chainContiguous,
		LockSamples:          len(observation.waits),
		LockSampleErrors:     observation.errors,
		LockWaitP95MS:        durationMilliseconds(p95),
		LockWaitP99MS:        durationMilliseconds(p99),
		LockP95ReferencePass: &p95Passed,
		LockP99ReferencePass: &p99Passed,
		BaselineP95MS:        durationMilliseconds(baselineP95),
		ContentionDeltaP95MS: durationMilliseconds(concurrentP95 - baselineP95),
		LockObservationNote: "approximate polling observation; advisory only. " +
			"Authoritative gates are append throughput, end-to-end latency, errors, and chain validity.",
	}
	if result.ContentionDeltaP95MS < 0 {
		result.ContentionDeltaP95MS = 0
	}
	return result
}

func percentileDuration(values []time.Duration, percentile float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	ordered := append([]time.Duration(nil), values...)
	sort.Slice(ordered, func(left, right int) bool { return ordered[left] < ordered[right] })
	index := int(math.Ceil(percentile*float64(len(ordered)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(ordered) {
		index = len(ordered) - 1
	}
	return ordered[index]
}

func percentileInt(values []int, percentile float64) int {
	if len(values) == 0 {
		return 0
	}
	ordered := append([]int(nil), values...)
	sort.Ints(ordered)
	index := int(math.Ceil(percentile*float64(len(ordered)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(ordered) {
		index = len(ordered) - 1
	}
	return ordered[index]
}

func durationMilliseconds(value time.Duration) float64 {
	return float64(value) / float64(time.Millisecond)
}

func printChainLoadResults(t *testing.T, results []chainLoadResult) {
	t.Helper()
	t.Log("CHAIN LOAD results (lock waits are approximate/advisory):")
	t.Log("scenario          result  target/s  achieved/s  wall/s     p95ms     p99ms   errors  chain")
	for _, result := range results {
		status := "FAIL"
		if result.Passed {
			status = "PASS"
		}
		t.Logf("%-17s %-6s %9.1f %11.1f %9.1f %9.2f %9.2f %8d  valid=%t contiguous=%t",
			result.Scenario, status, result.TargetRate, result.AchievedRate, result.WallRate,
			result.LatencyP95MS, result.LatencyP99MS, result.Errors,
			result.ChainValid, result.ChainContiguous,
		)
		if result.Scenario == "pg_backfill" {
			t.Logf("  backfill rows=%d seconds=%.3f rows/s=%.1f", result.BackfillRows,
				result.BackfillSeconds, result.BackfillRowsPerSecond)
		}
		if result.LockObservationNote != "" {
			t.Logf("  approximate state-lock wait p95=%.2fms p99=%.2fms samples=%d sample_errors=%d; "+
				"uncontended p95=%.2fms contention delta p95=%.2fms",
				result.LockWaitP95MS, result.LockWaitP99MS, result.LockSamples,
				result.LockSampleErrors, result.BaselineP95MS, result.ContentionDeltaP95MS)
		}
		logChainLoadJSON(t, "result", result)
	}
}

func logChainLoadJSON(t *testing.T, kind string, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Errorf("marshal chain load %s JSON: %v", kind, err)
		return
	}
	t.Logf("CHAIN_LOAD_JSON kind=%s %s", kind, encoded)
}
