-- Batch 36: every persisted control-plane business row belongs to a tenant.
-- Historical rows have no trustworthy ownership signal, so they belong to the
-- bootstrap/default tenant created by 0011.

INSERT INTO tenants(id,name,status) VALUES('tenant_default','Default','active') ON CONFLICT(id) DO NOTHING;

ALTER TABLE agents ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'tenant_default' REFERENCES tenants(id) ON DELETE RESTRICT;
ALTER TABLE datasources ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'tenant_default' REFERENCES tenants(id) ON DELETE RESTRICT;
ALTER TABLE policies ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'tenant_default' REFERENCES tenants(id) ON DELETE RESTRICT;
ALTER TABLE rules ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'tenant_default' REFERENCES tenants(id) ON DELETE RESTRICT;
ALTER TABLE mask_rules ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'tenant_default' REFERENCES tenants(id) ON DELETE RESTRICT;
ALTER TABLE audit_logs ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'tenant_default' REFERENCES tenants(id) ON DELETE RESTRICT;
ALTER TABLE approvals ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'tenant_default' REFERENCES tenants(id) ON DELETE RESTRICT;
ALTER TABLE notification_settings ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'tenant_default' REFERENCES tenants(id) ON DELETE RESTRICT;
ALTER TABLE notification_channels ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'tenant_default' REFERENCES tenants(id) ON DELETE RESTRICT;
ALTER TABLE redaction_key_versions ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'tenant_default' REFERENCES tenants(id) ON DELETE RESTRICT;
ALTER TABLE management_audit_outbox ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'tenant_default' REFERENCES tenants(id) ON DELETE RESTRICT;
ALTER TABLE chain_state ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'tenant_default' REFERENCES tenants(id) ON DELETE RESTRICT;
ALTER TABLE chain_verification ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'tenant_default' REFERENCES tenants(id) ON DELETE RESTRICT;
ALTER TABLE relation_policy_bindings ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'tenant_default' REFERENCES tenants(id) ON DELETE RESTRICT;
ALTER TABLE policy_column_permission_staging ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'tenant_default' REFERENCES tenants(id) ON DELETE RESTRICT;
ALTER TABLE policy_column_permissions ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'tenant_default' REFERENCES tenants(id) ON DELETE RESTRICT;
UPDATE b5_sessions SET tenant_id='tenant_default';
ALTER TABLE b5_transactions ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'tenant_default' REFERENCES tenants(id) ON DELETE RESTRICT;
ALTER TABLE b5_dml_grants ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'tenant_default' REFERENCES tenants(id) ON DELETE RESTRICT;
ALTER TABLE b5_result_receipts ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'tenant_default' REFERENCES tenants(id) ON DELETE RESTRICT;
ALTER TABLE b5_tx_events ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'tenant_default' REFERENCES tenants(id) ON DELETE RESTRICT;

DROP INDEX ux_mask_rules_scope_column;
CREATE UNIQUE INDEX ux_mask_rules_scope_column ON mask_rules(tenant_id, COALESCE(NULLIF(TRIM(datasource_id),''),''), COALESCE(NULLIF(TRIM(schema_name),''),''), COALESCE(NULLIF(TRIM(table_name),''),''), LOWER(TRIM(column_name)));
DROP INDEX ux_redaction_key_versions_active;
CREATE UNIQUE INDEX ux_redaction_key_versions_active ON redaction_key_versions(tenant_id) WHERE state='active';
ALTER TABLE notification_settings DROP CONSTRAINT notification_settings_pkey;
ALTER TABLE notification_settings ADD PRIMARY KEY (tenant_id,id);
ALTER TABLE notification_channels DROP CONSTRAINT notification_channels_position_key;
ALTER TABLE notification_channels ADD CONSTRAINT notification_channels_tenant_position_key UNIQUE(tenant_id,position);
ALTER TABLE notification_channels DROP CONSTRAINT notification_channels_pkey;
ALTER TABLE notification_channels ADD PRIMARY KEY(tenant_id,id);
ALTER TABLE chain_state DROP CONSTRAINT chain_state_pkey;
ALTER TABLE chain_state ADD PRIMARY KEY(tenant_id,chain_id);
ALTER TABLE chain_verification DROP CONSTRAINT chain_verification_pkey;
ALTER TABLE chain_verification ADD PRIMARY KEY(tenant_id,chain_id);

CREATE INDEX idx_agents_tenant ON agents(tenant_id);
CREATE INDEX idx_datasources_tenant ON datasources(tenant_id);
CREATE INDEX idx_policies_tenant ON policies(tenant_id);
CREATE INDEX idx_rules_tenant ON rules(tenant_id);
CREATE INDEX idx_mask_rules_tenant ON mask_rules(tenant_id);
CREATE INDEX idx_audit_logs_tenant ON audit_logs(tenant_id);
CREATE INDEX idx_approvals_tenant ON approvals(tenant_id);
CREATE INDEX idx_notification_settings_tenant ON notification_settings(tenant_id);
CREATE INDEX idx_notification_channels_tenant ON notification_channels(tenant_id);
CREATE INDEX idx_redaction_key_versions_tenant ON redaction_key_versions(tenant_id);
CREATE INDEX idx_management_audit_outbox_tenant ON management_audit_outbox(tenant_id);
CREATE INDEX idx_chain_state_tenant ON chain_state(tenant_id);
CREATE INDEX idx_chain_verification_tenant ON chain_verification(tenant_id);
CREATE INDEX idx_relation_policy_bindings_tenant ON relation_policy_bindings(tenant_id);
CREATE INDEX idx_policy_column_permission_staging_tenant ON policy_column_permission_staging(tenant_id);
CREATE INDEX idx_policy_column_permissions_tenant ON policy_column_permissions(tenant_id);
CREATE INDEX idx_b5_sessions_tenant ON b5_sessions(tenant_id);
CREATE INDEX idx_b5_transactions_tenant ON b5_transactions(tenant_id);
CREATE INDEX idx_b5_dml_grants_tenant ON b5_dml_grants(tenant_id);
CREATE INDEX idx_b5_result_receipts_tenant ON b5_result_receipts(tenant_id);
CREATE INDEX idx_b5_tx_events_tenant ON b5_tx_events(tenant_id);
