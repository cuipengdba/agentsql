// Package notify sends best-effort, security-minimized audit notifications.
package notify

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	defaultQueueSize   = 64
	defaultHTTPTimeout = 3 * time.Second
	defaultMaxRetries  = 2
)

// WebhookTemplate identifies an incoming-webhook payload shape.
type WebhookTemplate string

const (
	WebhookGeneric  WebhookTemplate = "generic"
	WebhookFeishu   WebhookTemplate = "feishu"
	WebhookDingTalk WebhookTemplate = "dingtalk"
	WebhookWeCom    WebhookTemplate = "wecom"
	WebhookSlack    WebhookTemplate = "slack"
)

// ChannelKind identifies one supported transport.
type ChannelKind string

const (
	ChannelWebhook ChannelKind = "webhook"
	ChannelSyslog  ChannelKind = "syslog"
)

// Config is the complete process-local notification configuration. It is
// intentionally persistence-agnostic; T37-b owns storage and bootstrap wiring.
type Config struct {
	Enabled   bool
	QueueSize int
	Channels  []ChannelConfig
}

// ChannelConfig configures one independently queued notification destination.
type ChannelConfig struct {
	ID                    string
	Enabled               bool
	Kind                  ChannelKind
	Decisions             []string
	IncludeSQL            bool
	AllowPrivateEndpoints bool
	Webhook               *WebhookConfig
	Syslog                *SyslogConfig
}

// WebhookConfig configures an HTTP incoming webhook. Secret is used for the
// generic AgentSQL HMAC or DingTalk's signing algorithm, depending on Template.
type WebhookConfig struct {
	Template    WebhookTemplate
	URL         string
	BearerToken string
	Headers     map[string]string
	Secret      string
}

// SyslogConfig configures RFC5424 delivery over UDP or TCP.
type SyslogConfig struct {
	Host      string
	Port      int
	Transport string
	Facility  int
}

// NameResolver supplies display names that are not present in AuditLog. It
// should be backed by a fast cache because it is called by channel workers.
type NameResolver interface {
	AgentName(ctx context.Context, id string) string
	DatasourceName(ctx context.Context, id string) string
}

// MetricRecorder is implemented by internal/metrics.Metrics. Implementations
// must keep reason bounded and must never record destination URLs or payloads.
type MetricRecorder interface {
	RecordNotificationSent(channel string, at time.Time)
	RecordNotificationFailed(channel, reason string)
	RecordNotificationDropped(channel string)
}

// ChannelStatus is a process-local delivery snapshot for the future admin API.
type ChannelStatus struct {
	Sent          uint64    `json:"sent"`
	Failed        uint64    `json:"failed"`
	Dropped       uint64    `json:"dropped"`
	LastError     string    `json:"last_error,omitempty"`
	LastSuccessAt time.Time `json:"last_success_at,omitempty"`
}

// Option customizes Manager without adding persistence concerns to Config.
type Option func(*managerOptions)

type managerOptions struct {
	resolver       NameResolver
	metrics        MetricRecorder
	httpTimeout    time.Duration
	maxRetries     int
	initialBackoff time.Duration
	maxBackoff     time.Duration
	now            func() time.Time
	httpClient     func(allowPrivate bool) *http.Client
}

// WithNameResolver enriches projected Agent/DataSource IDs with display names.
func WithNameResolver(resolver NameResolver) Option {
	return func(options *managerOptions) { options.resolver = resolver }
}

// WithMetrics connects delivery counters to the process metrics registry.
func WithMetrics(metrics MetricRecorder) Option {
	return func(options *managerOptions) { options.metrics = metrics }
}

// WithHTTPTimeout overrides the default three-second per-attempt timeout.
// It primarily exists to keep deterministic local tests fast.
func WithHTTPTimeout(timeout time.Duration) Option {
	return func(options *managerOptions) {
		if timeout > 0 {
			options.httpTimeout = timeout
		}
	}
}

// WithRetryPolicy overrides the default two retries and bounded backoff.
func WithRetryPolicy(maxRetries int, initialBackoff, maxBackoff time.Duration) Option {
	return func(options *managerOptions) {
		if maxRetries >= 0 && maxRetries <= defaultMaxRetries {
			options.maxRetries = maxRetries
		}
		if initialBackoff > 0 {
			options.initialBackoff = initialBackoff
		}
		if maxBackoff > 0 {
			options.maxBackoff = maxBackoff
		}
	}
}

func defaultManagerOptions() managerOptions {
	return managerOptions{
		httpTimeout:    defaultHTTPTimeout,
		maxRetries:     defaultMaxRetries,
		initialBackoff: 200 * time.Millisecond,
		maxBackoff:     time.Second,
		now:            time.Now,
	}
}

func normalizeConfig(config Config) (Config, error) {
	config = cloneConfig(config)
	if config.QueueSize < 0 {
		return Config{}, errors.New("notify queue size must not be negative")
	}
	if config.QueueSize == 0 {
		config.QueueSize = defaultQueueSize
	}
	seen := make(map[string]struct{}, len(config.Channels))
	for index := range config.Channels {
		channel := &config.Channels[index]
		channel.ID = strings.TrimSpace(channel.ID)
		if !channel.Enabled {
			continue
		}
		if channel.ID == "" {
			return Config{}, fmt.Errorf("notification channel %d has no id", index)
		}
		if _, exists := seen[channel.ID]; exists {
			return Config{}, fmt.Errorf("duplicate notification channel id %q", channel.ID)
		}
		seen[channel.ID] = struct{}{}
		decisions, err := normalizeDecisions(channel.Decisions)
		if err != nil {
			return Config{}, fmt.Errorf("notification channel %q: %w", channel.ID, err)
		}
		channel.Decisions = decisions
		switch channel.Kind {
		case ChannelWebhook:
			if channel.Webhook == nil || channel.Syslog != nil {
				return Config{}, fmt.Errorf("notification channel %q must contain only webhook config", channel.ID)
			}
			if err := normalizeWebhook(channel.Webhook); err != nil {
				return Config{}, fmt.Errorf("notification channel %q: %w", channel.ID, err)
			}
		case ChannelSyslog:
			if channel.Syslog == nil || channel.Webhook != nil {
				return Config{}, fmt.Errorf("notification channel %q must contain only syslog config", channel.ID)
			}
			if err := normalizeSyslog(channel.Syslog); err != nil {
				return Config{}, fmt.Errorf("notification channel %q: %w", channel.ID, err)
			}
		default:
			return Config{}, fmt.Errorf("notification channel %q has unsupported kind %q", channel.ID, channel.Kind)
		}
	}
	return config, nil
}

func cloneConfig(config Config) Config {
	cloned := config
	cloned.Channels = make([]ChannelConfig, len(config.Channels))
	for index, channel := range config.Channels {
		cloned.Channels[index] = channel
		cloned.Channels[index].Decisions = append([]string(nil), channel.Decisions...)
		if channel.Webhook != nil {
			webhook := *channel.Webhook
			if channel.Webhook.Headers != nil {
				webhook.Headers = make(map[string]string, len(channel.Webhook.Headers))
				for name, value := range channel.Webhook.Headers {
					webhook.Headers[name] = value
				}
			}
			cloned.Channels[index].Webhook = &webhook
		}
		if channel.Syslog != nil {
			syslog := *channel.Syslog
			cloned.Channels[index].Syslog = &syslog
		}
	}
	return cloned
}

func normalizeDecisions(decisions []string) ([]string, error) {
	if len(decisions) == 0 {
		return []string{"deny", "error"}, nil
	}
	allowed := map[string]bool{"allow": true, "warn": true, "approve": true, "deny": true, "error": true}
	result := make([]string, 0, len(decisions))
	seen := make(map[string]struct{}, len(decisions))
	for _, decision := range decisions {
		decision = strings.ToLower(strings.TrimSpace(decision))
		if !allowed[decision] {
			return nil, fmt.Errorf("unsupported decision %q", decision)
		}
		if _, exists := seen[decision]; exists {
			continue
		}
		seen[decision] = struct{}{}
		result = append(result, decision)
	}
	if len(result) == 0 {
		return nil, errors.New("decision filter must not be empty")
	}
	return result, nil
}

func normalizeWebhook(config *WebhookConfig) error {
	config.URL = strings.TrimSpace(config.URL)
	if config.Template == "" {
		config.Template = WebhookGeneric
	}
	switch config.Template {
	case WebhookGeneric, WebhookFeishu, WebhookDingTalk, WebhookWeCom, WebhookSlack:
	default:
		return fmt.Errorf("unsupported webhook template %q", config.Template)
	}
	if config.URL == "" {
		return errors.New("webhook URL is required")
	}
	for name, value := range config.Headers {
		if strings.TrimSpace(name) == "" || strings.ContainsAny(name, "\r\n") || strings.ContainsAny(value, "\r\n") {
			return errors.New("webhook header contains invalid characters")
		}
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "host", "content-length", "connection", "transfer-encoding", "x-agentsql-timestamp", "x-agentsql-signature":
			return fmt.Errorf("webhook header %q is reserved", name)
		}
	}
	return nil
}

func normalizeSyslog(config *SyslogConfig) error {
	config.Host = strings.TrimSpace(config.Host)
	config.Transport = strings.ToLower(strings.TrimSpace(config.Transport))
	if config.Host == "" {
		return errors.New("syslog host is required")
	}
	if config.Port < 1 || config.Port > 65535 {
		return errors.New("syslog port must be between 1 and 65535")
	}
	if config.Transport != "udp" && config.Transport != "tcp" {
		return errors.New("syslog transport must be udp or tcp")
	}
	if config.Facility < 0 || config.Facility > 23 {
		return errors.New("syslog facility must be between 0 and 23")
	}
	return nil
}
