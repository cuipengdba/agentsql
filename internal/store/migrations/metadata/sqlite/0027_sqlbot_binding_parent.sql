ALTER TABLE sqlbot_bindings ADD COLUMN parent_binding_id TEXT NOT NULL DEFAULT '';
ALTER TABLE sqlbot_bindings ADD COLUMN parent_binding_revision BIGINT NOT NULL DEFAULT 0;
CREATE INDEX idx_sqlbot_bindings_parent ON sqlbot_bindings(tenant_id,parent_binding_id);
