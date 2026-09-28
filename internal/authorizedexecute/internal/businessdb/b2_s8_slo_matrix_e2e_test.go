package businessdb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const b2S8MatrixGate = "AGENTSQL_B2_S8_MATRIX"

type b2S8Latency struct {
	P50MS float64 `json:"p50_ms"`
	P95MS float64 `json:"p95_ms"`
	P99MS float64 `json:"p99_ms"`
	MaxMS float64 `json:"max_ms"`
}

type b2S8ModeReport struct {
	Mode                string            `json:"mode"`
	Requests            uint64            `json:"requests"`
	Successes           uint64            `json:"successes"`
	Errors              uint64            `json:"errors"`
	ErrorRate           float64           `json:"error_rate"`
	ThroughputPerSecond float64           `json:"throughput_per_second"`
	Latency             b2S8Latency       `json:"latency"`
	ErrorReasons        map[string]uint64 `json:"error_reasons,omitempty"`
	StarvedWorkers      int               `json:"starved_workers"`
	OIDOrderVerified    bool              `json:"oid_order_verified"`
	UnauthorizedColumns uint64            `json:"unauthorized_columns"`
}

type b2S8CellReport struct {
	PostgresMajor        int                       `json:"postgres_major"`
	DurationSeconds      float64                   `json:"duration_seconds"`
	Stages               []int                     `json:"stages"`
	Modes                map[string]b2S8ModeReport `json:"modes"`
	LockWaitSamples      uint64                    `json:"lock_wait_samples"`
	MaxWaitingLocks      int64                     `json:"max_waiting_locks"`
	WaitingRelkinds      map[string]uint64         `json:"waiting_relkinds"`
	Deadlocks            int64                     `json:"deadlocks"`
	PoolMaxOpen          int                       `json:"pool_max_open"`
	PoolPeakInUse        int                       `json:"pool_peak_in_use"`
	PoolSaturatedSamples uint64                    `json:"pool_saturated_samples"`
	DDLTransactions      uint64                    `json:"ddl_transactions"`
	DDLFailures          uint64                    `json:"ddl_failures"`
	DDLRebuilds          uint64                    `json:"ddl_rebuilds"`
	DDLLastSuccessAgeMS  float64                   `json:"ddl_last_success_age_ms"`
	LockTransactions     uint64                    `json:"lock_transactions"`
	Passed               bool                      `json:"passed"`
	Failures             []string                  `json:"failures,omitempty"`
}

type b2S8Report struct {
	Schema             string           `json:"schema"`
	GeneratedAt        time.Time        `json:"generated_at"`
	GoVersion          string           `json:"go_version"`
	RequestedDuration  string           `json:"requested_duration"`
	MinimumDuration    string           `json:"minimum_qualifying_duration"`
	QualifyingEvidence bool             `json:"qualifying_evidence"`
	Cells              []b2S8CellReport `json:"cells"`
	Passed             bool             `json:"passed"`
	Digest             string           `json:"digest"`
}

type b2S8ModeStats struct {
	mu           sync.Mutex
	latencies    []time.Duration
	errors       map[string]uint64
	workerOK     map[int]uint64
	requests     uint64
	successes    uint64
	unauthorized uint64
	oidOrdered   bool
}

type b2S8Monitor struct {
	waitSamples atomic.Uint64
	maxWaiting  atomic.Int64
	poolPeak    atomic.Int64
	saturated   atomic.Uint64
	ddl         atomic.Uint64
	ddlFailures atomic.Uint64
	ddlRebuilds atomic.Uint64
	ddlLastOK   atomic.Int64
	locks       atomic.Uint64
	mu          sync.Mutex
	relkinds    map[string]uint64
}

// TestB2S8PostgresSLOMatrix is intentionally opt-in. The default run is a
// qualifying ten-minute staircase for each PG major; short runs must opt in
// separately and are always marked non-qualifying in the report.
func TestB2S8PostgresSLOMatrix(t *testing.T) {
	if os.Getenv(b2S8MatrixGate) != "1" {
		t.Skip("set " + b2S8MatrixGate + "=1 to run the PG14/18 B2 S8 SLO matrix")
	}
	duration := b2S8Duration(t)
	qualifying := duration >= 10*time.Minute
	if !qualifying && os.Getenv("AGENTSQL_B2_S8_ALLOW_SHORT") != "1" {
		t.Fatalf("AGENTSQL_B2_S8_DURATION=%s is below 10m; set AGENTSQL_B2_S8_ALLOW_SHORT=1 only for non-qualifying smoke", duration)
	}
	report := b2S8Report{Schema: "agentsql.b2.s8-slo/v1", GeneratedAt: time.Now().UTC(), GoVersion: runtime.Version(),
		RequestedDuration: duration.String(), MinimumDuration: "10m", QualifyingEvidence: qualifying, Passed: true}
	for _, major := range b2S8Majors() {
		major := major
		t.Run(fmt.Sprintf("pg%d", major), func(t *testing.T) {
			cell := runB2S8Cell(t, major, duration)
			report.Cells = append(report.Cells, cell)
			report.Passed = report.Passed && cell.Passed
		})
	}
	encoded, err := json.Marshal(report)
	require.NoError(t, err)
	digest := sha256.Sum256(encoded)
	report.Digest = "sha256:" + hex.EncodeToString(digest[:])
	writeB2S8Report(t, report)
	if !report.Passed {
		t.Fatalf("B2 S8 SLO matrix failed: %+v", report.Cells)
	}
}

func runB2S8Cell(t *testing.T, major int, duration time.Duration) b2S8CellReport {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), duration+5*time.Minute)
	t.Cleanup(cancel)
	majorText := strconv.Itoa(major)
	root, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "dbext", "postgres", "agentsql_binder"))
	require.NoError(t, err)
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{
		FromDockerfile: testcontainers.FromDockerfile{Context: root, Dockerfile: "Dockerfile.test", Repo: "agentsql-b2-s8", Tag: "pg" + majorText,
			BuildArgs: map[string]*string{"PG_MAJOR": &majorText}, KeepImage: true},
		Env:          map[string]string{"POSTGRES_DB": "agentsql", "POSTGRES_USER": "agentsql", "POSTGRES_PASSWORD": "agentsql-password"},
		ExposedPorts: []string{"5432/tcp"}, Labels: map[string]string{"agentsql.b2.s8-matrix": "true", "agentsql.pg-major": majorText},
		WaitingFor: wait.ForAll(wait.ForListeningPort("5432/tcp"), wait.ForLog("database system is ready to accept connections").WithOccurrence(2)).WithDeadline(2 * time.Minute),
	}, Started: true})
	require.NoError(t, err)
	testcontainers.CleanupContainer(t, container)
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)
	load := newB2S8Executor(t, ctx, "load", host, port.Int(), 32)
	observer := newB2S8Executor(t, ctx, "observer", host, port.Int(), 6)
	for _, statement := range []string{
		`CREATE SCHEMA agentsql_catalog`,
		`CREATE EXTENSION agentsql_binder WITH SCHEMA agentsql_catalog`,
		`CREATE SCHEMA s8`,
		`CREATE TABLE s8.hot(id integer PRIMARY KEY, payload text, forbidden text)`,
		`CREATE TABLE s8.churn(id integer)`,
		`INSERT INTO s8.hot SELECT i,'payload-'||i,'forbidden-'||i FROM pg_catalog.generate_series(1,100) i`,
		`CREATE VIEW s8.v_hot AS SELECT id,payload,forbidden FROM s8.hot`,
	} {
		_, err := load.Execute(ctx, statement)
		require.NoError(t, err, statement)
	}
	var deadlocksBefore int64
	require.NoError(t, observer.pool.QueryRow(ctx, `SELECT deadlocks FROM pg_catalog.pg_stat_database WHERE datname=current_database()`).Scan(&deadlocksBefore))
	monitor := &b2S8Monitor{relkinds: make(map[string]uint64)}
	monitor.ddlLastOK.Store(time.Now().UnixNano())
	stopBackground := make(chan struct{})
	var background sync.WaitGroup
	background.Add(3)
	go func() { defer background.Done(); b2S8LockContender(ctx, observer, stopBackground, monitor) }()
	go func() { defer background.Done(); b2S8DDLChurn(ctx, observer, stopBackground, monitor) }()
	go func() { defer background.Done(); b2S8Observe(ctx, load, observer, stopBackground, monitor) }()

	stages := b2S8Stages()
	stats := map[string]*b2S8ModeStats{
		"closed": {errors: make(map[string]uint64), workerOK: make(map[int]uint64), oidOrdered: true},
		"native": {errors: make(map[string]uint64), workerOK: make(map[int]uint64), oidOrdered: true},
	}
	started := time.Now()
	stageDuration := duration / time.Duration(len(stages))
	for stage, concurrency := range stages {
		runFor := stageDuration
		if stage == len(stages)-1 {
			runFor = duration - time.Since(started)
			if runFor <= 0 {
				runFor = time.Millisecond
			}
		}
		runB2S8Stage(ctx, load, concurrency, runFor, stage*1000, stats)
	}
	close(stopBackground)
	background.Wait()
	elapsed := time.Since(started)
	var deadlocksAfter int64
	require.NoError(t, observer.pool.QueryRow(ctx, `SELECT deadlocks FROM pg_catalog.pg_stat_database WHERE datname=current_database()`).Scan(&deadlocksAfter))
	cell := b2S8CellReport{PostgresMajor: major, DurationSeconds: elapsed.Seconds(), Stages: stages,
		Modes: make(map[string]b2S8ModeReport), LockWaitSamples: monitor.waitSamples.Load(), MaxWaitingLocks: monitor.maxWaiting.Load(),
		Deadlocks: deadlocksAfter - deadlocksBefore, PoolMaxOpen: load.poolSnapshot().MaxOpen, PoolPeakInUse: int(monitor.poolPeak.Load()),
		PoolSaturatedSamples: monitor.saturated.Load(), DDLTransactions: monitor.ddl.Load(), DDLFailures: monitor.ddlFailures.Load(),
		DDLRebuilds: monitor.ddlRebuilds.Load(), DDLLastSuccessAgeMS: float64(time.Since(time.Unix(0, monitor.ddlLastOK.Load()))) / float64(time.Millisecond),
		LockTransactions: monitor.locks.Load(), Passed: true}
	monitor.mu.Lock()
	cell.WaitingRelkinds = cloneB2S8Counts(monitor.relkinds)
	monitor.mu.Unlock()
	maxP99 := b2S8Float("AGENTSQL_B2_S8_MAX_P99_MS", 2_000)
	maxErrorRate := b2S8Float("AGENTSQL_B2_S8_MAX_ERROR_RATE", 0.005)
	for mode, value := range stats {
		modeReport := value.report(mode, elapsed)
		cell.Modes[mode] = modeReport
		if modeReport.Latency.P99MS > maxP99 {
			cell.Failures = append(cell.Failures, fmt.Sprintf("%s p99 %.3fms > %.3fms", mode, modeReport.Latency.P99MS, maxP99))
		}
		if modeReport.ErrorRate > maxErrorRate {
			cell.Failures = append(cell.Failures, fmt.Sprintf("%s error rate %.6f > %.6f", mode, modeReport.ErrorRate, maxErrorRate))
		}
		if modeReport.StarvedWorkers != 0 || !modeReport.OIDOrderVerified || modeReport.UnauthorizedColumns != 0 {
			cell.Failures = append(cell.Failures, fmt.Sprintf("%s starvation/order/authorization invariant failed", mode))
		}
	}
	if cell.Deadlocks != 0 {
		cell.Failures = append(cell.Failures, fmt.Sprintf("deadlocks=%d", cell.Deadlocks))
	}
	if cell.DDLTransactions == 0 || cell.LockTransactions == 0 || cell.LockWaitSamples == 0 {
		cell.Failures = append(cell.Failures, "DDL/lock contention was not observed")
	}
	maxDDLSilenceMS := b2S8Float("AGENTSQL_B2_S8_MAX_DDL_SILENCE_MS", 5_000)
	if cell.DDLLastSuccessAgeMS > maxDDLSilenceMS {
		cell.Failures = append(cell.Failures, fmt.Sprintf("DDL churn stalled: last success %.3fms ago > %.3fms", cell.DDLLastSuccessAgeMS, maxDDLSilenceMS))
	}
	cell.Passed = len(cell.Failures) == 0
	return cell
}

func newB2S8Executor(t *testing.T, ctx context.Context, id, host string, port, connections int) *PostgresExecutor {
	t.Helper()
	executor, err := NewPostgresExecutor(ctx, model.Datasource{ID: "s8-" + id, DBType: "postgres", Host: host, Port: port,
		Database: "agentsql", Username: "agentsql", ConnLimit: connections, StmtTimeoutMS: 5_000}, "agentsql-password", false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, executor.Close()) })
	return executor
}

func runB2S8Stage(ctx context.Context, executor *PostgresExecutor, concurrency int, duration time.Duration, workerBase int, stats map[string]*b2S8ModeStats) {
	stop := make(chan struct{})
	var wait sync.WaitGroup
	for worker := 0; worker < concurrency; worker++ {
		mode := "closed"
		if worker%2 == 1 {
			mode = "native"
		}
		workerID := workerBase + worker
		wait.Add(1)
		go func() {
			defer wait.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				started := time.Now()
				ordered, unauthorized, err := b2S8Request(ctx, executor, mode)
				stats[mode].record(workerID, time.Since(started), ordered, unauthorized, err)
			}
		}()
	}
	timer := time.NewTimer(duration)
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	close(stop)
	wait.Wait()
}

func b2S8Request(ctx context.Context, executor *PostgresExecutor, mode string) (bool, bool, error) {
	budget := &unlimitedPostgresBudget{}
	if mode == "closed" {
		prepared, err := executor.BindClosedSelect(ctx, BindRequest{RawSQL: `SELECT h.payload FROM s8.hot h WHERE h.id=1`,
			Identity: SemanticIdentity{DatasourceIdentity: "s8-closed"}}, budget)
		if err != nil {
			return true, false, err
		}
		ordered := b2S8LockOrder(prepared.Program().LockExpectation)
		result, executeErr := prepared.Execute(ctx, 10)
		unauthorized := false
		for _, column := range result.Columns {
			unauthorized = unauthorized || strings.EqualFold(column, "forbidden")
		}
		_, verifyErr := prepared.VerifyPost(ctx, budget)
		closeErr := prepared.Close(ctx)
		if executeErr != nil {
			return ordered, unauthorized, executeErr
		}
		if verifyErr != nil {
			return ordered, unauthorized, verifyErr
		}
		return ordered, unauthorized, closeErr
	}
	const query = `SELECT v.id,v.payload FROM s8.v_hot v WHERE v.id=1`
	enrollment, err := executor.EnrollPostgresSelect(ctx, query, budget)
	if err != nil {
		return true, false, err
	}
	prepared, err := executor.PrepareBoundPostgresSelect(ctx, query, &enrollment, budget)
	if err != nil {
		return true, false, err
	}
	ordered := sort.SliceIsSorted(manifestRelationOIDs(prepared.Manifest()), func(i, j int) bool {
		oids := manifestRelationOIDs(prepared.Manifest())
		return oids[i] < oids[j]
	})
	result, executeErr := prepared.Execute(ctx, 10)
	unauthorized := false
	for _, column := range result.Columns {
		unauthorized = unauthorized || strings.EqualFold(column, "forbidden")
	}
	_, verifyErr := prepared.VerifyPost(ctx, budget)
	closeErr := prepared.Close(ctx, executeErr == nil && verifyErr == nil)
	for _, candidate := range []error{executeErr, verifyErr, closeErr} {
		if candidate != nil {
			return ordered, unauthorized, candidate
		}
	}
	return ordered, unauthorized, nil
}

func b2S8LockOrder(locks []RelationLockExpectation) bool {
	return sort.SliceIsSorted(locks, func(i, j int) bool { return locks[i].RelationOID < locks[j].RelationOID })
}

func (stats *b2S8ModeStats) record(worker int, latency time.Duration, ordered, unauthorized bool, err error) {
	stats.mu.Lock()
	defer stats.mu.Unlock()
	if _, exists := stats.workerOK[worker]; !exists {
		stats.workerOK[worker] = 0
	}
	stats.requests++
	stats.latencies = append(stats.latencies, latency)
	stats.oidOrdered = stats.oidOrdered && ordered
	if unauthorized {
		stats.unauthorized++
	}
	if err == nil {
		stats.successes++
		stats.workerOK[worker]++
		return
	}
	reason := "AUTH_DATABASE_ERROR"
	if value, ok := err.(interface{ AuthorizationReason() string }); ok {
		reason = value.AuthorizationReason()
	} else {
		var databaseError *DBError
		if errors.As(err, &databaseError) {
			reason = fmt.Sprintf("db:%s:%s:%s", databaseError.Kind, databaseError.Stage, databaseError.Code)
			if driverCode, ok := databaseError.DriverCodeForLog(); ok {
				reason += ":" + driverCode
			}
		}
	}
	stats.errors[reason]++
}

func (stats *b2S8ModeStats) report(mode string, elapsed time.Duration) b2S8ModeReport {
	stats.mu.Lock()
	defer stats.mu.Unlock()
	latencies := append([]time.Duration(nil), stats.latencies...)
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	errors := uint64(0)
	for _, count := range stats.errors {
		errors += count
	}
	starved := 0
	for _, count := range stats.workerOK {
		if count == 0 {
			starved++
		}
	}
	rate := float64(0)
	if stats.requests != 0 {
		rate = float64(errors) / float64(stats.requests)
	}
	return b2S8ModeReport{Mode: mode, Requests: stats.requests, Successes: stats.successes, Errors: errors,
		ErrorRate: rate, ThroughputPerSecond: float64(stats.successes) / elapsed.Seconds(), Latency: b2S8Percentiles(latencies),
		ErrorReasons: cloneB2S8Counts(stats.errors), StarvedWorkers: starved, OIDOrderVerified: stats.oidOrdered,
		UnauthorizedColumns: stats.unauthorized}
}

func b2S8Percentiles(values []time.Duration) b2S8Latency {
	if len(values) == 0 {
		return b2S8Latency{}
	}
	pick := func(percent float64) float64 {
		index := int(float64(len(values)-1) * percent)
		return float64(values[index]) / float64(time.Millisecond)
	}
	return b2S8Latency{P50MS: pick(.50), P95MS: pick(.95), P99MS: pick(.99), MaxMS: float64(values[len(values)-1]) / float64(time.Millisecond)}
}

func b2S8LockContender(ctx context.Context, executor *PostgresExecutor, stop <-chan struct{}, monitor *b2S8Monitor) {
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		default:
		}
		connection, err := executor.pool.Acquire(ctx)
		if err != nil {
			continue
		}
		tx, err := connection.Begin(ctx)
		if err == nil {
			_, err = tx.Exec(ctx, `LOCK TABLE s8.hot IN ACCESS EXCLUSIVE MODE`)
			if err == nil {
				_, err = tx.Exec(ctx, `SELECT pg_catalog.pg_sleep(0.02)`)
			}
			_ = tx.Rollback(context.Background())
		}
		connection.Release()
		if err == nil {
			monitor.locks.Add(1)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func b2S8DDLChurn(ctx context.Context, executor *PostgresExecutor, stop <-chan struct{}, monitor *b2S8Monitor) {
	for sequence := 0; ; sequence++ {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		default:
		}
		// PostgreSQL retains dropped-column pg_attribute slots. Rebuild before
		// reaching the 1600-column history limit so successful DDL continues for
		// the entire soak instead of silently becoming an error-only loop.
		if sequence > 0 && sequence%256 == 0 {
			tx, err := executor.pool.Begin(ctx)
			if err == nil {
				_, err = tx.Exec(ctx, `DROP TABLE s8.churn`)
			}
			if err == nil {
				_, err = tx.Exec(ctx, `CREATE TABLE s8.churn(id integer)`)
			}
			if err == nil {
				err = tx.Commit(ctx)
			} else if tx != nil {
				_ = tx.Rollback(context.Background())
			}
			if err != nil {
				monitor.ddlFailures.Add(1)
				time.Sleep(25 * time.Millisecond)
				continue
			}
			monitor.ddlRebuilds.Add(1)
			monitor.ddlLastOK.Store(time.Now().UnixNano())
		}
		name := fmt.Sprintf("s8_c_%d", sequence%4)
		if _, err := executor.pool.Exec(ctx, `ALTER TABLE s8.churn ADD COLUMN `+name+` integer`); err == nil {
			if _, err = executor.pool.Exec(ctx, `ALTER TABLE s8.churn DROP COLUMN `+name); err == nil {
				monitor.ddl.Add(1)
				monitor.ddlLastOK.Store(time.Now().UnixNano())
			} else {
				monitor.ddlFailures.Add(1)
			}
		} else {
			monitor.ddlFailures.Add(1)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func b2S8Observe(ctx context.Context, load, observer *PostgresExecutor, stop <-chan struct{}, monitor *b2S8Monitor) {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			pool := load.poolSnapshot()
			for {
				peak := monitor.poolPeak.Load()
				if int64(pool.InUse) <= peak || monitor.poolPeak.CompareAndSwap(peak, int64(pool.InUse)) {
					break
				}
			}
			if pool.InUse >= pool.MaxOpen {
				monitor.saturated.Add(1)
			}
			rows, err := observer.pool.Query(ctx, `SELECT COALESCE(c.relkind::text,'nonrelation'),count(*)
FROM pg_catalog.pg_locks l LEFT JOIN pg_catalog.pg_class c ON c.oid=l.relation
WHERE NOT l.granted AND l.database=(SELECT oid FROM pg_catalog.pg_database WHERE datname=current_database())
GROUP BY c.relkind`)
			if err != nil {
				continue
			}
			var waiting int64
			for rows.Next() {
				var kind string
				var count int64
				if rows.Scan(&kind, &count) == nil {
					waiting += count
					monitor.mu.Lock()
					monitor.relkinds[kind] += uint64(count)
					monitor.mu.Unlock()
				}
			}
			rows.Close()
			if waiting > 0 {
				monitor.waitSamples.Add(1)
			}
			for {
				maximum := monitor.maxWaiting.Load()
				if waiting <= maximum || monitor.maxWaiting.CompareAndSwap(maximum, waiting) {
					break
				}
			}
		}
	}
}

func b2S8Duration(t *testing.T) time.Duration {
	t.Helper()
	value := os.Getenv("AGENTSQL_B2_S8_DURATION")
	if value == "" {
		return 10 * time.Minute
	}
	duration, err := time.ParseDuration(value)
	require.NoError(t, err)
	require.Positive(t, duration)
	return duration
}

func b2S8Stages() []int {
	value := os.Getenv("AGENTSQL_B2_S8_STAGES")
	if value == "" {
		return []int{4, 12, 24}
	}
	var stages []int
	for _, token := range strings.Split(value, ",") {
		parsed, err := strconv.Atoi(strings.TrimSpace(token))
		if err == nil && parsed >= 2 {
			stages = append(stages, parsed)
		}
	}
	if len(stages) == 0 {
		return []int{4, 12, 24}
	}
	return stages
}

func b2S8Majors() []int {
	value := os.Getenv("AGENTSQL_B2_S8_MAJORS")
	if value == "" {
		return []int{14, 18}
	}
	var majors []int
	for _, token := range strings.Split(value, ",") {
		parsed, err := strconv.Atoi(strings.TrimSpace(token))
		if err == nil && (parsed == 14 || parsed == 18) {
			majors = append(majors, parsed)
		}
	}
	if len(majors) == 0 {
		return []int{14, 18}
	}
	return majors
}

func b2S8Float(name string, fallback float64) float64 {
	if value, err := strconv.ParseFloat(os.Getenv(name), 64); err == nil && value > 0 {
		return value
	}
	return fallback
}

func cloneB2S8Counts(source map[string]uint64) map[string]uint64 {
	result := make(map[string]uint64, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func writeB2S8Report(t *testing.T, report b2S8Report) {
	t.Helper()
	directory := os.Getenv("AGENTSQL_B2_S8_OUTPUT_DIR")
	if directory == "" {
		directory = filepath.Join("..", "..", "..", "..", "artifacts", "b2-s8")
	} else if !filepath.IsAbs(directory) {
		root, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
		require.NoError(t, err)
		directory = filepath.Join(root, directory)
	}
	require.NoError(t, os.MkdirAll(directory, 0o700))
	encoded, err := json.MarshalIndent(report, "", "  ")
	require.NoError(t, err)
	path := filepath.Join(directory, "slo-"+report.GeneratedAt.Format("20060102T150405Z")+".json")
	require.NoError(t, os.WriteFile(path, append(encoded, '\n'), 0o600))
	t.Logf("B2 S8 report: %s", path)
}
