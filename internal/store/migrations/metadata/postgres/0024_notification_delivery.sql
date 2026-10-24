ALTER TABLE notification_channels ADD COLUMN reliable_enabled BOOLEAN NOT NULL DEFAULT FALSE;
CREATE TABLE notification_delivery_cursors (
  tenant_id TEXT NOT NULL REFERENCES tenants(id),
  channel_id TEXT NOT NULL,
  last_seq BIGINT NOT NULL CHECK(last_seq >= 0),
  config_digest TEXT NOT NULL CHECK(length(config_digest) = 64),
  lease_token TEXT,
  lease_until_ms BIGINT NOT NULL DEFAULT 0 CHECK(lease_until_ms >= 0),
  failures INTEGER NOT NULL DEFAULT 0 CHECK(failures >= 0),
  retry_after_ms BIGINT NOT NULL DEFAULT 0 CHECK(retry_after_ms >= 0),
  dead_letter BOOLEAN NOT NULL DEFAULT FALSE,
  last_error_reason TEXT NOT NULL DEFAULT '',
  updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(tenant_id, channel_id),
  CHECK((lease_token IS NULL AND lease_until_ms = 0) OR (lease_token IS NOT NULL AND lease_until_ms > 0))
);
CREATE INDEX idx_notification_delivery_due
ON notification_delivery_cursors(dead_letter, retry_after_ms, lease_until_ms);
