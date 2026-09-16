CREATE TABLE IF NOT EXISTS schema_migrations (
  version    BIGINT PRIMARY KEY,
  applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS audit_logs (
  id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  ts              TIMESTAMPTZ DEFAULT now(),
  agent_id        TEXT,
  datasource_id   TEXT,
  session_id      TEXT,
  conversation_id TEXT,
  mcp_tool        TEXT,
  db_type         TEXT,
  sql_raw         TEXT,
  sql_norm        TEXT,
  stmt_type       TEXT,
  objects         TEXT,
  decision        TEXT NOT NULL,
  rule_hits       TEXT,
  risk_level      INTEGER,
  est_rows        BIGINT,
  rows_returned   INTEGER,
  latency_ms      BIGINT,
  client_ip       TEXT,
  model_name      TEXT,
  error_msg       TEXT
);
CREATE INDEX IF NOT EXISTS idx_audit_ts ON audit_logs(ts);
CREATE INDEX IF NOT EXISTS idx_audit_agent_ts ON audit_logs(agent_id, ts);
CREATE INDEX IF NOT EXISTS idx_audit_decision ON audit_logs(decision);
