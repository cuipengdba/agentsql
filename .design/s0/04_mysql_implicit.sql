DROP DATABASE IF EXISTS s0_implicit;
CREATE DATABASE s0_implicit;
CREATE TABLE s0_implicit.t(
  id integer,
  payload varchar(64) DEFAULT ('d|x'),
  payload_len integer GENERATED ALWAYS AS (char_length(payload)) STORED,
  updated_at timestamp DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
);
CREATE TRIGGER s0_implicit.t_bi BEFORE INSERT ON s0_implicit.t FOR EACH ROW
  SET NEW.payload=coalesce(NEW.payload,'triggered');
CREATE INDEX t_expr_idx ON s0_implicit.t ((lower(payload)));
CREATE VIEW s0_implicit.v AS SELECT id,payload FROM s0_implicit.t;
CREATE EVENT s0_implicit.ev_touch ON SCHEDULE EVERY 1 DAY DO
  UPDATE s0_implicit.t SET payload=payload WHERE id=-1;

-- roots would normally be a connection-local AgentSQL temporary table.
WITH roots(schema_name,table_name) AS (
  SELECT 's0_implicit','t' UNION ALL SELECT 's0_implicit','v'
), findings AS (
  SELECT tr.EVENT_OBJECT_SCHEMA schema_name,tr.EVENT_OBJECT_TABLE table_name,
         'trigger' kind,tr.TRIGGER_NAME object_name,tr.ACTION_STATEMENT detail
  FROM information_schema.TRIGGERS tr JOIN roots r
    ON r.schema_name=tr.EVENT_OBJECT_SCHEMA AND r.table_name=tr.EVENT_OBJECT_TABLE
  UNION ALL
  SELECT c.TABLE_SCHEMA,c.TABLE_NAME,'column_default',c.COLUMN_NAME,
         coalesce(c.COLUMN_DEFAULT,'<NULL>')
  FROM information_schema.COLUMNS c JOIN roots r
    ON r.schema_name=c.TABLE_SCHEMA AND r.table_name=c.TABLE_NAME
  JOIN information_schema.TABLES bt
    ON bt.TABLE_SCHEMA=c.TABLE_SCHEMA AND bt.TABLE_NAME=c.TABLE_NAME
   AND bt.TABLE_TYPE='BASE TABLE'
  WHERE c.COLUMN_DEFAULT IS NOT NULL
    AND coalesce(c.GENERATION_EXPRESSION,'')=''
  UNION ALL
  SELECT c.TABLE_SCHEMA,c.TABLE_NAME,'generated_column',c.COLUMN_NAME,
         c.GENERATION_EXPRESSION
  FROM information_schema.COLUMNS c JOIN roots r
    ON r.schema_name=c.TABLE_SCHEMA AND r.table_name=c.TABLE_NAME
  JOIN information_schema.TABLES bt
    ON bt.TABLE_SCHEMA=c.TABLE_SCHEMA AND bt.TABLE_NAME=c.TABLE_NAME
   AND bt.TABLE_TYPE='BASE TABLE'
  WHERE coalesce(c.GENERATION_EXPRESSION,'')<>''
  UNION ALL
  SELECT c.TABLE_SCHEMA,c.TABLE_NAME,'on_update_expression',c.COLUMN_NAME,c.EXTRA
  FROM information_schema.COLUMNS c JOIN roots r
    ON r.schema_name=c.TABLE_SCHEMA AND r.table_name=c.TABLE_NAME
  JOIN information_schema.TABLES bt
    ON bt.TABLE_SCHEMA=c.TABLE_SCHEMA AND bt.TABLE_NAME=c.TABLE_NAME
   AND bt.TABLE_TYPE='BASE TABLE'
  WHERE lower(c.EXTRA) LIKE '%on update%'
  UNION ALL
  SELECT v.TABLE_SCHEMA,v.TABLE_NAME,'view',v.TABLE_NAME,v.VIEW_DEFINITION
  FROM information_schema.VIEWS v JOIN roots r
    ON r.schema_name=v.TABLE_SCHEMA AND r.table_name=v.TABLE_NAME
  UNION ALL
  SELECT s.TABLE_SCHEMA,s.TABLE_NAME,'expression_index',s.INDEX_NAME,s.EXPRESSION
  FROM information_schema.STATISTICS s JOIN roots r
    ON r.schema_name=s.TABLE_SCHEMA AND r.table_name=s.TABLE_NAME
  WHERE s.EXPRESSION IS NOT NULL
  UNION ALL
  -- EVENT metadata carries no reliable referenced-table edge; reject every
  -- visible event in a root schema.
  SELECT e.EVENT_SCHEMA,'*','event',e.EVENT_NAME,e.EVENT_DEFINITION
  FROM information_schema.EVENTS e
  WHERE e.EVENT_SCHEMA IN (SELECT DISTINCT schema_name FROM roots)
)
SELECT * FROM findings ORDER BY kind,object_name;
