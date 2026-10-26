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
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_managed_file_imports_open ON managed_file_imports(tenant_id,status,created_at);
CREATE INDEX idx_managed_file_imports_owner ON managed_file_imports(tenant_id,workspace_id,owner_user_id,created_at);
CREATE UNIQUE INDEX ux_managed_file_import_active_owner ON managed_file_imports(tenant_id,workspace_id,owner_user_id)
  WHERE status IN ('preparing','staged','policy_ready','cleanup_needed');
