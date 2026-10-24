package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/cuipengdba/agentsql/internal/notify"
)

const notificationSettingsID = 1

// NotificationRepository stores the complete notification configuration in
// the metadata database. Confidential webhook values are encrypted at rest.
type NotificationRepository struct {
	repositoryBase
	cipher *PasswordCipher
}

// ListReliableTenants is an internal worker inventory. It never exposes
// cross-tenant configuration to an authenticated request.
func (repository *NotificationRepository) ListReliableTenants(ctx context.Context) ([]string, error) {
	if ctx == nil {
		return nil, fmt.Errorf("list reliable notification tenants: %w", ErrNilContext)
	}
	rows, err := repository.db.QueryContext(ctx, repository.bind(`
SELECT DISTINCT c.tenant_id
FROM notification_channels c JOIN notification_settings s ON s.tenant_id=c.tenant_id
WHERE s.enabled=? AND c.enabled=? AND c.reliable_enabled=? AND c.kind='webhook'
ORDER BY c.tenant_id LIMIT 1001`), true, true, true)
	if err != nil {
		return nil, fmt.Errorf("list reliable notification tenants: %w", err)
	}
	defer rows.Close()
	var tenants []string
	for rows.Next() {
		var tenant string
		if err := rows.Scan(&tenant); err != nil {
			return nil, fmt.Errorf("scan reliable notification tenant: %w", err)
		}
		tenants = append(tenants, tenant)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("finish reliable notification tenants: %w", err)
	}
	if len(tenants) > 1000 {
		return nil, errors.New("reliable notification tenant inventory exceeds limit")
	}
	return tenants, nil
}

// Get returns the persisted configuration. An unconfigured store has the safe
// default: notifications disabled and no channels.
func (repository *NotificationRepository) Get(ctx context.Context) (notify.Config, error) {
	if ctx == nil {
		return notify.Config{}, fmt.Errorf("get notification configuration: %w", ErrNilContext)
	}
	tenantID, tenantErr := repository.requireTenant(ctx, "get notification configuration")
	if tenantErr != nil {
		return notify.Config{}, tenantErr
	}
	var config notify.Config
	var enabled databaseBool
	err := repository.db.QueryRowContext(ctx, repository.bind(`
SELECT enabled, queue_size
FROM notification_settings
WHERE id = ? AND tenant_id = ?`), notificationSettingsID, tenantID).Scan(&enabled, &config.QueueSize)
	if errors.Is(err, sql.ErrNoRows) {
		return notify.Config{}, nil
	}
	if err != nil {
		return notify.Config{}, fmt.Errorf("get notification settings: %w", err)
	}
	config.Enabled = enabled.value

	rows, err := repository.db.QueryContext(ctx, repository.bind(`
SELECT id, enabled, reliable_enabled, kind, decisions, include_sql, allow_private_endpoints,
       webhook_present, webhook_template, webhook_url_enc,
       webhook_bearer_token_enc, webhook_headers_enc, webhook_secret_enc,
       syslog_present, syslog_host, syslog_port, syslog_transport,
       syslog_facility
FROM notification_channels
WHERE tenant_id = ?
ORDER BY position ASC`), tenantID)
	if err != nil {
		return notify.Config{}, fmt.Errorf("list notification channels: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		channel, scanErr := repository.scanChannel(rows)
		if scanErr != nil {
			return notify.Config{}, fmt.Errorf("scan notification channel: %w", scanErr)
		}
		config.Channels = append(config.Channels, channel)
	}
	if err := rows.Err(); err != nil {
		return notify.Config{}, fmt.Errorf("finish notification channels: %w", err)
	}
	return config, nil
}

// Replace atomically replaces the complete notification configuration.
func (repository *NotificationRepository) Replace(ctx context.Context, config notify.Config) (resultErr error) {
	return repository.replace(ctx, config, nil, nil)
}

// NotificationCursorSeed is the committed audit-stream tail captured before
// making a reliable channel visible to workers.
type NotificationCursorSeed struct {
	ChannelID       string
	ConfigDigest    string
	InitialSequence int64
}

// ReplaceWithCursors publishes configuration and its initial durable cursors
// in one metadata transaction. The audit database may be separate, so the
// caller must read the tail before this call; a gap can replay, never skip.
func (repository *NotificationRepository) ReplaceWithCursors(ctx context.Context, config notify.Config, seeds []NotificationCursorSeed) ([]NotificationCursorSeed, error) {
	created := make([]NotificationCursorSeed, 0, len(seeds))
	if err := repository.replace(ctx, config, seeds, &created); err != nil {
		return nil, err
	}
	return created, nil
}

func (repository *NotificationRepository) replace(ctx context.Context, config notify.Config, seeds []NotificationCursorSeed, created *[]NotificationCursorSeed) (resultErr error) {
	if ctx == nil {
		return fmt.Errorf("replace notification configuration: %w", ErrNilContext)
	}
	tenantID, tenantErr := repository.requireTenant(ctx, "replace notification configuration")
	if tenantErr != nil {
		return tenantErr
	}
	prepared := make([]storedNotificationChannel, len(config.Channels))
	seen := make(map[string]struct{}, len(config.Channels))
	for index, channel := range config.Channels {
		if _, exists := seen[channel.ID]; exists {
			return fmt.Errorf("replace notification configuration: duplicate channel id %q", channel.ID)
		}
		seen[channel.ID] = struct{}{}
		stored, err := repository.prepareChannel(channel)
		if err != nil {
			return fmt.Errorf("prepare notification channel %q: %w", channel.ID, err)
		}
		stored.position = index
		prepared[index] = stored
	}

	transaction, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin notification configuration replacement: %w", err)
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, transaction.Rollback())
		}
	}()
	if seeds != nil {
		if err := repository.prepareCursors(ctx, transaction, tenantID, config, seeds, created); err != nil {
			return err
		}
	}
	_, err = transaction.ExecContext(ctx, repository.bind(`
INSERT INTO notification_settings (tenant_id, id, enabled, queue_size)
VALUES (?, ?, ?, ?)
ON CONFLICT(tenant_id,id) DO UPDATE SET
  enabled = excluded.enabled,
  queue_size = excluded.queue_size,
  updated_at = CURRENT_TIMESTAMP`), tenantID, notificationSettingsID, config.Enabled, config.QueueSize)
	if err != nil {
		return fmt.Errorf("upsert notification settings: %w", err)
	}
	positionShift := len(prepared) + 1
	if _, err := transaction.ExecContext(ctx, repository.bind(
		"UPDATE notification_channels SET position = position + ? WHERE tenant_id = ?",
	), positionShift, tenantID); err != nil {
		return fmt.Errorf("stage notification channels for replacement: %w", err)
	}
	for _, channel := range prepared {
		if err := repository.insertChannel(ctx, transaction, tenantID, channel); err != nil {
			return err
		}
	}
	if _, err := transaction.ExecContext(ctx, repository.bind(
		"DELETE FROM notification_channels WHERE tenant_id = ? AND position >= ?",
	), tenantID, positionShift); err != nil {
		return fmt.Errorf("remove stale notification channels: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit notification configuration replacement: %w", err)
	}
	return nil
}

func (repository *NotificationRepository) prepareCursors(ctx context.Context, transaction *sql.Tx, tenantID string, config notify.Config, seeds []NotificationCursorSeed, created *[]NotificationCursorSeed) error {
	expected := make(map[string]bool)
	if config.Enabled {
		for _, channel := range config.Channels {
			if channel.Enabled && channel.Reliable && channel.Kind == notify.ChannelWebhook {
				expected[channel.ID] = true
			}
		}
	}
	if len(seeds) != len(expected) {
		return errors.New("reliable notification cursor seed count differs from channels")
	}
	for _, seed := range seeds {
		if !expected[seed.ChannelID] || !validNotificationDeliveryIdentity(seed.ChannelID, seed.ConfigDigest) || seed.InitialSequence < 0 {
			return errors.New("invalid reliable notification cursor seed")
		}
		delete(expected, seed.ChannelID)
		result, err := transaction.ExecContext(ctx, repository.bind(`
INSERT INTO notification_delivery_cursors(tenant_id,channel_id,last_seq,config_digest)
VALUES(?,?,?,?) ON CONFLICT(tenant_id,channel_id) DO NOTHING`),
			tenantID, seed.ChannelID, seed.InitialSequence, seed.ConfigDigest)
		if err != nil {
			return fmt.Errorf("prepare reliable notification cursor: %w", err)
		}
		inserted, err := changedRow(result)
		if err != nil {
			return fmt.Errorf("check reliable notification cursor: %w", err)
		}
		if inserted && created != nil {
			*created = append(*created, seed)
		}
		var existing string
		if err := transaction.QueryRowContext(ctx, repository.bind(`
SELECT config_digest FROM notification_delivery_cursors WHERE tenant_id=? AND channel_id=?`),
			tenantID, seed.ChannelID).Scan(&existing); err != nil {
			return fmt.Errorf("verify reliable notification cursor: %w", err)
		}
		if existing != seed.ConfigDigest {
			return errors.New("notification delivery configuration changed; resolve pending delivery before rebinding channel")
		}
	}
	return nil
}

type storedNotificationChannel struct {
	channel                                                  notify.ChannelConfig
	position                                                 int
	decisions                                                string
	webhookPresent                                           bool
	webhookURL, webhookBearer, webhookHeaders, webhookSecret string
	syslogPresent                                            bool
}

func (repository *NotificationRepository) prepareChannel(channel notify.ChannelConfig) (storedNotificationChannel, error) {
	decisions, err := json.Marshal(channel.Decisions)
	if err != nil {
		return storedNotificationChannel{}, fmt.Errorf("encode decisions: %w", err)
	}
	stored := storedNotificationChannel{channel: channel, decisions: string(decisions)}
	if channel.Webhook != nil {
		stored.webhookPresent = true
		headers, err := json.Marshal(channel.Webhook.Headers)
		if err != nil {
			return storedNotificationChannel{}, fmt.Errorf("encode webhook headers: %w", err)
		}
		plaintexts := []string{channel.Webhook.URL, channel.Webhook.BearerToken, string(headers), channel.Webhook.Secret}
		encrypted := make([]string, len(plaintexts))
		for index, plaintext := range plaintexts {
			encrypted[index], err = repository.cipher.encryptNotification(plaintext)
			if err != nil {
				return storedNotificationChannel{}, fmt.Errorf("encrypt webhook field: %w", err)
			}
		}
		stored.webhookURL, stored.webhookBearer = encrypted[0], encrypted[1]
		stored.webhookHeaders, stored.webhookSecret = encrypted[2], encrypted[3]
	}
	stored.syslogPresent = channel.Syslog != nil
	return stored, nil
}

func (repository *NotificationRepository) insertChannel(ctx context.Context, transaction *sql.Tx, tenantID string, stored storedNotificationChannel) error {
	channel := stored.channel
	var webhookTemplate any
	if channel.Webhook != nil {
		webhookTemplate = string(channel.Webhook.Template)
	}
	var syslogHost, syslogPort, syslogTransport, syslogFacility any
	if channel.Syslog != nil {
		syslogHost, syslogPort = channel.Syslog.Host, channel.Syslog.Port
		syslogTransport, syslogFacility = channel.Syslog.Transport, channel.Syslog.Facility
	}
	// Existing rows are updated after their positions have been shifted out of
	// the replacement range, so channel reordering cannot violate UNIQUE(position).
	_, err := transaction.ExecContext(ctx, repository.bind(`
INSERT INTO notification_channels (
  tenant_id, id, position, enabled, reliable_enabled, kind, decisions, include_sql,
  allow_private_endpoints, webhook_present, webhook_template,
  webhook_url_enc, webhook_bearer_token_enc, webhook_headers_enc,
  webhook_secret_enc, syslog_present, syslog_host, syslog_port,
  syslog_transport, syslog_facility
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(tenant_id,id) DO UPDATE SET
  position = excluded.position,
  enabled = excluded.enabled,
  reliable_enabled = excluded.reliable_enabled,
  kind = excluded.kind,
  decisions = excluded.decisions,
  include_sql = excluded.include_sql,
  allow_private_endpoints = excluded.allow_private_endpoints,
  webhook_present = excluded.webhook_present,
  webhook_template = excluded.webhook_template,
  webhook_url_enc = excluded.webhook_url_enc,
  webhook_bearer_token_enc = excluded.webhook_bearer_token_enc,
  webhook_headers_enc = excluded.webhook_headers_enc,
  webhook_secret_enc = excluded.webhook_secret_enc,
  syslog_present = excluded.syslog_present,
  syslog_host = excluded.syslog_host,
  syslog_port = excluded.syslog_port,
  syslog_transport = excluded.syslog_transport,
  syslog_facility = excluded.syslog_facility,
  updated_at = CURRENT_TIMESTAMP`),
		tenantID, channel.ID, stored.position, channel.Enabled, channel.Reliable, string(channel.Kind), stored.decisions,
		channel.IncludeSQL, channel.AllowPrivateEndpoints, stored.webhookPresent,
		webhookTemplate, nullableEncrypted(stored.webhookPresent, stored.webhookURL),
		nullableEncrypted(stored.webhookPresent, stored.webhookBearer),
		nullableEncrypted(stored.webhookPresent, stored.webhookHeaders),
		nullableEncrypted(stored.webhookPresent, stored.webhookSecret),
		stored.syslogPresent, syslogHost, syslogPort, syslogTransport, syslogFacility,
	)
	if err != nil {
		return fmt.Errorf("insert notification channel %q: %w", channel.ID, err)
	}
	return nil
}

func nullableEncrypted(present bool, value string) any {
	if !present {
		return nil
	}
	return value
}

func (repository *NotificationRepository) scanChannel(scanner rowScanner) (notify.ChannelConfig, error) {
	var channel notify.ChannelConfig
	var enabled, reliable, includeSQL, allowPrivate, webhookPresent, syslogPresent databaseBool
	var decisions string
	var webhookTemplate, webhookURL, webhookBearer, webhookHeaders, webhookSecret sql.NullString
	var syslogHost, syslogTransport sql.NullString
	var syslogPort, syslogFacility sql.NullInt64
	if err := scanner.Scan(
		&channel.ID, &enabled, &reliable, &channel.Kind, &decisions, &includeSQL, &allowPrivate,
		&webhookPresent, &webhookTemplate, &webhookURL, &webhookBearer,
		&webhookHeaders, &webhookSecret, &syslogPresent, &syslogHost,
		&syslogPort, &syslogTransport, &syslogFacility,
	); err != nil {
		return notify.ChannelConfig{}, err
	}
	channel.Enabled = enabled.value
	channel.Reliable = reliable.value
	channel.IncludeSQL = includeSQL.value
	channel.AllowPrivateEndpoints = allowPrivate.value
	if err := json.Unmarshal([]byte(decisions), &channel.Decisions); err != nil {
		return notify.ChannelConfig{}, fmt.Errorf("decode decisions: %w", err)
	}
	if webhookPresent.value {
		ciphertexts := []sql.NullString{webhookURL, webhookBearer, webhookHeaders, webhookSecret}
		plaintexts := make([]string, len(ciphertexts))
		for index, ciphertext := range ciphertexts {
			if !ciphertext.Valid {
				return notify.ChannelConfig{}, errors.New("webhook ciphertext is missing")
			}
			plaintext, err := repository.cipher.decryptNotification(ciphertext.String)
			if err != nil {
				return notify.ChannelConfig{}, fmt.Errorf("decrypt webhook field: %w", err)
			}
			plaintexts[index] = plaintext
		}
		webhook := &notify.WebhookConfig{
			Template: notify.WebhookTemplate(webhookTemplate.String), URL: plaintexts[0],
			BearerToken: plaintexts[1], Secret: plaintexts[3],
		}
		if err := json.Unmarshal([]byte(plaintexts[2]), &webhook.Headers); err != nil {
			return notify.ChannelConfig{}, fmt.Errorf("decode webhook headers: %w", err)
		}
		channel.Webhook = webhook
	}
	if syslogPresent.value {
		if !syslogHost.Valid || !syslogPort.Valid || !syslogTransport.Valid || !syslogFacility.Valid {
			return notify.ChannelConfig{}, errors.New("syslog configuration is incomplete")
		}
		channel.Syslog = &notify.SyslogConfig{
			Host: syslogHost.String, Port: int(syslogPort.Int64),
			Transport: syslogTransport.String, Facility: int(syslogFacility.Int64),
		}
	}
	return channel, nil
}
