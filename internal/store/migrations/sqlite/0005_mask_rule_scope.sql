ALTER TABLE mask_rules ADD COLUMN schema_name TEXT;

UPDATE mask_rules SET schema_name = '', table_name = '';

DROP INDEX IF EXISTS ux_mask_rules_scope_column;

CREATE UNIQUE INDEX ux_mask_rules_scope_column
ON mask_rules (
  COALESCE(NULLIF(TRIM(datasource_id),''),''),
  schema_name,
  table_name,
  LOWER(TRIM(column_name))
);
