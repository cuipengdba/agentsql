CREATE TABLE IF NOT EXISTS notification_settings (
  id         INTEGER PRIMARY KEY CHECK (id = 1),
  enabled    INTEGER NOT NULL DEFAULT 0,
  queue_size INTEGER NOT NULL DEFAULT 0,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS notification_channels (
  id                      TEXT PRIMARY KEY,
  position                INTEGER NOT NULL CHECK (position >= 0),
  enabled                 INTEGER NOT NULL DEFAULT 0,
  kind                    TEXT NOT NULL,
  decisions               TEXT NOT NULL,
  include_sql             INTEGER NOT NULL DEFAULT 0,
  allow_private_endpoints INTEGER NOT NULL DEFAULT 0,
  webhook_present         INTEGER NOT NULL DEFAULT 0,
  webhook_template        TEXT,
  webhook_url_enc         TEXT,
  webhook_bearer_token_enc TEXT,
  webhook_headers_enc     TEXT,
  webhook_secret_enc      TEXT,
  syslog_present          INTEGER NOT NULL DEFAULT 0,
  syslog_host             TEXT,
  syslog_port             INTEGER,
  syslog_transport        TEXT,
  syslog_facility         INTEGER,
  created_at              TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at              TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(position)
);
