package notify

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/cuipengdba/agentsql/internal/eventbus"
	"github.com/cuipengdba/agentsql/internal/model"
)

var (
	// ErrManagerClosed indicates that a lifecycle operation followed Close.
	ErrManagerClosed = errors.New("notification manager is closed")
	// ErrManagerStarted indicates that Start was called more than once.
	ErrManagerStarted = errors.New("notification manager is already started")
)

type sender interface {
	Send(ctx context.Context, payload Payload) error
	Close()
}

type channelRuntime struct {
	config ChannelConfig
	queue  chan model.AuditLog
	sender sender
	state  *statusState
}

type generation struct {
	ctx         context.Context
	cancel      context.CancelFunc
	runtimes    []*channelRuntime
	events      <-chan eventbus.Event
	unsubscribe func()
	wg          sync.WaitGroup
}

// Manager owns the live-only eventbus subscription and one bounded worker
// queue per enabled channel.
type Manager struct {
	mu       sync.Mutex
	hub      *eventbus.Hub
	options  managerOptions
	current  *generation
	started  bool
	closed   bool
	statusMu sync.RWMutex
	statuses map[string]*statusState
}

type statusState struct {
	mu     sync.Mutex
	status ChannelStatus
}

// NewManager creates an inert manager. Start or Reload applies configuration.
func NewManager(hub *eventbus.Hub, options ...Option) *Manager {
	configured := defaultManagerOptions()
	for _, option := range options {
		if option != nil {
			option(&configured)
		}
	}
	return &Manager{hub: hub, options: configured, statuses: make(map[string]*statusState)}
}

// Start applies the initial configuration. Disabled or empty configurations
// intentionally do not subscribe to the event bus.
func (manager *Manager) Start(ctx context.Context, config Config) error {
	if manager == nil {
		return ErrManagerClosed
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closed {
		return ErrManagerClosed
	}
	if manager.started {
		return ErrManagerStarted
	}
	next, err := manager.prepare(ctx, config)
	if err != nil {
		return err
	}
	manager.started = true
	manager.current = next
	if next != nil {
		next.start(manager)
	}
	return nil
}

// Reload atomically replaces the active generation. It cancels the old
// live-only subscription before establishing the new one, so retained eventbus
// history is never replayed. A very small best-effort gap is allowed.
func (manager *Manager) Reload(ctx context.Context, config Config) error {
	if manager == nil {
		return ErrManagerClosed
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closed {
		return ErrManagerClosed
	}
	next, err := manager.prepare(ctx, config)
	if err != nil {
		return err
	}
	if manager.current != nil {
		manager.current.stop()
	}
	manager.started = true
	manager.current = next
	if next != nil {
		next.start(manager)
	}
	return nil
}

// Close stops subscriptions and workers. It is safe to call concurrently and
// more than once.
func (manager *Manager) Close() error {
	if manager == nil {
		return nil
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closed {
		return nil
	}
	manager.closed = true
	if manager.current != nil {
		manager.current.stop()
		manager.current = nil
	}
	return nil
}

// Status returns a race-safe copy of cumulative process-local channel health.
func (manager *Manager) Status() map[string]ChannelStatus {
	if manager == nil {
		return map[string]ChannelStatus{}
	}
	manager.statusMu.RLock()
	states := make(map[string]*statusState, len(manager.statuses))
	for id, state := range manager.statuses {
		states[id] = state
	}
	manager.statusMu.RUnlock()
	result := make(map[string]ChannelStatus, len(states))
	for id, state := range states {
		state.mu.Lock()
		result[id] = state.status
		state.mu.Unlock()
	}
	return result
}

func (manager *Manager) prepare(ctx context.Context, config Config) (*generation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	normalized, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}
	if !normalized.Enabled {
		return nil, nil
	}
	var enabled []ChannelConfig
	for _, channel := range normalized.Channels {
		if channel.Enabled {
			enabled = append(enabled, channel)
		}
	}
	if len(enabled) == 0 {
		return nil, nil
	}
	if manager.hub == nil {
		return nil, errors.New("notification manager requires an event hub when enabled")
	}
	runtimeContext, cancel := context.WithCancel(context.Background())
	next := &generation{ctx: runtimeContext, cancel: cancel}
	for _, channel := range enabled {
		var transport sender
		switch channel.Kind {
		case ChannelWebhook:
			transport, err = newWebhookSender(ctx, channel, manager.options)
		case ChannelSyslog:
			transport, err = newSyslogSender(ctx, channel, manager.options)
		}
		if err != nil {
			cancel()
			for _, prepared := range next.runtimes {
				prepared.sender.Close()
			}
			return nil, fmt.Errorf("prepare notification channel %q: %w", channel.ID, err)
		}
		next.runtimes = append(next.runtimes, &channelRuntime{
			config: channel,
			queue:  make(chan model.AuditLog, normalized.QueueSize),
			sender: transport,
			state:  manager.status(channel.ID),
		})
	}
	return next, nil
}

func (manager *Manager) status(id string) *statusState {
	manager.statusMu.Lock()
	defer manager.statusMu.Unlock()
	state := manager.statuses[id]
	if state == nil {
		state = &statusState{}
		manager.statuses[id] = state
	}
	return state
}

func (generation *generation) start(manager *Manager) {
	for _, runtime := range generation.runtimes {
		generation.wg.Add(1)
		go runtime.run(generation.ctx, manager.options, &generation.wg)
	}
	generation.events, generation.unsubscribe = manager.hub.SubscribeLive()
	generation.wg.Add(1)
	go generation.dispatch(manager.options.metrics)
}

func (generation *generation) dispatch(metrics MetricRecorder) {
	defer generation.wg.Done()
	for {
		select {
		case <-generation.ctx.Done():
			return
		case event, open := <-generation.events:
			if !open {
				return
			}
			// Intent events stay visible in the audit stream, but only the final
			// outcome is eligible for external notification delivery.
			if isIntentAudit(event.Audit) {
				continue
			}
			for _, runtime := range generation.runtimes {
				if !matchesDecision(runtime.config, event.Audit.Decision) {
					continue
				}
				select {
				case runtime.queue <- event.Audit:
				default:
					runtime.recordDropped(metrics)
				}
			}
		}
	}
}

func (generation *generation) stop() {
	if generation.unsubscribe != nil {
		generation.unsubscribe()
	}
	generation.cancel()
	generation.wg.Wait()
	for _, runtime := range generation.runtimes {
		runtime.sender.Close()
	}
}

func (runtime *channelRuntime) run(ctx context.Context, options managerOptions, waitGroup *sync.WaitGroup) {
	defer waitGroup.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case audit := <-runtime.queue:
			runtime.deliver(ctx, audit, options)
		}
	}
}

func (runtime *channelRuntime) deliver(ctx context.Context, audit model.AuditLog, options managerOptions) {
	defer func() {
		if recover() != nil {
			runtime.recordFailed(options.metrics, "panic")
		}
	}()
	payload := project(ctx, audit, runtime.config.IncludeSQL, options.resolver)
	var err error
	for attempt := 0; attempt <= options.maxRetries; attempt++ {
		if attempt > 0 {
			if !waitBackoff(ctx, options.initialBackoff, options.maxBackoff, attempt-1) {
				return
			}
		}
		attemptContext, cancel := context.WithTimeout(ctx, options.httpTimeout)
		err = runtime.sender.Send(attemptContext, payload)
		cancel()
		if err == nil {
			runtime.recordSent(options.metrics, options.now())
			return
		}
		if ctx.Err() != nil {
			return
		}
	}
	runtime.recordFailed(options.metrics, failureReason(err))
}

func (runtime *channelRuntime) recordSent(metrics MetricRecorder, at time.Time) {
	runtime.state.mu.Lock()
	runtime.state.status.Sent++
	runtime.state.status.LastSuccessAt = at
	runtime.state.status.LastError = ""
	runtime.state.mu.Unlock()
	if metrics != nil {
		metrics.RecordNotificationSent(runtime.config.ID, at)
	}
}

func (runtime *channelRuntime) recordFailed(metrics MetricRecorder, reason string) {
	runtime.state.mu.Lock()
	runtime.state.status.Failed++
	runtime.state.status.LastError = reason
	runtime.state.mu.Unlock()
	if metrics != nil {
		metrics.RecordNotificationFailed(runtime.config.ID, reason)
	}
}

func (runtime *channelRuntime) recordDropped(metrics MetricRecorder) {
	runtime.state.mu.Lock()
	runtime.state.status.Dropped++
	runtime.state.mu.Unlock()
	if metrics != nil {
		metrics.RecordNotificationDropped(runtime.config.ID)
	}
}

func waitBackoff(ctx context.Context, initial, maximum time.Duration, retry int) bool {
	delay := initial
	for index := 0; index < retry; index++ {
		if delay >= maximum/4 {
			delay = maximum
			break
		}
		delay *= 4
	}
	if delay > maximum {
		delay = maximum
	}
	jittered := time.Duration(float64(delay) * (0.75 + rand.Float64()*0.5))
	timer := time.NewTimer(jittered)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func failureReason(err error) string {
	if err == nil {
		return "unknown"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, errEndpointBlocked) {
		return "ssrf_blocked"
	}
	var netError net.Error
	if errors.As(err, &netError) {
		if netError.Timeout() {
			return "timeout"
		}
		return "connect_error"
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "http status") {
		return "http_status"
	}
	if strings.Contains(message, "context canceled") {
		return "canceled"
	}
	return "send_error"
}

func (sender *webhookSender) Close() {
	if sender == nil || sender.client == nil {
		return
	}
	if closer, ok := sender.client.Transport.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func (sender *syslogSender) Close() {}
