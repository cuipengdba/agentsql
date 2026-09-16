PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS agents (
  id           TEXT PRIMARY KEY,
  name         TEXT NOT NULL,
  owner        TEXT,
  status       TEXT NOT NULL DEFAULT 'active',
  api_key_hash TEXT NOT NULL,
  level        TEXT NOT NULL DEFAULT 'readonly',
  expires_at   TIMESTAMP,
  created_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_agents_keyhash ON agents(api_key_hash);

CREATE TABLE IF NOT EXISTS datasources (
  id              TEXT PRIMARY KEY,
  name            TEXT NOT NULL,
  db_type         TEXT NOT NULL,
  host            TEXT NOT NULL,
  port            INTEGER NOT NULL,
  database        TEXT NOT NULL,
  username        TEXT NOT NULL,
  password_enc    TEXT NOT NULL,
  conn_limit      INTEGER DEFAULT 5,
  stmt_timeout_ms INTEGER DEFAULT 5000,
  row_limit       INTEGER DEFAULT 1000,
  created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS policies (
  id            TEXT PRIMARY KEY,
  agent_id      TEXT NOT NULL REFERENCES agents(id),
  datasource_id TEXT NOT NULL REFERENCES datasources(id),
  object_type   TEXT NOT NULL,
  object_name   TEXT NOT NULL,
  columns       TEXT,
  row_filter    TEXT,
  action        TEXT NOT NULL,
  created_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_policies_agent_ds ON policies(agent_id, datasource_id);

CREATE TABLE IF NOT EXISTS rules (
  id           TEXT PRIMARY KEY,
  db_type      TEXT NOT NULL,
  title        TEXT NOT NULL,
  risk_level   INTEGER NOT NULL,
  pattern_type TEXT NOT NULL,
  definition   TEXT NOT NULL,
  enabled      INTEGER DEFAULT 1,
  builtin      INTEGER DEFAULT 0,
  created_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS mask_rules (
  id             TEXT PRIMARY KEY,
  datasource_id  TEXT,
  table_name     TEXT NOT NULL,
  column_name    TEXT NOT NULL,
  sensitive_type TEXT NOT NULL,
  algo           TEXT NOT NULL,
  created_at     TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at     TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS approvals (
  id         TEXT PRIMARY KEY,
  audit_id   INTEGER,
  agent_id   TEXT,
  sql_raw    TEXT,
  reason     TEXT,
  status     TEXT DEFAULT 'pending',
  approver   TEXT,
  decided_at TIMESTAMP,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_approvals_status ON approvals(status);
