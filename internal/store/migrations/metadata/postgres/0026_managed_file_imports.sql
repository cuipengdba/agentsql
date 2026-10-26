CREATE TABLE managed_file_imports (
  id TEXT PRIMARY KEY CHECK(length(id)=32),
  tenant_id TEXT NOT NULL REFERENCES tenants(id),
  binding_id TEXT NOT NULL,
  binding_revision BIGINT NOT NULL CHECK(binding_revision > 0),
  workspace_id TEXT NOT NULL,
  owner_user_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  datasource_id TEXT NOT NULL,
  file_sha256 TEXT NOT NULL CHECK(length(file_sha256)=64),
  file_bytes BIGINT NOT NULL CHECK(file_bytes > 0 AND file_bytes <= 4194304),
  sheet_count INTEGER NOT NULL CHECK(sheet_count > 0 AND sheet_count <= 8),
  status TEXT NOT NULL DEFAULT 'preparing' CHECK(status IN ('preparing','staged','policy_ready','published','deleting','deleted','cleanup_needed')),
  revision BIGINT NOT NULL DEFAULT 1 CHECK(revision > 0),
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_managed_file_imports_open ON managed_file_imports(tenant_id,status,created_at);
CREATE INDEX idx_managed_file_imports_owner ON managed_file_imports(tenant_id,workspace_id,owner_user_id,created_at);
CREATE UNIQUE INDEX ux_managed_file_import_active_owner ON managed_file_imports(tenant_id,workspace_id,owner_user_id)
  WHERE status IN ('preparing','staged','policy_ready','cleanup_needed');
CREATE TABLE managed_file_tables (
  id TEXT PRIMARY KEY,
  operation_id TEXT NOT NULL REFERENCES managed_file_imports(id) ON DELETE RESTRICT,
  tenant_id TEXT NOT NULL REFERENCES tenants(id),
  sheet_index INTEGER NOT NULL CHECK(sheet_index >= 0 AND sheet_index < 8),
  table_name TEXT NOT NULL CHECK(length(table_name)=46),
  row_count INTEGER NOT NULL CHECK(row_count >= 0 AND row_count <= 1000),
  policy_id TEXT NOT NULL DEFAULT '',
  UNIQUE(operation_id,sheet_index),
  UNIQUE(tenant_id,table_name)
);
CREATE UNIQUE INDEX ux_managed_file_tables_policy ON managed_file_tables(tenant_id,policy_id) WHERE policy_id <> '';
