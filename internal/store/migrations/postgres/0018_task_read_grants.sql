CREATE UNIQUE INDEX ux_task_grant_agents_tenant ON agents(id, tenant_id);
CREATE UNIQUE INDEX ux_task_grant_datasources_tenant ON datasources(id, tenant_id);
CREATE TABLE task_read_grants (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL,
  issuer_user_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  datasource_id TEXT NOT NULL,
  sql_sha256 TEXT NOT NULL CHECK (length(sql_sha256) = 64),
  key_hash TEXT NOT NULL UNIQUE CHECK (length(key_hash) = 64),
  expires_at TIMESTAMPTZ NOT NULL,
  revoked_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  FOREIGN KEY (issuer_user_id, tenant_id) REFERENCES users(id, tenant_id) ON DELETE CASCADE,
  FOREIGN KEY (agent_id, tenant_id) REFERENCES agents(id, tenant_id) ON DELETE CASCADE,
  FOREIGN KEY (datasource_id, tenant_id) REFERENCES datasources(id, tenant_id) ON DELETE CASCADE
);
CREATE INDEX idx_task_read_grants_tenant ON task_read_grants(tenant_id, created_at, id);
CREATE INDEX idx_task_read_grants_agent ON task_read_grants(tenant_id, agent_id, expires_at);
