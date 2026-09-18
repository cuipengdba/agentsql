package adminapi

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/eventbus"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/notify"
)

const notificationSecretMask = "********"

type notificationConfigDTO struct {
	Enabled   bool                     `json:"enabled"`
	QueueSize int                      `json:"queue_size"`
	Channels  []notificationChannelDTO `json:"channels"`
}

type notificationChannelDTO struct {
	ID                    string                  `json:"id"`
	Enabled               bool                    `json:"enabled"`
	Kind                  notify.ChannelKind      `json:"kind"`
	Decisions             []string                `json:"decisions"`
	IncludeSQL            bool                    `json:"include_sql"`
	AllowPrivateEndpoints bool                    `json:"allow_private_endpoints"`
	Webhook               *notificationWebhookDTO `json:"webhook,omitempty"`
	Syslog                *notify.SyslogConfig    `json:"syslog,omitempty"`
}

type notificationWebhookDTO struct {
	Template              notify.WebhookTemplate `json:"template"`
	URL                   string                 `json:"url"`
	URLConfigured         bool                   `json:"url_configured"`
	BearerToken           string                 `json:"bearer_token"`
	BearerTokenConfigured bool                   `json:"bearer_token_configured"`
	Headers               map[string]string      `json:"headers"`
	HeadersConfigured     bool                   `json:"headers_configured"`
	Secret                string                 `json:"secret"`
	SecretConfigured      bool                   `json:"secret_configured"`
}

type notificationTestInput struct {
	Channel notificationChannelDTO `json:"channel"`
}

type notificationTestResult struct {
	ChannelID string `json:"channel_id"`
	Success   bool   `json:"success"`
	Category  string `json:"category"`
}

type notificationHealthChannel struct {
	ID string `json:"id"`
	notify.ChannelStatus
}

type notificationHealthView struct {
	Channels []notificationHealthChannel `json:"channels"`
}

func (handler *Handler) notificationsGet(writer http.ResponseWriter, request *http.Request) {
	config, err := handler.deps.Runtime.Store.Notifications().Get(request.Context())
	if err != nil {
		handler.internal(writer, err)
		return
	}
	handler.ok(writer, notificationConfigToDTO(config))
}

func (handler *Handler) notificationsPut(writer http.ResponseWriter, request *http.Request) {
	var input notificationConfigDTO
	if err := decodeJSON(writer, request, &input); err != nil {
		handler.fail(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	if handler.deps.Runtime.Notifications == nil {
		handler.fail(writer, http.StatusServiceUnavailable, "notification manager is unavailable")
		return
	}

	handler.notificationMu.Lock()
	defer handler.notificationMu.Unlock()
	repository := handler.deps.Runtime.Store.Notifications()
	previous, err := repository.Get(request.Context())
	if err != nil {
		handler.internal(writer, err)
		return
	}
	next := mergeNotificationSecrets(notificationDTOToConfig(input), previous)
	if err := validateNotificationConfig(request.Context(), next); err != nil {
		handler.fail(writer, http.StatusUnprocessableEntity, "invalid notification configuration")
		return
	}
	if err := repository.Replace(request.Context(), next); err != nil {
		handler.internal(writer, err)
		return
	}
	reloadContext, cancelReload := context.WithTimeout(context.Background(), 5*time.Second)
	reloadErr := handler.deps.Runtime.Notifications.Reload(reloadContext, next)
	cancelReload()
	if reloadErr != nil {
		rollbackContext, cancelRollback := context.WithTimeout(context.Background(), 5*time.Second)
		rollbackErr := repository.Replace(rollbackContext, previous)
		cancelRollback()
		if rollbackErr != nil {
			handler.logger.Error().Str("error_type", "notification_reload_and_rollback").
				Msg("notification reload failed and persisted configuration rollback failed")
			handler.fail(writer, http.StatusInternalServerError, "notification reload failed and rollback failed")
			return
		}
		handler.logger.Error().Str("error_type", "notification_reload").
			Msg("notification reload failed; previous persisted configuration restored")
		handler.fail(writer, http.StatusInternalServerError, "notification reload failed; previous configuration restored")
		return
	}
	handler.ok(writer, notificationConfigToDTO(next))
}

func (handler *Handler) notificationsTest(writer http.ResponseWriter, request *http.Request) {
	var input notificationTestInput
	if err := decodeJSON(writer, request, &input); err != nil {
		handler.fail(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	previous, err := handler.deps.Runtime.Store.Notifications().Get(request.Context())
	if err != nil {
		handler.internal(writer, err)
		return
	}
	requested := notificationDTOToChannel(input.Channel)
	merged := mergeNotificationSecrets(notify.Config{Channels: []notify.ChannelConfig{requested}}, previous)
	if len(merged.Channels) != 1 {
		handler.fail(writer, http.StatusUnprocessableEntity, "invalid notification test channel")
		return
	}
	channel := merged.Channels[0]
	channel.Enabled = true
	channel.Decisions = []string{"deny"}
	result, err := sendNotificationTest(request.Context(), channel)
	if err != nil {
		handler.fail(writer, http.StatusUnprocessableEntity, "invalid notification test channel")
		return
	}
	handler.ok(writer, result)
}

func (handler *Handler) notificationsHealth(writer http.ResponseWriter, _ *http.Request) {
	if handler.deps.Runtime.Notifications == nil {
		handler.fail(writer, http.StatusServiceUnavailable, "notification manager is unavailable")
		return
	}
	statuses := handler.deps.Runtime.Notifications.Status()
	ids := make([]string, 0, len(statuses))
	for id := range statuses {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	view := notificationHealthView{Channels: make([]notificationHealthChannel, 0, len(ids))}
	for _, id := range ids {
		view.Channels = append(view.Channels, notificationHealthChannel{ID: id, ChannelStatus: statuses[id]})
	}
	handler.ok(writer, view)
}

func validateNotificationConfig(ctx context.Context, config notify.Config) error {
	validation := config
	validation.Enabled = true
	validation.Channels = append([]notify.ChannelConfig(nil), config.Channels...)
	for index := range validation.Channels {
		validation.Channels[index].Enabled = true
	}
	hub, err := eventbus.New(eventbus.Options{HistorySize: 1, SubscriberBuffer: 1})
	if err != nil {
		return err
	}
	manager := notify.NewManager(hub, notify.WithRetryPolicy(0, time.Millisecond, time.Millisecond))
	if err := manager.Start(ctx, validation); err != nil {
		hub.Close()
		return err
	}
	err = manager.Close()
	hub.Close()
	return err
}

func sendNotificationTest(parent context.Context, channel notify.ChannelConfig) (notificationTestResult, error) {
	config := notify.Config{Enabled: true, QueueSize: 1, Channels: []notify.ChannelConfig{channel}}
	hub, err := eventbus.New(eventbus.Options{HistorySize: 1, SubscriberBuffer: 1})
	if err != nil {
		return notificationTestResult{}, err
	}
	manager := notify.NewManager(
		hub,
		notify.WithHTTPTimeout(750*time.Millisecond),
		notify.WithRetryPolicy(0, time.Millisecond, time.Millisecond),
	)
	testContext, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	if err := manager.Start(testContext, config); err != nil {
		hub.Close()
		return notificationTestResult{}, err
	}
	defer func() {
		_ = manager.Close()
		hub.Close()
	}()
	hub.Publish(eventbus.Event{Audit: model.AuditLog{
		TS: time.Now().UTC(), Decision: "deny",
	}})
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		status := manager.Status()[channel.ID]
		if status.Sent > 0 {
			return notificationTestResult{ChannelID: channel.ID, Success: true, Category: "sent"}, nil
		}
		if status.Failed > 0 {
			category := status.LastError
			if category == "" {
				category = "send_error"
			}
			return notificationTestResult{ChannelID: channel.ID, Success: false, Category: category}, nil
		}
		select {
		case <-testContext.Done():
			return notificationTestResult{ChannelID: channel.ID, Success: false, Category: "timeout"}, nil
		case <-ticker.C:
		}
	}
}

func notificationConfigToDTO(config notify.Config) notificationConfigDTO {
	result := notificationConfigDTO{Enabled: config.Enabled, QueueSize: config.QueueSize,
		Channels: make([]notificationChannelDTO, 0, len(config.Channels))}
	for _, channel := range config.Channels {
		view := notificationChannelDTO{
			ID: channel.ID, Enabled: channel.Enabled, Kind: channel.Kind,
			Decisions: append([]string(nil), channel.Decisions...), IncludeSQL: channel.IncludeSQL,
			AllowPrivateEndpoints: channel.AllowPrivateEndpoints,
		}
		if channel.Webhook != nil {
			webhook := channel.Webhook
			view.Webhook = &notificationWebhookDTO{
				Template: webhook.Template,
				URL:      redactedNotificationValue(webhook.URL), URLConfigured: webhook.URL != "",
				BearerToken: redactedNotificationValue(webhook.BearerToken), BearerTokenConfigured: webhook.BearerToken != "",
				Headers: redactNotificationHeaders(webhook.Headers), HeadersConfigured: len(webhook.Headers) > 0,
				Secret: redactedNotificationValue(webhook.Secret), SecretConfigured: webhook.Secret != "",
			}
		}
		if channel.Syslog != nil {
			syslog := *channel.Syslog
			view.Syslog = &syslog
		}
		result.Channels = append(result.Channels, view)
	}
	return result
}

func notificationDTOToConfig(input notificationConfigDTO) notify.Config {
	config := notify.Config{Enabled: input.Enabled, QueueSize: input.QueueSize,
		Channels: make([]notify.ChannelConfig, 0, len(input.Channels))}
	for _, channel := range input.Channels {
		config.Channels = append(config.Channels, notificationDTOToChannel(channel))
	}
	return config
}

func notificationDTOToChannel(input notificationChannelDTO) notify.ChannelConfig {
	channel := notify.ChannelConfig{
		ID: input.ID, Enabled: input.Enabled, Kind: input.Kind,
		Decisions: append([]string(nil), input.Decisions...), IncludeSQL: input.IncludeSQL,
		AllowPrivateEndpoints: input.AllowPrivateEndpoints,
	}
	if input.Webhook != nil {
		channel.Webhook = &notify.WebhookConfig{
			Template: input.Webhook.Template, URL: input.Webhook.URL,
			BearerToken: input.Webhook.BearerToken, Secret: input.Webhook.Secret,
			Headers: cloneNotificationHeaders(input.Webhook.Headers),
		}
	}
	if input.Syslog != nil {
		syslog := *input.Syslog
		channel.Syslog = &syslog
	}
	return channel
}

func mergeNotificationSecrets(next, previous notify.Config) notify.Config {
	oldByID := make(map[string]notify.ChannelConfig, len(previous.Channels))
	for _, channel := range previous.Channels {
		oldByID[channel.ID] = channel
	}
	for index := range next.Channels {
		channel := &next.Channels[index]
		old, exists := oldByID[channel.ID]
		if !exists || channel.Webhook == nil || old.Webhook == nil {
			stripNotificationMasks(channel.Webhook)
			continue
		}
		if notificationValueIsBlankOrMask(channel.Webhook.URL) {
			channel.Webhook.URL = old.Webhook.URL
		}
		if notificationValueIsBlankOrMask(channel.Webhook.BearerToken) {
			channel.Webhook.BearerToken = old.Webhook.BearerToken
		}
		if notificationValueIsBlankOrMask(channel.Webhook.Secret) {
			channel.Webhook.Secret = old.Webhook.Secret
		}
		if len(channel.Webhook.Headers) == 0 && len(old.Webhook.Headers) > 0 {
			channel.Webhook.Headers = cloneNotificationHeaders(old.Webhook.Headers)
		} else {
			for name, value := range channel.Webhook.Headers {
				if !notificationValueIsBlankOrMask(value) {
					continue
				}
				if oldValue, found := notificationHeader(old.Webhook.Headers, name); found {
					channel.Webhook.Headers[name] = oldValue
				} else {
					delete(channel.Webhook.Headers, name)
				}
			}
		}
	}
	return next
}

func stripNotificationMasks(webhook *notify.WebhookConfig) {
	if webhook == nil {
		return
	}
	if strings.TrimSpace(webhook.URL) == notificationSecretMask {
		webhook.URL = ""
	}
	if strings.TrimSpace(webhook.BearerToken) == notificationSecretMask {
		webhook.BearerToken = ""
	}
	if strings.TrimSpace(webhook.Secret) == notificationSecretMask {
		webhook.Secret = ""
	}
	for name, value := range webhook.Headers {
		if strings.TrimSpace(value) == notificationSecretMask {
			delete(webhook.Headers, name)
		}
	}
}

func notificationValueIsBlankOrMask(value string) bool {
	trimmed := strings.TrimSpace(value)
	return trimmed == "" || trimmed == notificationSecretMask
}

func notificationHeader(headers map[string]string, name string) (string, bool) {
	for candidate, value := range headers {
		if strings.EqualFold(strings.TrimSpace(candidate), strings.TrimSpace(name)) {
			return value, true
		}
	}
	return "", false
}

func redactedNotificationValue(value string) string {
	if value == "" {
		return ""
	}
	return notificationSecretMask
}

func redactNotificationHeaders(headers map[string]string) map[string]string {
	if headers == nil {
		return nil
	}
	redacted := make(map[string]string, len(headers))
	for name := range headers {
		redacted[name] = notificationSecretMask
	}
	return redacted
}

func cloneNotificationHeaders(headers map[string]string) map[string]string {
	if headers == nil {
		return nil
	}
	cloned := make(map[string]string, len(headers))
	for name, value := range headers {
		cloned[name] = value
	}
	return cloned
}
