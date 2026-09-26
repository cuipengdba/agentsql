DROP SCHEMA IF EXISTS s0_lock CASCADE;
CREATE SCHEMA s0_lock;
CREATE TABLE s0_lock.base_a(id integer PRIMARY KEY, secret text);
CREATE TABLE s0_lock.base_b(id integer PRIMARY KEY, note text);
CREATE VIEW s0_lock.v_ab AS
  SELECT a.id, a.secret, b.note
  FROM s0_lock.base_a AS a JOIN s0_lock.base_b AS b USING (id);

-- Session B: perform genuine server parse/analyze/rewrite without execution.
BEGIN READ ONLY;
SELECT pg_backend_pid(); -- substitute as :binder_pid below
PREPARE s0_bound(integer) AS
  SELECT secret FROM s0_lock.v_ab WHERE id = $1;

-- Session O: enumerate actual held user-relation closure while B remains open.
SELECT l.relation, c.oid, n.nspname, c.relname, c.relkind,
       l.mode, l.granted, l.fastpath
FROM pg_catalog.pg_locks AS l
JOIN pg_catalog.pg_class AS c ON c.oid = l.relation
JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
WHERE l.pid = :binder_pid AND l.locktype = 'relation'
  AND n.nspname = 's0_lock'
ORDER BY c.oid, l.mode;

-- Execution transaction: obtain explicit locks in numeric OID order. Generate
-- the identifier list only from server-returned, already bound OID/name pairs.
SELECT c.oid, format('%I.%I', n.nspname, c.relname) AS qname
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
WHERE n.nspname='s0_lock' AND c.relname IN ('base_a','base_b','v_ab')
ORDER BY c.oid;

BEGIN READ ONLY;
SET LOCAL lock_timeout = '1s';
LOCK TABLE s0_lock.base_a, s0_lock.base_b, s0_lock.v_ab
  IN ACCESS SHARE MODE; -- runner substitutes the actual OID-sorted order

-- Immediate invariant: every candidate OID has a granted relation lock.
WITH candidate(oid) AS (VALUES (:oid1::oid),(:oid2::oid),(:oid3::oid))
SELECT c.oid, c.oid::regclass, l.mode, l.granted
FROM candidate c
LEFT JOIN pg_catalog.pg_locks l
  ON l.pid=pg_backend_pid() AND l.locktype='relation' AND l.relation=c.oid
ORDER BY c.oid;

-- Bind/rewrite again in the locked execution transaction, then enumerate all
-- held user relations. Any row outside the candidate closure is fail-closed.
PREPARE s0_exec(integer) AS
  SELECT secret FROM s0_lock.v_ab WHERE id = $1;
WITH candidate(oid) AS (VALUES (:oid1::oid),(:oid2::oid),(:oid3::oid))
SELECT l.relation::regclass AS unexpected_relation
FROM pg_catalog.pg_locks l
JOIN pg_catalog.pg_class c ON c.oid=l.relation
JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
LEFT JOIN candidate x ON x.oid=l.relation
WHERE l.pid=pg_backend_pid() AND l.locktype='relation' AND l.granted
  AND n.nspname NOT IN ('pg_catalog','information_schema','pg_toast')
  AND x.oid IS NULL;

