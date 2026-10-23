CREATE TABLE sqlbot_bindings (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL REFERENCES tenants(id),
  workspace_id TEXT NOT NULL,
  datasource_name TEXT NOT NULL,
  sqlbot_user_id TEXT NOT NULL,
  agentsql_user_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  datasource_id TEXT NOT NULL,
  schema_name TEXT NOT NULL,
  numeric_columns TEXT NOT NULL DEFAULT '[]',
  agent_key_env TEXT NOT NULL,
  connector_token_env TEXT NOT NULL,
  connector_token_hash TEXT NOT NULL UNIQUE,
  enabled BOOLEAN NOT NULL DEFAULT FALSE,
  revision BIGINT NOT NULL DEFAULT 1 CHECK(revision > 0),
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(tenant_id,workspace_id,datasource_name,sqlbot_user_id)
);
CREATE INDEX idx_sqlbot_bindings_tenant ON sqlbot_bindings(tenant_id,created_at);
CREATE INDEX idx_sqlbot_bindings_agent ON sqlbot_bindings(tenant_id,agent_id);
