-- The timed runner uses one warmed persistent physical connection and 500
-- uncontended samples per engine. Network path is host -> Docker published port.

-- PostgreSQL sample (four protocol operations: BEGIN, LOCK, catalog, ROLLBACK):
BEGIN READ ONLY;
LOCK TABLE s0_latency.t1,s0_latency.t2,s0_latency.v IN ACCESS SHARE MODE;
WITH target(relid) AS (
  SELECT unnest(ARRAY['s0_latency.t1'::regclass,
                      's0_latency.t2'::regclass,
                      's0_latency.v'::regclass])
)
SELECT count(*) FROM (
  SELECT t.oid FROM pg_trigger t JOIN target x ON x.relid=t.tgrelid
  UNION ALL SELECT d.oid FROM pg_attrdef d JOIN target x ON x.relid=d.adrelid
  UNION ALL SELECT p.oid FROM pg_policy p JOIN target x ON x.relid=p.polrelid
  UNION ALL SELECT r.oid FROM pg_rewrite r JOIN target x ON x.relid=r.ev_class
  UNION ALL SELECT i.indexrelid FROM pg_index i JOIN target x ON x.relid=i.indrelid
  UNION ALL SELECT c.oid FROM pg_constraint c JOIN target x
    ON c.conrelid=x.relid OR c.confrelid=x.relid
) findings;
ROLLBACK;

-- MySQL sample (six operations: backup lock, drop/create/fill temp roots,
-- catalog query, unlock):
LOCK INSTANCE FOR BACKUP;
DROP TEMPORARY TABLE IF EXISTS agentsql_catalog_roots;
CREATE TEMPORARY TABLE agentsql_catalog_roots(
  schema_name VARBINARY(256),table_name VARBINARY(256));
INSERT INTO agentsql_catalog_roots VALUES
  ('s0_latency','t1'),('s0_latency','t2'),('s0_latency','v');
-- A CTE over the temp table referenced in multiple UNION arms fails on MySQL
-- 8.4 with 1137 Can't reopen table. Scan the temp roots exactly once instead.
SELECT coalesce(sum(
  (SELECT count(*) FROM information_schema.COLUMNS c
    WHERE c.TABLE_SCHEMA=CONVERT(r.schema_name USING utf8mb4)
      AND c.TABLE_NAME=CONVERT(r.table_name USING utf8mb4)) +
  (SELECT count(*) FROM information_schema.TRIGGERS t
    WHERE t.EVENT_OBJECT_SCHEMA=CONVERT(r.schema_name USING utf8mb4)
      AND t.EVENT_OBJECT_TABLE=CONVERT(r.table_name USING utf8mb4)) +
  (SELECT count(*) FROM information_schema.VIEWS v
    WHERE v.TABLE_SCHEMA=CONVERT(r.schema_name USING utf8mb4)
      AND v.TABLE_NAME=CONVERT(r.table_name USING utf8mb4))
),0) FROM agentsql_catalog_roots r;
UNLOCK INSTANCE;
