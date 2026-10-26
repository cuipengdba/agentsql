DROP INDEX idx_sqlbot_bindings_parent;
ALTER TABLE sqlbot_bindings DROP COLUMN parent_binding_revision;
ALTER TABLE sqlbot_bindings DROP COLUMN parent_binding_id;
