DO $guard$
BEGIN
    IF EXISTS (SELECT 1 FROM audit_logs WHERE tenant_id <> 'tenant_default')
       OR EXISTS (SELECT 1 FROM chain_state WHERE tenant_id <> 'tenant_default')
       OR EXISTS (SELECT 1 FROM chain_verification WHERE tenant_id <> 'tenant_default') THEN
        RAISE EXCEPTION 'cannot roll back audit tenant ownership while non-default tenant rows exist';
    END IF;
END
$guard$;

DROP INDEX idx_chain_verification_tenant;
DROP INDEX idx_chain_state_tenant;
DROP INDEX idx_audit_logs_tenant;
ALTER TABLE chain_verification DROP CONSTRAINT chain_verification_pkey;
ALTER TABLE chain_verification ADD PRIMARY KEY(chain_id);
ALTER TABLE chain_state DROP CONSTRAINT chain_state_pkey;
ALTER TABLE chain_state ADD PRIMARY KEY(chain_id);
ALTER TABLE chain_verification DROP COLUMN tenant_id;
ALTER TABLE chain_state DROP COLUMN tenant_id;
ALTER TABLE audit_logs DROP COLUMN tenant_id;
