DO $guard$
BEGIN
    IF EXISTS (SELECT 1 FROM agents WHERE tenant_id <> 'tenant_default')
       OR EXISTS (SELECT 1 FROM datasources WHERE tenant_id <> 'tenant_default')
       OR EXISTS (SELECT 1 FROM policies WHERE tenant_id <> 'tenant_default')
       OR EXISTS (SELECT 1 FROM rules WHERE tenant_id <> 'tenant_default')
       OR EXISTS (SELECT 1 FROM mask_rules WHERE tenant_id <> 'tenant_default')
       OR EXISTS (SELECT 1 FROM audit_logs WHERE tenant_id <> 'tenant_default')
       OR EXISTS (SELECT 1 FROM approvals WHERE tenant_id <> 'tenant_default')
       OR EXISTS (SELECT 1 FROM notification_settings WHERE tenant_id <> 'tenant_default')
       OR EXISTS (SELECT 1 FROM notification_channels WHERE tenant_id <> 'tenant_default')
       OR EXISTS (SELECT 1 FROM redaction_key_versions WHERE tenant_id <> 'tenant_default')
       OR EXISTS (SELECT 1 FROM management_audit_outbox WHERE tenant_id <> 'tenant_default')
       OR EXISTS (SELECT 1 FROM chain_state WHERE tenant_id <> 'tenant_default')
       OR EXISTS (SELECT 1 FROM chain_verification WHERE tenant_id <> 'tenant_default')
       OR EXISTS (SELECT 1 FROM relation_policy_bindings WHERE tenant_id <> 'tenant_default')
       OR EXISTS (SELECT 1 FROM policy_column_permission_staging WHERE tenant_id <> 'tenant_default')
       OR EXISTS (SELECT 1 FROM policy_column_permissions WHERE tenant_id <> 'tenant_default')
       OR EXISTS (SELECT 1 FROM b5_transactions WHERE tenant_id <> 'tenant_default')
       OR EXISTS (SELECT 1 FROM b5_dml_grants WHERE tenant_id <> 'tenant_default')
       OR EXISTS (SELECT 1 FROM b5_result_receipts WHERE tenant_id <> 'tenant_default')
       OR EXISTS (SELECT 1 FROM b5_tx_events WHERE tenant_id <> 'tenant_default') THEN
        RAISE EXCEPTION 'cannot roll back business metadata tenant ownership while non-default tenant rows exist';
    END IF;
END
$guard$;

DROP INDEX idx_b5_tx_events_tenant; DROP INDEX idx_b5_result_receipts_tenant; DROP INDEX idx_b5_dml_grants_tenant; DROP INDEX idx_b5_transactions_tenant; DROP INDEX idx_b5_sessions_tenant;
DROP INDEX idx_policy_column_permissions_tenant; DROP INDEX idx_policy_column_permission_staging_tenant; DROP INDEX idx_relation_policy_bindings_tenant;
DROP INDEX idx_chain_verification_tenant; DROP INDEX idx_chain_state_tenant; DROP INDEX idx_management_audit_outbox_tenant; DROP INDEX idx_redaction_key_versions_tenant;
DROP INDEX idx_notification_channels_tenant; DROP INDEX idx_notification_settings_tenant; DROP INDEX idx_approvals_tenant; DROP INDEX idx_audit_logs_tenant;
DROP INDEX idx_mask_rules_tenant; DROP INDEX idx_rules_tenant; DROP INDEX idx_policies_tenant; DROP INDEX idx_datasources_tenant; DROP INDEX idx_agents_tenant;
ALTER TABLE chain_verification DROP CONSTRAINT chain_verification_pkey; ALTER TABLE chain_verification ADD PRIMARY KEY(chain_id);
ALTER TABLE chain_state DROP CONSTRAINT chain_state_pkey; ALTER TABLE chain_state ADD PRIMARY KEY(chain_id);
ALTER TABLE notification_channels DROP CONSTRAINT notification_channels_tenant_position_key; ALTER TABLE notification_channels ADD UNIQUE(position);
ALTER TABLE notification_channels DROP CONSTRAINT notification_channels_pkey; ALTER TABLE notification_channels ADD PRIMARY KEY(id);
ALTER TABLE notification_settings DROP CONSTRAINT notification_settings_pkey; ALTER TABLE notification_settings ADD PRIMARY KEY(id);
DROP INDEX ux_redaction_key_versions_active; CREATE UNIQUE INDEX ux_redaction_key_versions_active ON redaction_key_versions(state) WHERE state='active';
DROP INDEX ux_mask_rules_scope_column; CREATE UNIQUE INDEX ux_mask_rules_scope_column ON mask_rules(COALESCE(NULLIF(TRIM(datasource_id),''),''),COALESCE(NULLIF(TRIM(schema_name),''),''),COALESCE(NULLIF(TRIM(table_name),''),''),LOWER(TRIM(column_name)));
ALTER TABLE b5_tx_events DROP COLUMN tenant_id; ALTER TABLE b5_result_receipts DROP COLUMN tenant_id; ALTER TABLE b5_dml_grants DROP COLUMN tenant_id; ALTER TABLE b5_transactions DROP COLUMN tenant_id;
ALTER TABLE policy_column_permissions DROP COLUMN tenant_id; ALTER TABLE policy_column_permission_staging DROP COLUMN tenant_id; ALTER TABLE relation_policy_bindings DROP COLUMN tenant_id;
ALTER TABLE chain_verification DROP COLUMN tenant_id; ALTER TABLE chain_state DROP COLUMN tenant_id; ALTER TABLE management_audit_outbox DROP COLUMN tenant_id; ALTER TABLE redaction_key_versions DROP COLUMN tenant_id;
ALTER TABLE notification_channels DROP COLUMN tenant_id; ALTER TABLE notification_settings DROP COLUMN tenant_id; ALTER TABLE approvals DROP COLUMN tenant_id; ALTER TABLE audit_logs DROP COLUMN tenant_id;
ALTER TABLE mask_rules DROP COLUMN tenant_id; ALTER TABLE rules DROP COLUMN tenant_id; ALTER TABLE policies DROP COLUMN tenant_id; ALTER TABLE datasources DROP COLUMN tenant_id; ALTER TABLE agents DROP COLUMN tenant_id;
