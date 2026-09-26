DROP DATABASE IF EXISTS s0_fk;
CREATE DATABASE s0_fk;
CREATE TABLE s0_fk.parent(id integer PRIMARY KEY, payload varchar(30));
CREATE TABLE s0_fk.child(
  id integer PRIMARY KEY,
  parent_id integer,
  CONSTRAINT child_parent_fk FOREIGN KEY(parent_id) REFERENCES s0_fk.parent(id)
    ON UPDATE CASCADE ON DELETE RESTRICT
);

-- Bidirectional detector for a root set. MySQL exposes no PG-style implicit
-- trigger rows, so KCU is the authoritative FK edge catalog.
WITH roots(schema_name, table_name) AS (
  SELECT 's0_fk', 'parent'
)
SELECT k.CONSTRAINT_SCHEMA,
       k.CONSTRAINT_NAME,
       k.TABLE_SCHEMA AS referencing_schema,
       k.TABLE_NAME AS referencing_table,
       k.COLUMN_NAME AS referencing_column,
       k.REFERENCED_TABLE_SCHEMA AS referenced_schema,
       k.REFERENCED_TABLE_NAME AS referenced_table,
       k.REFERENCED_COLUMN_NAME AS referenced_column,
       CASE WHEN k.TABLE_SCHEMA = r.schema_name AND k.TABLE_NAME = r.table_name
            THEN 'outgoing' ELSE 'incoming' END AS direction
FROM information_schema.KEY_COLUMN_USAGE AS k
JOIN roots AS r
  ON (k.TABLE_SCHEMA = r.schema_name AND k.TABLE_NAME = r.table_name)
  OR (k.REFERENCED_TABLE_SCHEMA = r.schema_name
      AND k.REFERENCED_TABLE_NAME = r.table_name)
WHERE k.REFERENCED_TABLE_NAME IS NOT NULL
ORDER BY k.CONSTRAINT_SCHEMA, k.CONSTRAINT_NAME, k.ORDINAL_POSITION;

