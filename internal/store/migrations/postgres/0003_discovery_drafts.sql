ALTER TABLE mask_rules
  ADD COLUMN enabled BOOLEAN NOT NULL DEFAULT TRUE;

CREATE UNIQUE INDEX ux_mask_rules_scope_column
ON mask_rules (
  COALESCE(NULLIF(BTRIM(datasource_id),''),''),
  LOWER(BTRIM(column_name))
);

ALTER TABLE audit_logs ADD COLUMN action TEXT;
ALTER TABLE audit_logs ADD COLUMN actor_type TEXT;
ALTER TABLE audit_logs ADD COLUMN actor_id TEXT;
ALTER TABLE audit_logs ADD COLUMN details_json TEXT;
