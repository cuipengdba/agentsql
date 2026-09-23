package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/metrics"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/stretchr/testify/require"
)

type chainMonitorManifest struct {
	mode       string
	version    int
	keys       map[int][]byte
	modeErr    error
	versionErr error
}

func (manifest chainMonitorManifest) ExpectedMode(context.Context) (string, error) {
	return manifest.mode, manifest.modeErr
}

func (manifest chainMonitorManifest) CurrentKeyVersion(context.Context) (int, error) {
	return manifest.version, manifest.versionErr
}

func (manifest chainMonitorManifest) ChainKeyForVersion(_ context.Context, version int) ([]byte, error) {
	key, exists := manifest.keys[version]
	if !exists {
		return nil, fmt.Errorf("chain key version %d is unavailable", version)
	}
	return key, nil
}

type chainMonitorProvider struct {
	manifest store.ChainManifest
	err      error
}

func (provider chainMonitorProvider) ChainManifestForDomain(context.Context, string) (store.ChainManifest, error) {
	return provider.manifest, provider.err
}

func TestChainMonitorStartupImmediatelyVerifiesActiveKeylessChain(t *testing.T) {
	opened, _ := openChainMonitorStore(t, chainMonitorManifest{mode: "keyless"}, true)
	hub := metrics.New(nil)
	monitor := NewChainMonitor(opened, store.NewKeylessChainManifestProvider(), hub, []string{"management"}, time.Hour)
	monitor.jitter = 0
	monitor.start(context.Background())
	t.Cleanup(monitor.close)

	require.Eventually(t, func() bool {
		valid, validOK := chainMetricValueOK(t, hub, "agentsql_audit_chain_valid", "management")
		verified, verifiedOK := chainMetricValueOK(t, hub, "agentsql_audit_chain_last_verified_timestamp_seconds", "management")
		return validOK && verifiedOK && valid == 1 && verified > 0
	}, time.Second, 10*time.Millisecond)
	require.Zero(t, chainMetricValue(t, hub, "agentsql_audit_chain_verification_lag", "management"))
}

func TestChainMonitorSingleFlightSkipsOverlappingDomainRounds(t *testing.T) {
	provider := &blockingChainMonitorProvider{entered: make(chan struct{}), release: make(chan struct{})}
	monitor := NewChainMonitor(nil, provider, metrics.New(nil), []string{"management"}, 5*time.Millisecond)
	monitor.jitter = 0
	monitor.start(context.Background())
	<-provider.entered

	time.Sleep(20 * time.Millisecond)
	monitor.RunChainVerificationOnce(context.Background())
	require.Equal(t, int32(1), provider.calls.Load())
	require.Equal(t, int32(1), provider.maximum.Load())
	close(provider.release)
	monitor.close()
}

func TestChainMonitorHMACMissingKeyOnlyLowersWriterReadiness(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	good := chainMonitorManifest{mode: "hmac", version: 1, keys: map[int][]byte{1: key}}
	opened, _ := openChainMonitorStore(t, good, true)
	hub := metrics.New(nil)
	missing := chainMonitorManifest{mode: "hmac", version: 1}
	monitor := NewChainMonitor(opened, chainMonitorProvider{manifest: missing}, hub, []string{"management"}, time.Hour)

	monitor.RunChainVerificationOnce(context.Background())
	require.False(t, monitor.AuditWriterReady("management"))
	require.Zero(t, chainMetricValue(t, hub, "agentsql_audit_writer_ready", "management"))
	require.Zero(t, chainMetricValue(t, hub, "agentsql_audit_chain_valid", "management"))
}

func TestChainMonitorHistoricalBreakDoesNotLowerWriterReadiness(t *testing.T) {
	manifest := chainMonitorManifest{mode: "keyless"}
	opened, database := openChainMonitorStore(t, manifest, true)
	_, err := database.Exec(`DROP TRIGGER trg_audit_logs_chain_contract_update`)
	require.NoError(t, err)
	_, err = database.Exec(`UPDATE audit_logs SET details_json = '{"tampered":true}' WHERE id = (SELECT MIN(id) FROM audit_logs)`)
	require.NoError(t, err)
	hub := metrics.New(nil)
	monitor := NewChainMonitor(opened, chainMonitorProvider{manifest: manifest}, hub, []string{"management"}, time.Hour)

	monitor.RunChainVerificationOnce(context.Background())
	require.True(t, monitor.AuditWriterReady("management"))
	require.Zero(t, chainMetricValue(t, hub, "agentsql_audit_chain_valid", "management"))
	require.Equal(t, float64(1), chainMetricValue(t, hub, "agentsql_audit_writer_ready", "management"))
}

func TestChainMonitorVerificationLagThresholdAndDisabledExclusion(t *testing.T) {
	manifest := chainMonitorManifest{mode: "keyless"}
	opened, _ := openChainMonitorStore(t, manifest, true)
	hub := metrics.New(nil)
	monitor := NewChainMonitor(opened, chainMonitorProvider{manifest: manifest}, hub, []string{"management"}, 100*time.Second)
	now := time.Unix(10_000, 0)
	monitor.now = func() time.Time { return now }
	monitor.observations["management"] = chainObservation{lastVerified: now.Add(-120 * time.Second), enabled: true}

	monitor.refreshLag(now)
	require.Zero(t, chainMetricValue(t, hub, "agentsql_audit_chain_verification_lag", "management"))
	monitor.observations["management"] = chainObservation{lastVerified: now.Add(-121 * time.Second), enabled: true}
	monitor.refreshLag(now)
	require.Equal(t, float64(1), chainMetricValue(t, hub, "agentsql_audit_chain_verification_lag", "management"))
	monitor.RunChainVerificationOnce(context.Background())
	require.Zero(t, chainMetricValue(t, hub, "agentsql_audit_chain_verification_lag", "management"))

	disabled, _ := openChainMonitorStore(t, manifest, false)
	disabledHub := metrics.New(nil)
	disabledMonitor := NewChainMonitor(disabled, chainMonitorProvider{manifest: manifest}, disabledHub, []string{"management"}, 100*time.Second)
	disabledMonitor.now = func() time.Time { return now }
	disabledMonitor.RunChainVerificationOnce(context.Background())
	disabledMonitor.now = func() time.Time { return now.Add(time.Hour) }
	disabledMonitor.refreshLag(disabledMonitor.now())
	require.Zero(t, chainMetricValue(t, disabledHub, "agentsql_audit_chain_verification_lag", "management"))
}

func TestChainMonitorCloseStopsTickerAndWaitsForRound(t *testing.T) {
	provider := &cancelableChainMonitorProvider{entered: make(chan struct{})}
	monitor := NewChainMonitor(nil, provider, metrics.New(nil), []string{"management"}, 5*time.Millisecond)
	monitor.jitter = 0
	monitor.start(context.Background())
	<-provider.entered
	monitor.close()
	callsAtClose := provider.calls.Load()
	time.Sleep(20 * time.Millisecond)
	require.Equal(t, callsAtClose, provider.calls.Load())
	require.Equal(t, int32(0), provider.active.Load())
}

func TestConfiguredChainDomains(t *testing.T) {
	require.Equal(t, []string{"management"}, configuredChainDomains(false))
	require.Equal(t, []string{"management", "traffic"}, configuredChainDomains(true))
}

func TestChainMonitorIntervalJitterBounds(t *testing.T) {
	monitor := NewChainMonitor(nil, nil, nil, nil, 100*time.Second)
	monitor.random = func() float64 { return 0 }
	require.Equal(t, 80*time.Second, monitor.nextInterval())
	monitor.random = func() float64 { return 1 }
	require.Equal(t, 120*time.Second, monitor.nextInterval())
}

type blockingChainMonitorProvider struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	calls   atomic.Int32
	active  atomic.Int32
	maximum atomic.Int32
}

func (provider *blockingChainMonitorProvider) ChainManifestForDomain(context.Context, string) (store.ChainManifest, error) {
	provider.calls.Add(1)
	active := provider.active.Add(1)
	defer provider.active.Add(-1)
	for {
		maximum := provider.maximum.Load()
		if active <= maximum || provider.maximum.CompareAndSwap(maximum, active) {
			break
		}
	}
	provider.once.Do(func() { close(provider.entered) })
	<-provider.release
	return nil, errors.New("released")
}

type cancelableChainMonitorProvider struct {
	entered chan struct{}
	once    sync.Once
	calls   atomic.Int32
	active  atomic.Int32
}

func (provider *cancelableChainMonitorProvider) ChainManifestForDomain(ctx context.Context, _ string) (store.ChainManifest, error) {
	provider.calls.Add(1)
	provider.active.Add(1)
	defer provider.active.Add(-1)
	provider.once.Do(func() { close(provider.entered) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func openChainMonitorStore(t *testing.T, manifest store.ChainManifest, active bool) (*store.Store, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "chain-monitor.db")
	opened, err := store.OpenWithSecret(context.Background(), path, bootstrapTestSecret)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, opened.Close()) })
	database, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	if !active {
		return opened, database
	}
	details := `{"test":true}`
	_, err = opened.AuditLogs().Insert(context.Background(), model.AuditLog{Decision: "allow", DetailsJSON: &details})
	require.NoError(t, err)
	provisioner := store.NewChainProvisioner(database, store.DialectSQLite, "management", manifest, store.BackfillConfig{})
	require.NoError(t, provisioner.Provision(context.Background(), "chain-monitor-test"))
	return opened, database
}

func chainMetricValue(t *testing.T, hub *metrics.Metrics, name, domain string) float64 {
	t.Helper()
	value, exists := chainMetricValueOK(t, hub, name, domain)
	require.True(t, exists, "%s for %s was not exposed", name, domain)
	return value
}

func chainMetricValueOK(t *testing.T, hub *metrics.Metrics, name, domain string) (float64, bool) {
	t.Helper()
	recorder := httptest.NewRecorder()
	hub.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body, err := io.ReadAll(recorder.Result().Body)
	require.NoError(t, err)
	pattern := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + `\{domain="` + regexp.QuoteMeta(domain) + `"\} ([^\s]+)$`)
	match := pattern.FindSubmatch(body)
	if len(match) != 2 {
		return 0, false
	}
	value, err := strconv.ParseFloat(string(match[1]), 64)
	require.NoError(t, err)
	return value, true
}
