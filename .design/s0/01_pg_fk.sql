DROP SCHEMA IF EXISTS s0_fk CASCADE;
CREATE SCHEMA s0_fk;
CREATE TABLE s0_fk.parent(id integer PRIMARY KEY, payload text);
CREATE TABLE s0_fk.child(
  id integer PRIMARY KEY,
  parent_id integer REFERENCES s0_fk.parent(id)
    ON UPDATE CASCADE ON DELETE RESTRICT
);

-- Evidence: the one FK constraint has internal triggers on both relations.
SELECT t.oid AS trigger_oid,
       t.tgname,
       t.tgrelid::regclass AS trigger_relation,
       t.tgisinternal,
       c.oid AS constraint_oid,
       c.conrelid::regclass AS conrelid,
       c.confrelid::regclass AS confrelid
FROM pg_catalog.pg_trigger AS t
JOIN pg_catalog.pg_constraint AS c ON c.oid = t.tgconstraint
WHERE c.conname = 'child_parent_id_fkey'
ORDER BY t.tgrelid, t.tgname;

-- Deliberately wrong detector: targeting parent and scanning conrelid only.
SELECT c.oid, c.conname
FROM pg_catalog.pg_constraint AS c
WHERE c.contype = 'f'
  AND c.conrelid = 's0_fk.parent'::regclass;

-- Required bidirectional detector. The direction column makes audit output clear.
WITH target(relid) AS (VALUES ('s0_fk.parent'::regclass))
SELECT c.oid,
       c.conname,
       c.conrelid::regclass AS referencing_relation,
       c.confrelid::regclass AS referenced_relation,
       CASE WHEN c.conrelid = t.relid AND c.confrelid = t.relid THEN 'self'
            WHEN c.conrelid = t.relid THEN 'outgoing'
            ELSE 'incoming' END AS direction
FROM pg_catalog.pg_constraint AS c
JOIN target AS t ON c.conrelid = t.relid OR c.confrelid = t.relid
WHERE c.contype = 'f'
ORDER BY c.oid;

INSERT INTO s0_fk.parent VALUES (1, 'p');
INSERT INTO s0_fk.child VALUES (1, 1);
UPDATE s0_fk.parent SET id = 2 WHERE id = 1;
SELECT * FROM s0_fk.child; -- parent_id is now 2: parent-side UPDATE trigger ran.
DELETE FROM s0_fk.parent WHERE id = 2; -- ERROR: parent-side DELETE trigger ran.

