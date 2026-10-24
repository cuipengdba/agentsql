package bootstrap

import (
	"context"
	"sync"
	"time"

	"github.com/cuipengdba/agentsql/internal/notify"
	"github.com/cuipengdba/agentsql/internal/store"
)

const (
	reliableNotificationPollInterval = time.Second
	reliableNotificationLease        = 30 * time.Second
	reliableNotificationMaxFailures  = 5
	reliableNotificationBatch        = 16
)

type reliableNotificationHealth struct {
	mu        sync.RWMutex
	lastError string
	lastPoll  time.Time
}

func (state *reliableNotificationHealth) set(reason string) {
	state.mu.Lock()
	state.lastError = reason
	state.lastPoll = time.Now().UTC()
	state.mu.Unlock()
}

func (state *reliableNotificationHealth) snapshot() (string, time.Time) {
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.lastError, state.lastPoll
}

func (runtime *Runtime) startReliableNotifications() {
	ctx, cancel := context.WithCancel(context.Background())
	runtime.reliableCancel = cancel
	runtime.reliableWait.Add(1)
	go func() {
		defer runtime.reliableWait.Done()
		ticker := time.NewTicker(reliableNotificationPollInterval)
		defer ticker.Stop()
		for {
			runtime.pollReliableNotifications(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (runtime *Runtime) pollReliableNotifications(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	tenants, err := runtime.Store.Notifications().ListReliableTenants(ctx)
	if err != nil {
		runtime.reliableHealth.set("inventory_error")
		return
	}
	runtime.reliableHealth.set("")
	for _, tenantID := range tenants {
		if ctx.Err() != nil {
			return
		}
		tenantContext, err := store.WithTenant(ctx, tenantID)
		if err != nil {
			runtime.reliableHealth.set("tenant_error")
			continue
		}
		config, err := runtime.Store.Notifications().Get(tenantContext)
		if err != nil {
			runtime.reliableHealth.set("config_error")
			continue
		}
		if !config.Enabled {
			continue
		}
		for _, channel := range config.Channels {
			if ctx.Err() != nil {
				return
			}
			if !channel.Enabled || !channel.Reliable || channel.Kind != notify.ChannelWebhook {
				continue
			}
			runtime.drainReliableChannel(tenantContext, tenantID, channel)
		}
	}
}

func (runtime *Runtime) drainReliableChannel(ctx context.Context, tenantID string, channel notify.ChannelConfig) {
	digest, err := notify.ChannelDigest(channel)
	if err != nil {
		runtime.reliableHealth.set("config_error")
		return
	}
	delivery := runtime.Store.NotificationDelivery()
	if err := delivery.AssertConfig(ctx, channel.ID, digest); err != nil {
		runtime.reliableHealth.set("cursor_config_error")
		return
	}
	for range reliableNotificationBatch {
		if ctx.Err() != nil {
			return
		}
		claim, err := delivery.Claim(ctx, channel.ID, digest, time.Now().UTC(), reliableNotificationLease)
		if err != nil {
			runtime.reliableHealth.set("cursor_error")
			return
		}
		if claim == nil {
			return
		}
		page, err := runtime.Store.AuditLogs().StreamAfter(ctx, claim.LastSequence, 1)
		if err != nil {
			runtime.failReliableClaim(ctx, delivery, channel.ID, *claim, "unknown")
			runtime.reliableHealth.set("audit_stream_error")
			return
		}
		if len(page) == 0 {
			released, releaseErr := delivery.Release(ctx, channel.ID, *claim, time.Now().UTC())
			if releaseErr != nil || !released {
				runtime.reliableHealth.set("cursor_release_error")
			}
			return
		}
		item := page[0]
		if notify.EligibleForChannel(channel, item.Audit) {
			id, err := notify.DeliveryID(tenantID, channel.ID, item.Sequence)
			if err != nil {
				runtime.failReliableClaim(ctx, delivery, channel.ID, *claim, "unknown")
				return
			}
			if reason := notify.DeliverDurableWebhook(ctx, channel, item.Audit, id, runtime.names); reason != "" {
				runtime.failReliableClaim(ctx, delivery, channel.ID, *claim, reason)
				if runtime.Metrics != nil {
					runtime.Metrics.RecordNotificationFailed(channel.ID, reason)
				}
				return
			}
		}
		acknowledged, err := delivery.Acknowledge(ctx, channel.ID, *claim, item.Sequence, time.Now().UTC())
		if err != nil || !acknowledged {
			runtime.reliableHealth.set("cursor_ack_error")
			return
		}
		if notify.EligibleForChannel(channel, item.Audit) && runtime.Metrics != nil {
			runtime.Metrics.RecordNotificationSent(channel.ID, time.Now().UTC())
		}
	}
}

func (runtime *Runtime) failReliableClaim(ctx context.Context, delivery *store.NotificationDeliveryRepository, channelID string, claim store.NotificationDeliveryClaim, reason string) {
	now := time.Now().UTC()
	changed, err := delivery.Fail(ctx, channelID, claim, reason, now, now.Add(notify.RetryDelay(claim.Failures+1)), reliableNotificationMaxFailures)
	if err != nil || !changed {
		runtime.reliableHealth.set("cursor_fail_error")
	}
}
