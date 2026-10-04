-- SQLite cannot add a REFERENCES column with a non-NULL backfill default.
-- The NOT NULL owner and repository filters are authoritative; new writes are
-- additionally checked by the application against tenants.
INSERT OR IGNORE INTO tenants(id,name,status) VALUES('tenant_default','Default','active');
ALTER TABLE agents ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'tenant_default';
ALTER TABLE datasources ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'tenant_default';
ALTER TABLE policies ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'tenant_default';
ALTER TABLE rules ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'tenant_default';
ALTER TABLE mask_rules ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'tenant_default';
ALTER TABLE audit_logs ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'tenant_default';
ALTER TABLE approvals ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'tenant_default';
ALTER TABLE notification_settings RENAME TO notification_settings_legacy;
CREATE TABLE notification_settings (
  tenant_id TEXT NOT NULL DEFAULT 'tenant_default', id INTEGER NOT NULL CHECK(id=1),
  enabled INTEGER NOT NULL DEFAULT 0, queue_size INTEGER NOT NULL DEFAULT 0,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(tenant_id,id)
);
INSERT INTO notification_settings(tenant_id,id,enabled,queue_size,created_at,updated_at)
SELECT 'tenant_default',id,enabled,queue_size,created_at,updated_at FROM notification_settings_legacy;
DROP TABLE notification_settings_legacy;
ALTER TABLE notification_channels RENAME TO notification_channels_legacy;
CREATE TABLE notification_channels (
  tenant_id TEXT NOT NULL DEFAULT 'tenant_default', id TEXT NOT NULL, position INTEGER NOT NULL CHECK(position>=0),
  enabled INTEGER NOT NULL DEFAULT 0, kind TEXT NOT NULL, decisions TEXT NOT NULL, include_sql INTEGER NOT NULL DEFAULT 0,
  allow_private_endpoints INTEGER NOT NULL DEFAULT 0, webhook_present INTEGER NOT NULL DEFAULT 0, webhook_template TEXT,
  webhook_url_enc TEXT, webhook_bearer_token_enc TEXT, webhook_headers_enc TEXT, webhook_secret_enc TEXT,
  syslog_present INTEGER NOT NULL DEFAULT 0, syslog_host TEXT, syslog_port INTEGER, syslog_transport TEXT, syslog_facility INTEGER,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(tenant_id,id), UNIQUE(tenant_id,position)
);
INSERT INTO notification_channels(tenant_id,id,position,enabled,kind,decisions,include_sql,allow_private_endpoints,webhook_present,webhook_template,webhook_url_enc,webhook_bearer_token_enc,webhook_headers_enc,webhook_secret_enc,syslog_present,syslog_host,syslog_port,syslog_transport,syslog_facility,created_at,updated_at)
SELECT 'tenant_default',id,position,enabled,kind,decisions,include_sql,allow_private_endpoints,webhook_present,webhook_template,webhook_url_enc,webhook_bearer_token_enc,webhook_headers_enc,webhook_secret_enc,syslog_present,syslog_host,syslog_port,syslog_transport,syslog_facility,created_at,updated_at FROM notification_channels_legacy;
DROP TABLE notification_channels_legacy;
ALTER TABLE redaction_key_versions ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'tenant_default';
ALTER TABLE management_audit_outbox ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'tenant_default';
ALTER TABLE chain_state RENAME TO chain_state_legacy;
CREATE TABLE chain_state (
  tenant_id TEXT NOT NULL DEFAULT 'tenant_default',
  chain_id TEXT NOT NULL CHECK (chain_id IN ('management','traffic')),
  chain_instance_id TEXT,
  status TEXT NOT NULL CHECK (status IN ('DISABLED','BUILDING','ACTIVE','FAILED')),
  mode TEXT CHECK (mode IS NULL OR mode IN ('keyless','hmac')),
  head_seq INTEGER NOT NULL DEFAULT 0,
  head_id INTEGER,
  head_hash TEXT,
  genesis_at TIMESTAMP,
  protected_since_id INTEGER,
  build_owner TEXT,
  build_lease_until TIMESTAMP,
  build_epoch INTEGER NOT NULL DEFAULT 0,
  last_built_id INTEGER,
  last_built_seq INTEGER,
  last_built_hash TEXT,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(tenant_id,chain_id)
);
INSERT INTO chain_state(tenant_id,chain_id,chain_instance_id,status,mode,head_seq,head_id,head_hash,genesis_at,protected_since_id,build_owner,build_lease_until,build_epoch,last_built_id,last_built_seq,last_built_hash,updated_at)
SELECT 'tenant_default',chain_id,chain_instance_id,status,mode,head_seq,head_id,head_hash,genesis_at,protected_since_id,build_owner,build_lease_until,build_epoch,last_built_id,last_built_seq,last_built_hash,updated_at FROM chain_state_legacy;
DROP TABLE chain_state_legacy;
ALTER TABLE chain_verification RENAME TO chain_verification_legacy;
CREATE TABLE chain_verification (
  tenant_id TEXT NOT NULL DEFAULT 'tenant_default',
  chain_id TEXT NOT NULL,
  observed_instance_id TEXT,
  observed_head_hash TEXT,
  result TEXT,
  last_verified_head_seq INTEGER,
  last_verified_at TIMESTAMP,
  break_seq INTEGER,
  break_id INTEGER,
  break_reason TEXT,
  PRIMARY KEY(tenant_id,chain_id)
);
INSERT INTO chain_verification(tenant_id,chain_id,observed_instance_id,observed_head_hash,result,last_verified_head_seq,last_verified_at,break_seq,break_id,break_reason)
SELECT 'tenant_default',chain_id,observed_instance_id,observed_head_hash,result,last_verified_head_seq,last_verified_at,break_seq,break_id,break_reason FROM chain_verification_legacy;
DROP TABLE chain_verification_legacy;
ALTER TABLE relation_policy_bindings ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'tenant_default';
ALTER TABLE policy_column_permission_staging ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'tenant_default';
ALTER TABLE policy_column_permissions ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'tenant_default';
UPDATE b5_sessions SET tenant_id='tenant_default';
ALTER TABLE b5_transactions ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'tenant_default';
ALTER TABLE b5_dml_grants ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'tenant_default';
ALTER TABLE b5_result_receipts ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'tenant_default';
ALTER TABLE b5_tx_events ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'tenant_default';

DROP INDEX ux_mask_rules_scope_column;
CREATE UNIQUE INDEX ux_mask_rules_scope_column ON mask_rules(tenant_id,COALESCE(NULLIF(TRIM(datasource_id),''),''),COALESCE(NULLIF(TRIM(schema_name),''),''),COALESCE(NULLIF(TRIM(table_name),''),''),LOWER(TRIM(column_name)));
DROP INDEX ux_redaction_key_versions_active;
CREATE UNIQUE INDEX ux_redaction_key_versions_active ON redaction_key_versions(tenant_id) WHERE state='active';

CREATE INDEX idx_agents_tenant ON agents(tenant_id); CREATE INDEX idx_datasources_tenant ON datasources(tenant_id); CREATE INDEX idx_policies_tenant ON policies(tenant_id); CREATE INDEX idx_rules_tenant ON rules(tenant_id); CREATE INDEX idx_mask_rules_tenant ON mask_rules(tenant_id); CREATE INDEX idx_audit_logs_tenant ON audit_logs(tenant_id); CREATE INDEX idx_approvals_tenant ON approvals(tenant_id);
CREATE INDEX idx_notification_settings_tenant ON notification_settings(tenant_id); CREATE INDEX idx_notification_channels_tenant ON notification_channels(tenant_id); CREATE INDEX idx_redaction_key_versions_tenant ON redaction_key_versions(tenant_id); CREATE INDEX idx_management_audit_outbox_tenant ON management_audit_outbox(tenant_id); CREATE INDEX idx_chain_state_tenant ON chain_state(tenant_id); CREATE INDEX idx_chain_verification_tenant ON chain_verification(tenant_id);
CREATE INDEX idx_relation_policy_bindings_tenant ON relation_policy_bindings(tenant_id); CREATE INDEX idx_policy_column_permission_staging_tenant ON policy_column_permission_staging(tenant_id); CREATE INDEX idx_policy_column_permissions_tenant ON policy_column_permissions(tenant_id); CREATE INDEX idx_b5_sessions_tenant ON b5_sessions(tenant_id); CREATE INDEX idx_b5_transactions_tenant ON b5_transactions(tenant_id); CREATE INDEX idx_b5_dml_grants_tenant ON b5_dml_grants(tenant_id); CREATE INDEX idx_b5_result_receipts_tenant ON b5_result_receipts(tenant_id); CREATE INDEX idx_b5_tx_events_tenant ON b5_tx_events(tenant_id);
