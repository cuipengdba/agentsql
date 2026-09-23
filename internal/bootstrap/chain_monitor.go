package bootstrap

import (
	"context"
	"errors"
	"log/slog"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/cuipengdba/agentsql/internal/metrics"
	"github.com/cuipengdba/agentsql/internal/store"
)

const (
	chainVerificationInterval = 15 * time.Minute
	chainVerificationJitter   = 0.20
	chainVerificationValid    = "VALID_AT_OBSERVED_HEAD"
)

var errChainManifestUnavailable = errors.New("audit chain manifest is unavailable")

// ChainManifestProvider resolves trusted, database-external chain authority.
type ChainManifestProvider = store.ChainManifestProvider

type chainObservation struct {
	lastVerified time.Time
	enabled      bool
}

// ChainMonitor periodically verifies the configured audit-chain domains. It is
// observational: its result never participates in the process-wide readiness
// endpoint.
type ChainMonitor struct {
	metrics   *metrics.Metrics
	store     *store.Store
	manifests ChainManifestProvider
	domains   []string
	interval  time.Duration
	jitter    float64
	now       func() time.Time
	random    func() float64

	flightMu map[string]*sync.Mutex

	stateMu       sync.RWMutex
	observations  map[string]chainObservation
	expectedModes map[string]string

	lifecycleMu sync.Mutex
	cancel      context.CancelFunc
	wait        sync.WaitGroup
}

// NewChainMonitor constructs a monitor with production jitter. interval is
// injectable so tests and alternate orchestrators can use a shorter cadence.
func NewChainMonitor(
	metadataStore *store.Store,
	manifests ChainManifestProvider,
	hub *metrics.Metrics,
	domains []string,
	interval time.Duration,
) *ChainMonitor {
	monitor := &ChainMonitor{
		metrics:       hub,
		store:         metadataStore,
		manifests:     manifests,
		domains:       uniqueChainDomains(domains),
		interval:      interval,
		jitter:        chainVerificationJitter,
		now:           time.Now,
		random:        rand.Float64,
		flightMu:      make(map[string]*sync.Mutex),
		observations:  make(map[string]chainObservation),
		expectedModes: make(map[string]string),
	}
	for _, domain := range monitor.domains {
		monitor.flightMu[domain] = &sync.Mutex{}
		hub.SetAuditChainValid(domain, false)
		hub.SetAuditChainVerificationLag(domain, false)
	}
	return monitor
}

// Domains returns a defensive copy of the domains observed by this monitor.
func (monitor *ChainMonitor) Domains() []string {
	if monitor == nil {
		return nil
	}
	return append([]string(nil), monitor.domains...)
}

// RunChainVerificationOnce synchronously attempts one verification for every
// domain. A domain already being verified is skipped without waiting.
func (monitor *ChainMonitor) RunChainVerificationOnce(ctx context.Context) {
	if monitor == nil || ctx == nil {
		return
	}
	monitor.refreshLag(monitor.nowUTC())
	var wait sync.WaitGroup
	for _, domain := range monitor.domains {
		domain := domain
		wait.Add(1)
		go func() {
			defer wait.Done()
			monitor.verifyDomain(ctx, domain)
		}()
	}
	wait.Wait()
}

// AuditWriterReady is false only when the trusted expected mode is HMAC and a
// non-empty current chain key cannot be obtained. Chain validity is deliberately
// irrelevant to writer readiness.
func (monitor *ChainMonitor) AuditWriterReady(domain string) bool {
	if monitor == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ready := monitor.auditWriterReady(ctx, domain, nil)
	monitor.metrics.SetAuditWriterReady(domain, ready)
	return ready
}

func (monitor *ChainMonitor) verifyDomain(ctx context.Context, domain string) {
	flight := monitor.flightMu[domain]
	if flight == nil || !flight.TryLock() {
		return
	}
	defer flight.Unlock()

	manifest, err := monitor.manifest(ctx, domain)
	if err != nil {
		monitor.metrics.SetAuditWriterReady(domain, monitor.readyAfterManifestFailure(ctx, domain))
		monitor.metrics.SetAuditChainValid(domain, false)
		if ctx.Err() == nil {
			slog.Warn("audit chain monitor could not load manifest", "domain", domain, "error", err)
		}
		return
	}
	monitor.metrics.SetAuditWriterReady(domain, monitor.auditWriterReady(ctx, domain, manifest))

	if monitor.store == nil {
		monitor.metrics.SetAuditChainValid(domain, false)
		slog.Warn("audit chain monitor store is unavailable", "domain", domain)
		return
	}
	access, err := monitor.store.Chain(domain)
	if err != nil {
		monitor.metrics.SetAuditChainValid(domain, false)
		slog.Warn("audit chain monitor could not open domain", "domain", domain, "error", err)
		return
	}
	outcome, verifyErr := access.Verifier(manifest).VerifyAndPersist(ctx)
	completedAt := monitor.nowUTC()
	monitor.metrics.SetAuditChainValid(domain, outcome.Valid && outcome.Result == chainVerificationValid)
	monitor.metrics.SetAuditChainLastVerified(domain, completedAt)

	monitor.stateMu.Lock()
	monitor.observations[domain] = chainObservation{
		lastVerified: completedAt,
		enabled:      outcome.Status != "" && outcome.Status != "DISABLED",
	}
	monitor.stateMu.Unlock()
	monitor.refreshDomainLag(domain, completedAt)

	if verifyErr != nil && ctx.Err() == nil {
		slog.Warn("audit chain verification completed with an error", "domain", domain, "result", outcome.Result, "error", verifyErr)
	}
}

func (monitor *ChainMonitor) auditWriterReady(ctx context.Context, domain string, manifest store.ChainManifest) bool {
	if manifest == nil {
		loaded, err := monitor.manifest(ctx, domain)
		if err != nil {
			return monitor.readyAfterManifestFailure(ctx, domain)
		}
		manifest = loaded
	}
	mode, err := manifest.ExpectedMode(ctx)
	if err != nil {
		return monitor.readyAfterManifestFailure(ctx, domain)
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	monitor.stateMu.Lock()
	monitor.expectedModes[domain] = mode
	monitor.stateMu.Unlock()
	if mode != "hmac" {
		return true
	}
	version, err := manifest.CurrentKeyVersion(ctx)
	if err != nil || version < 1 {
		return false
	}
	key, err := manifest.ChainKeyForVersion(ctx, version)
	return err == nil && len(key) != 0
}

func (monitor *ChainMonitor) manifest(ctx context.Context, domain string) (store.ChainManifest, error) {
	if monitor.manifests == nil {
		return nil, errChainManifestUnavailable
	}
	manifest, err := monitor.manifests.ChainManifestForDomain(ctx, domain)
	if err != nil {
		return nil, err
	}
	if manifest == nil {
		return nil, errChainManifestUnavailable
	}
	return manifest, nil
}

func (monitor *ChainMonitor) readyAfterManifestFailure(ctx context.Context, domain string) bool {
	monitor.stateMu.RLock()
	mode := monitor.expectedModes[domain]
	monitor.stateMu.RUnlock()
	if mode == "hmac" {
		return false
	}
	if monitor.store == nil {
		return true
	}
	access, err := monitor.store.Chain(domain)
	if err != nil {
		return true
	}
	state, err := access.State().Get(ctx, domain)
	return err != nil || state.Mode == nil || *state.Mode != "hmac"
}

func (monitor *ChainMonitor) refreshLag(now time.Time) {
	if monitor == nil {
		return
	}
	for _, domain := range monitor.domains {
		monitor.refreshDomainLag(domain, now)
	}
}

func (monitor *ChainMonitor) refreshDomainLag(domain string, now time.Time) {
	monitor.stateMu.RLock()
	observation := monitor.observations[domain]
	monitor.stateMu.RUnlock()
	lagging := observation.enabled && !observation.lastVerified.IsZero() &&
		monitor.interval > 0 && now.Sub(observation.lastVerified) > time.Duration(float64(monitor.interval)*1.2)
	monitor.metrics.SetAuditChainVerificationLag(domain, lagging)
}

func (monitor *ChainMonitor) nowUTC() time.Time {
	if monitor == nil || monitor.now == nil {
		return time.Now().UTC()
	}
	return monitor.now().UTC()
}

func (monitor *ChainMonitor) nextInterval() time.Duration {
	if monitor == nil || monitor.interval <= 0 {
		return 0
	}
	jitter := monitor.jitter
	if jitter < 0 {
		jitter = -jitter
	}
	random := 0.5
	if monitor.random != nil {
		random = monitor.random()
	}
	factor := 1 + jitter*(2*random-1)
	return time.Duration(float64(monitor.interval) * factor)
}

func (monitor *ChainMonitor) start(parent context.Context) {
	if monitor == nil || parent == nil || monitor.interval <= 0 {
		return
	}
	monitor.lifecycleMu.Lock()
	if monitor.cancel != nil {
		monitor.lifecycleMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	monitor.cancel = cancel
	monitor.wait.Add(1)
	monitor.lifecycleMu.Unlock()

	go func() {
		defer monitor.wait.Done()
		monitor.launchRound(ctx)
		for {
			timer := time.NewTimer(monitor.nextInterval())
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
				monitor.refreshLag(monitor.nowUTC())
				monitor.launchRound(ctx)
			}
		}
	}()
}

func (monitor *ChainMonitor) launchRound(ctx context.Context) {
	monitor.wait.Add(1)
	go func() {
		defer monitor.wait.Done()
		monitor.RunChainVerificationOnce(ctx)
	}()
}

func (monitor *ChainMonitor) close() {
	if monitor == nil {
		return
	}
	monitor.lifecycleMu.Lock()
	cancel := monitor.cancel
	monitor.cancel = nil
	monitor.lifecycleMu.Unlock()
	if cancel != nil {
		cancel()
	}
	monitor.wait.Wait()
}

func uniqueChainDomains(domains []string) []string {
	seen := make(map[string]struct{}, len(domains))
	result := make([]string, 0, len(domains))
	for _, domain := range domains {
		domain = strings.TrimSpace(domain)
		if domain == "" {
			continue
		}
		if _, exists := seen[domain]; exists {
			continue
		}
		seen[domain] = struct{}{}
		result = append(result, domain)
	}
	return result
}

func configuredChainDomains(separate bool) []string {
	domains := []string{"management"}
	if separate {
		domains = append(domains, "traffic")
	}
	return domains
}
