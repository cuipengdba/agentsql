CREATE TABLE IF NOT EXISTS notification_settings (
  id         INTEGER PRIMARY KEY CHECK (id = 1),
  enabled    BOOLEAN NOT NULL DEFAULT FALSE,
  queue_size INTEGER NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS notification_channels (
  id                       TEXT PRIMARY KEY,
  position                 INTEGER NOT NULL UNIQUE CHECK (position >= 0),
  enabled                  BOOLEAN NOT NULL DEFAULT FALSE,
  kind                     TEXT NOT NULL,
  decisions                TEXT NOT NULL,
  include_sql              BOOLEAN NOT NULL DEFAULT FALSE,
  allow_private_endpoints  BOOLEAN NOT NULL DEFAULT FALSE,
  webhook_present          BOOLEAN NOT NULL DEFAULT FALSE,
  webhook_template         TEXT,
  webhook_url_enc          TEXT,
  webhook_bearer_token_enc TEXT,
  webhook_headers_enc      TEXT,
  webhook_secret_enc       TEXT,
  syslog_present           BOOLEAN NOT NULL DEFAULT FALSE,
  syslog_host              TEXT,
  syslog_port              INTEGER,
  syslog_transport         TEXT,
  syslog_facility          INTEGER,
  created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at               TIMESTAMPTZ NOT NULL DEFAULT now()
);
