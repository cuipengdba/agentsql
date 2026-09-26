DROP SCHEMA IF EXISTS s0_mv CASCADE;
CREATE SCHEMA s0_mv;
CREATE TABLE s0_mv.base_a(id integer PRIMARY KEY, secret text);
CREATE TABLE s0_mv.base_b(id integer PRIMARY KEY, note text);
INSERT INTO s0_mv.base_a VALUES (1,'alpha');
INSERT INTO s0_mv.base_b VALUES (1,'bravo');
CREATE MATERIALIZED VIEW s0_mv.mv1 AS
  SELECT a.id, a.secret, b.note
  FROM s0_mv.base_a a JOIN s0_mv.base_b b USING(id);
CREATE MATERIALIZED VIEW s0_mv.mv2 AS
  SELECT id, upper(secret) AS secret_upper FROM s0_mv.mv1;

-- Reading mv2 only locks its stored heap; it does not rewrite through mv1/base.
BEGIN READ ONLY;
SELECT pg_backend_pid(); -- use as :pid in an observer session
PREPARE s0_mv_read AS SELECT secret_upper FROM s0_mv.mv2;
-- observer:
SELECT c.oid,c.relname,c.relkind,l.mode,l.granted
FROM pg_catalog.pg_locks l
JOIN pg_catalog.pg_class c ON c.oid=l.relation
JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
WHERE l.pid=:pid AND n.nspname='s0_mv' AND l.locktype='relation'
ORDER BY c.oid;

-- Definitions are real analyzed Query trees. pg_get_viewdef is the supported
-- deparser; ev_action is retained as evidence/input to an in-server tree walker.
SELECT c.oid,c.relname,r.oid AS rule_oid,
       pg_get_viewdef(c.oid,true) AS definition_sql,
       r.ev_action::text AS analyzed_node_tree
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_rewrite r ON r.ev_class=c.oid AND r.rulename='_RETURN'
JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
WHERE n.nspname='s0_mv' AND c.relkind='m'
ORDER BY c.oid;

-- Recursive definition-relation closure. refobjsubid additionally provides
-- source-column dependencies, but pg_depend alone does not associate a source
-- dependency with one output TargetEntry.
WITH RECURSIVE definition_closure(relid) AS (
  VALUES ('s0_mv.mv2'::regclass)
  UNION
  SELECT d.refobjid
  FROM definition_closure c
  JOIN pg_catalog.pg_rewrite r ON r.ev_class=c.relid AND r.rulename='_RETURN'
  JOIN pg_catalog.pg_depend d
    ON d.classid='pg_rewrite'::regclass AND d.objid=r.oid
   AND d.refclassid='pg_class'::regclass AND d.deptype='n'
  JOIN pg_catalog.pg_class rc ON rc.oid=d.refobjid
  WHERE d.refobjid<>c.relid AND rc.relkind IN ('r','p','v','m','f')
)
SELECT c.relid,c.relid::regclass,pc.relkind
FROM definition_closure c JOIN pg_catalog.pg_class pc ON pc.oid=c.relid
ORDER BY c.relid;

-- Source columns per definition layer (not yet per output TargetEntry).
SELECT owner.relname AS definition_owner, src.relname AS source_relation,
       d.refobjsubid AS source_attnum, a.attname AS source_column
FROM pg_catalog.pg_class owner
JOIN pg_catalog.pg_rewrite r ON r.ev_class=owner.oid AND r.rulename='_RETURN'
JOIN pg_catalog.pg_depend d
  ON d.classid='pg_rewrite'::regclass AND d.objid=r.oid
 AND d.refclassid='pg_class'::regclass AND d.refobjsubid>0
JOIN pg_catalog.pg_class src ON src.oid=d.refobjid
JOIN pg_catalog.pg_attribute a ON a.attrelid=d.refobjid AND a.attnum=d.refobjsubid
JOIN pg_catalog.pg_namespace n ON n.oid=owner.relnamespace
WHERE n.nspname='s0_mv'
ORDER BY owner.oid,src.oid,d.refobjsubid;

