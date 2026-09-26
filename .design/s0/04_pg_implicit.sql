DROP SCHEMA IF EXISTS s0_implicit CASCADE;
CREATE SCHEMA s0_implicit;
CREATE TABLE s0_implicit.t(
  id integer,
  payload text DEFAULT md5(random()::text),
  doubled integer GENERATED ALWAYS AS (id * 2) STORED,
  slot int4range,
  CONSTRAINT t_slot_excl EXCLUDE USING gist (slot WITH &&)
);
CREATE FUNCTION s0_implicit.t_biu() RETURNS trigger LANGUAGE plpgsql AS
$$ BEGIN NEW.payload := coalesce(NEW.payload, 'triggered'); RETURN NEW; END $$;
CREATE TRIGGER t_biu BEFORE INSERT OR UPDATE ON s0_implicit.t
FOR EACH ROW EXECUTE FUNCTION s0_implicit.t_biu();
ALTER TABLE s0_implicit.t ENABLE ROW LEVEL SECURITY;
CREATE POLICY t_policy ON s0_implicit.t USING (id > 0);
CREATE INDEX t_expr_idx ON s0_implicit.t ((lower(payload)));
CREATE INDEX t_partial_idx ON s0_implicit.t (id) WHERE id > 0;
CREATE VIEW s0_implicit.v AS SELECT id,payload FROM s0_implicit.t;
CREATE FUNCTION s0_implicit.v_ins() RETURNS trigger LANGUAGE plpgsql AS
$$ BEGIN INSERT INTO s0_implicit.t(id,payload) VALUES(NEW.id,NEW.payload); RETURN NEW; END $$;
CREATE TRIGGER v_ins INSTEAD OF INSERT ON s0_implicit.v
FOR EACH ROW EXECUTE FUNCTION s0_implicit.v_ins();

-- One row means fail closed. Feed the complete bound relation closure as $1.
WITH target(relid) AS (
  SELECT unnest(ARRAY['s0_implicit.t'::regclass,'s0_implicit.v'::regclass])
), findings AS (
  SELECT t.tgrelid relid,'trigger'::text kind,t.oid object_oid,
         pg_get_triggerdef(t.oid,true) detail
  FROM pg_catalog.pg_trigger t JOIN target x ON x.relid=t.tgrelid
  WHERE NOT t.tgisinternal
  UNION ALL
  SELECT d.adrelid,CASE WHEN a.attgenerated<>'' THEN 'generated' ELSE 'column_default' END,
         d.oid,pg_get_expr(d.adbin,d.adrelid)
  FROM pg_catalog.pg_attrdef d JOIN target x ON x.relid=d.adrelid
  JOIN pg_catalog.pg_attribute a ON a.attrelid=d.adrelid AND a.attnum=d.adnum
  UNION ALL
  SELECT p.polrelid,'rls_policy',p.oid,pg_get_expr(p.polqual,p.polrelid)
  FROM pg_catalog.pg_policy p JOIN target x ON x.relid=p.polrelid
  UNION ALL
  SELECT r.ev_class,'view_rule',r.oid,pg_get_ruledef(r.oid,true)
  FROM pg_catalog.pg_rewrite r JOIN target x ON x.relid=r.ev_class
  JOIN pg_catalog.pg_class c ON c.oid=r.ev_class
  WHERE c.relkind IN ('v','m') AND r.rulename='_RETURN'
  UNION ALL
  SELECT t.tgrelid,'instead_of_trigger',t.oid,pg_get_triggerdef(t.oid,true)
  FROM pg_catalog.pg_trigger t JOIN target x ON x.relid=t.tgrelid
  WHERE NOT t.tgisinternal AND (t.tgtype::int & 64)<>0
  UNION ALL
  SELECT i.indrelid,CASE WHEN i.indexprs IS NOT NULL THEN 'expression_index'
                        ELSE 'partial_index' END,
         i.indexrelid,pg_get_indexdef(i.indexrelid)
  FROM pg_catalog.pg_index i JOIN target x ON x.relid=i.indrelid
  WHERE i.indexprs IS NOT NULL OR i.indpred IS NOT NULL
  UNION ALL
  SELECT c.conrelid,'exclusion_constraint',c.oid,pg_get_constraintdef(c.oid,true)
  FROM pg_catalog.pg_constraint c JOIN target x ON x.relid=c.conrelid
  WHERE c.contype='x'
)
SELECT relid::regclass,kind,object_oid,detail FROM findings ORDER BY kind,object_oid;

