package notify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
)

// ChannelDigest binds a durable cursor to its exact channel configuration,
// including the selected endpoint and secrets, without storing those values
// in cursor metadata.
func ChannelDigest(channel ChannelConfig) (string, error) {
	if channel.ID == "" || channel.Kind != ChannelWebhook || channel.Webhook == nil || !channel.Reliable {
		return "", errors.New("invalid reliable webhook channel")
	}
	encoded, err := json.Marshal(channel)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(append([]byte("agentsql-reliable-channel/v1\x00"), encoded...))
	return hex.EncodeToString(digest[:]), nil
}

// EligibleForChannel excludes intent records and applies the channel's final
// decision filter. Skipped audit records still advance a durable cursor.
func EligibleForChannel(channel ChannelConfig, audit model.AuditLog) bool {
	return !isIntentAudit(audit) && matchesDecision(channel, audit.Decision)
}

// DeliverDurableWebhook sends one attempt and returns only a bounded failure
// category. A caller must persist retry state and never log the raw endpoint,
// payload, SQL, or transport error.
func DeliverDurableWebhook(ctx context.Context, channel ChannelConfig, audit model.AuditLog, deliveryID string, resolver NameResolver) string {
	if ctx == nil || deliveryID == "" || channel.Kind != ChannelWebhook || channel.Webhook == nil || !channel.Reliable {
		return "send_error"
	}
	options := defaultManagerOptions()
	sender, err := newWebhookSender(ctx, channel, options)
	if err != nil {
		return failureReason(err)
	}
	defer sender.Close()
	payload := project(ctx, audit, channel.IncludeSQL, resolver)
	payload.DeliveryID = deliveryID
	attempt, cancel := context.WithTimeout(ctx, options.httpTimeout)
	defer cancel()
	if err := sender.Send(attempt, payload); err != nil {
		return failureReason(err)
	}
	return ""
}

// RetryDelay bounds durable retries independently from the process-local
// best-effort worker. The caller stores the resulting absolute due time.
func RetryDelay(failures int) time.Duration {
	if failures < 1 {
		return time.Second
	}
	delay := time.Second
	for i := 1; i < failures && delay < time.Minute; i++ {
		delay *= 2
	}
	if delay > time.Minute {
		return time.Minute
	}
	return delay
}
