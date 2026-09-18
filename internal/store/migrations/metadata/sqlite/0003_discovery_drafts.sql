ALTER TABLE mask_rules
  ADD COLUMN enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1));

CREATE UNIQUE INDEX ux_mask_rules_scope_column
ON mask_rules (
  COALESCE(NULLIF(TRIM(datasource_id),''),''),
  LOWER(TRIM(column_name))
);
