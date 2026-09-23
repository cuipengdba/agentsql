CREATE TABLE redaction_key_versions (
  id              TEXT PRIMARY KEY,
  state           TEXT NOT NULL CHECK (state IN ('standby','active','legacy','retired')),
  commitment      TEXT,
  label           TEXT,
  config_revision TEXT,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  activated_at    TIMESTAMPTZ,
  retired_at      TIMESTAMPTZ
);
CREATE UNIQUE INDEX ux_redaction_key_versions_active
  ON redaction_key_versions(state) WHERE state='active';

CREATE TABLE management_audit_outbox (
  event_uuid      TEXT PRIMARY KEY,
  action          TEXT NOT NULL,
  actor_type      TEXT NOT NULL,
  actor_id        TEXT NOT NULL,
  details_json    TEXT NOT NULL,
  created_at      TIMESTAMPTZ NOT NULL,
  attempts        INTEGER NOT NULL DEFAULT 0,
  claimed_by      TEXT,
  claimed_at      TIMESTAMPTZ,
  last_error      TEXT,
  next_attempt_at TIMESTAMPTZ NOT NULL,
  delivered_at    TIMESTAMPTZ
);
