package authorizedexecute

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const b2ColumnLoadGate = "AGENTSQL_B2_COLUMN_LOAD"

type b2ColumnLoadResult struct {
	PostgresMajor        string         `json:"postgres_major"`
	DurationSeconds      float64        `json:"duration_seconds"`
	Workers              int            `json:"workers"`
	Completed            int64          `json:"completed"`
	Throughput           float64        `json:"throughput_per_second"`
	P95MS                float64        `json:"p95_ms"`
	P99MS                float64        `json:"p99_ms"`
	BaselineP99MS        float64        `json:"s0_baseline_p99_ms"`
	IncrementalP99MS     float64        `json:"incremental_p99_ms"`
	CatalogBinderP95MS   float64        `json:"catalog_binder_p95_ms"`
	ErrorCodes           map[string]int `json:"error_codes"`
	ReservationTriggered bool           `json:"reservation_triggered"`
}

func TestB2ColumnSelectProductionLoad(t *testing.T) {
	if os.Getenv(b2ColumnLoadGate) != "1" {
		t.Skip("set " + b2ColumnLoadGate + "=1 to run the PG16/18 B2 load gate")
	}
	for _, major := range []string{"16", "18"} {
		t.Run("pg"+major, func(t *testing.T) { runB2ColumnLoad(t, major) })
	}
}

func runB2ColumnLoad(t *testing.T, major string) {
	t.Helper()
	ctx := s4DockerTestContext(t)
	root, err := filepath.Abs(filepath.Join("..", "..", "dbext", "postgres", "agentsql_binder"))
	require.NoError(t, err)
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			FromDockerfile: testcontainers.FromDockerfile{Context: root, Dockerfile: "Dockerfile.test", Repo: "agentsql-b2-load", Tag: "pg" + major, BuildArgs: map[string]*string{"PG_MAJOR": &major}, KeepImage: true},
			Env:            map[string]string{"POSTGRES_DB": "agentsql", "POSTGRES_USER": "agentsql", "POSTGRES_PASSWORD": "agentsql-password"},
			ExposedPorts:   []string{"5432/tcp"},
			WaitingFor:     wait.ForAll(wait.ForListeningPort("5432/tcp"), wait.ForLog("database system is ready to accept connections").WithOccurrence(2)).WithDeadline(90 * time.Second),
		}, Started: true,
	})
	require.NoError(t, err)
	testcontainers.CleanupContainer(t, container)
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)
	secret := []byte("0123456789abcdef0123456789abcdef")
	cipher, err := store.NewPasswordCipher(secret)
	require.NoError(t, err)
	encrypted, err := cipher.Encrypt("agentsql-password")
	require.NoError(t, err)
	datasource := model.Datasource{ID: "b2-load-pg-" + major, DBType: "postgres", Host: host,
		Port: port.Int(), Database: "agentsql", Username: "agentsql", PasswordEnc: encrypted,
		ConnLimit: 16, StmtTimeoutMS: 5_000, RowLimit: 10}
	gateway := NewGateway(false)
	t.Cleanup(func() { require.NoError(t, gateway.CloseAll()) })
	for _, sqlText := range []string{
		`CREATE SCHEMA agentsql_catalog`,
		`CREATE EXTENSION agentsql_binder WITH SCHEMA agentsql_catalog`,
		`CREATE SCHEMA load`,
		`CREATE TABLE load.t(id integer, phone text)`,
		`INSERT INTO load.t VALUES(1,'13812345678')`,
	} {
		statement, err := gateway.AuthorizedExecute(ctx, datasource, secret, sqlText, "")
		require.NoError(t, err)
		_, err = statement.Execute(ctx)
		require.NoError(t, err)
		require.NoError(t, statement.Close())
	}

	query := `SELECT phone FROM load.t WHERE id=1`
	enrollment, err := gateway.EnrollPostgresSelect(ctx, datasource, secret, query, DefaultLimits)
	require.NoError(t, err)
	policies := policiesForEnrollment(datasource.ID, enrollment)
	redactor, err := mask.NewRedactor([]mask.Rule{{Schema: "load", Table: "t", Column: "phone", SensitiveType: mask.TypePhone, Algorithm: mask.AlgoMask}})
	require.NoError(t, err)

	reservationTriggered := false
	held := make([]*Reservation, 0, DefaultReservationLimits.PerAgent)
	for index := 0; index < DefaultReservationLimits.PerAgent; index++ {
		reservation, reserveErr := gateway.reservations.Reserve("reservation-proof", "reservation-proof", datasource.ID, 1)
		require.NoError(t, reserveErr)
		held = append(held, reservation)
	}
	_, reserveErr := gateway.reservations.Reserve("reservation-proof", "reservation-proof", datasource.ID, 1)
	reservationTriggered = reserveErr != nil && StableError(reserveErr).Reason == ReasonConcurrencyLimit
	for _, reservation := range held {
		reservation.Release()
	}
	require.True(t, reservationTriggered)

	duration := 10 * time.Second
	if text := os.Getenv("AGENTSQL_B2_LOAD_SECONDS"); text != "" {
		seconds, parseErr := strconv.Atoi(text)
		require.NoError(t, parseErr)
		require.Greater(t, seconds, 0)
		duration = time.Duration(seconds) * time.Second
	}
	const workers = 4
	latencies := make([]time.Duration, 0, 4096)
	binderCatalog := make([]time.Duration, 0, 4096)
	errorCodes := make(map[string]int)
	var mu sync.Mutex
	var completed atomic.Int64

	execute := func(runCtx context.Context) error {
		started := time.Now()
		var businessStarted time.Time
		var bindDuration time.Duration
		_, err := executeColumnAuthorized(runCtx, gateway, datasource, secret, query, ColumnAuthorizationRequest{
			Agent: model.Agent{ID: "agent", Status: "active", Level: "readonly"}, Policies: policies,
			Redactor: redactor, RowLimit: 10, PreliminaryAllowed: true, ControlRevisionDigest: "load",
			Observe: func(phase SelectPhase) {
				now := time.Now()
				if phase == PhaseBusinessBegin {
					businessStarted = now
				} else if phase == PhaseExecute && !businessStarted.IsZero() {
					bindDuration = now.Sub(businessStarted)
				}
			},
			DurableAudit: func(context.Context, ColumnAuthorizationAudit, *model.QueryResult, mask.RedactReport) error {
				return nil
			},
			FinalFence: func(context.Context) error { return nil },
		})
		elapsed := time.Since(started)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			errorCodes[string(StableError(err).Reason)]++
			return err
		}
		latencies = append(latencies, elapsed)
		binderCatalog = append(binderCatalog, bindDuration)
		completed.Add(1)
		return nil
	}
	for index := 0; index < 10; index++ {
		require.NoError(t, execute(ctx))
	}
	mu.Lock()
	latencies = latencies[:0]
	binderCatalog = binderCatalog[:0]
	completed.Store(0)
	mu.Unlock()

	started := time.Now()
	stopAt := started.Add(duration)
	var waitGroup sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			for time.Now().Before(stopAt) {
				_ = execute(ctx)
			}
		}()
	}
	waitGroup.Wait()
	wall := time.Since(started)
	mu.Lock()
	latencyCopy := append([]time.Duration(nil), latencies...)
	binderCopy := append([]time.Duration(nil), binderCatalog...)
	errorCopy := make(map[string]int, len(errorCodes))
	for key, value := range errorCodes {
		errorCopy[key] = value
	}
	mu.Unlock()
	require.NotEmpty(t, latencyCopy)
	require.Empty(t, errorCopy)
	p95, p99 := percentileDuration(latencyCopy, 0.95), percentileDuration(latencyCopy, 0.99)
	binderP95 := percentileDuration(binderCopy, 0.95)
	const baselineP99MS = 5.5
	result := b2ColumnLoadResult{PostgresMajor: major, DurationSeconds: wall.Seconds(), Workers: workers,
		Completed: completed.Load(), Throughput: float64(completed.Load()) / wall.Seconds(),
		P95MS: milliseconds(p95), P99MS: milliseconds(p99), BaselineP99MS: baselineP99MS,
		IncrementalP99MS: milliseconds(p99) - baselineP99MS, CatalogBinderP95MS: milliseconds(binderP95),
		ErrorCodes: errorCopy, ReservationTriggered: reservationTriggered}
	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	t.Logf("B2_COLUMN_LOAD %s", encoded)
	require.LessOrEqual(t, result.P99MS, 250.0, fmt.Sprintf("B2 p99 latency is unacceptable: %+v", result))
}

func percentileDuration(values []time.Duration, quantile float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(left, right int) bool { return sorted[left] < sorted[right] })
	index := int(float64(len(sorted))*quantile+0.999999) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}

func milliseconds(value time.Duration) float64 { return float64(value) / float64(time.Millisecond) }
