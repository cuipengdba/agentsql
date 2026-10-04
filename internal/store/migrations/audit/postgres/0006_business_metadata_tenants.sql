-- Independent audit stores deliberately do not contain the RBAC tenants table.
-- Ownership is still mandatory and is validated by the metadata/auth boundary.
ALTER TABLE audit_logs ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'tenant_default';
ALTER TABLE chain_state ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'tenant_default';
ALTER TABLE chain_verification ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'tenant_default';
ALTER TABLE chain_state DROP CONSTRAINT chain_state_pkey;
ALTER TABLE chain_state ADD PRIMARY KEY(tenant_id,chain_id);
ALTER TABLE chain_verification DROP CONSTRAINT chain_verification_pkey;
ALTER TABLE chain_verification ADD PRIMARY KEY(tenant_id,chain_id);
CREATE INDEX idx_audit_logs_tenant ON audit_logs(tenant_id);
CREATE INDEX idx_chain_state_tenant ON chain_state(tenant_id);
CREATE INDEX idx_chain_verification_tenant ON chain_verification(tenant_id);
