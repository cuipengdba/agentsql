CREATE TABLE redaction_key_versions (
  id              TEXT PRIMARY KEY,
  state           TEXT NOT NULL CHECK (state IN ('standby','active','legacy','retired')),
  commitment      TEXT,
  label           TEXT,
  config_revision TEXT,
  created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  activated_at    TIMESTAMP,
  retired_at      TIMESTAMP
);
CREATE UNIQUE INDEX ux_redaction_key_versions_active
  ON redaction_key_versions(state) WHERE state='active';

CREATE TABLE management_audit_outbox (
  event_uuid      TEXT PRIMARY KEY,
  action          TEXT NOT NULL,
  actor_type      TEXT NOT NULL,
  actor_id        TEXT NOT NULL,
  details_json    TEXT NOT NULL,
  created_at      TIMESTAMP NOT NULL,
  attempts        INTEGER NOT NULL DEFAULT 0,
  claimed_by      TEXT,
  claimed_at      TIMESTAMP,
  last_error      TEXT,
  next_attempt_at TIMESTAMP NOT NULL,
  delivered_at    TIMESTAMP
);

ALTER TABLE audit_logs ADD COLUMN event_uuid TEXT;
CREATE UNIQUE INDEX ux_audit_logs_event_uuid
  ON audit_logs(event_uuid) WHERE event_uuid IS NOT NULL;
