ALTER TABLE policies ADD COLUMN relation_binding_id TEXT;
ALTER TABLE policies ADD COLUMN revision BIGINT NOT NULL DEFAULT 1 CHECK (revision>0);
ALTER TABLE policies ADD COLUMN legacy_unrepresentable BOOLEAN NOT NULL DEFAULT FALSE;
CREATE TABLE relation_policy_bindings (
 id TEXT PRIMARY KEY,policy_id TEXT NOT NULL UNIQUE REFERENCES policies(id) ON DELETE CASCADE,datasource_id TEXT NOT NULL REFERENCES datasources(id),
 schema_name TEXT NOT NULL DEFAULT '',relation_name TEXT NOT NULL,stable_object_id TEXT,catalog_fingerprint TEXT,
 status TEXT NOT NULL DEFAULT 'staging' CHECK(status IN('staging','healthy','needs_rebind','unsupported','revoked')),
 revision BIGINT NOT NULL DEFAULT 1 CHECK(revision>0),created_at TIMESTAMPTZ NOT NULL DEFAULT now(),updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_relation_policy_bindings_datasource ON relation_policy_bindings(datasource_id,relation_name);
CREATE TABLE policy_column_permission_staging (
 policy_id TEXT NOT NULL REFERENCES policies(id) ON DELETE CASCADE,token_ordinal INTEGER NOT NULL CHECK(token_ordinal>0),legacy_token TEXT NOT NULL,
 requested_usage TEXT NOT NULL CHECK(requested_usage IN('output','reference')),source_csv_sha256 TEXT NOT NULL CHECK(length(source_csv_sha256)=64),
 bind_status TEXT NOT NULL DEFAULT 'pending' CHECK(bind_status IN('pending','bound','error')),error_code TEXT,PRIMARY KEY(policy_id,token_ordinal,requested_usage)
);
CREATE TABLE policy_column_permissions (
 policy_id TEXT NOT NULL REFERENCES policies(id) ON DELETE CASCADE,relation_enrollment_id TEXT NOT NULL,column_ordinal INTEGER NOT NULL CHECK(column_ordinal>0),
 column_name TEXT NOT NULL CHECK(column_name<>'' AND column_name<>'*'),column_type_digest TEXT NOT NULL CHECK(column_type_digest<>''),
 usage TEXT NOT NULL CHECK(usage IN('output','reference')),parent_revision BIGINT NOT NULL CHECK(parent_revision>0),
 PRIMARY KEY(policy_id,relation_enrollment_id,column_ordinal,usage)
);
CREATE INDEX idx_policy_column_permissions_policy ON policy_column_permissions(policy_id,column_ordinal,usage);
CREATE TABLE control_plane_compat (
 fence_key TEXT PRIMARY KEY,min_reader_protocol INTEGER NOT NULL DEFAULT 2 CHECK(min_reader_protocol>=2),max_writer_protocol INTEGER NOT NULL DEFAULT 2 CHECK(max_writer_protocol>=min_reader_protocol),
 fence_epoch BIGINT NOT NULL DEFAULT 1 CHECK(fence_epoch>0),state TEXT NOT NULL DEFAULT 'protocol2' CHECK(state IN('protocol2','staging','protocol3','frozen')),
 revision BIGINT NOT NULL DEFAULT 1 CHECK(revision>0),updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO control_plane_compat(fence_key) VALUES('global');
CREATE TABLE runtime_instances (
 instance_id TEXT PRIMARY KEY,protocol_version INTEGER NOT NULL CHECK(protocol_version>=1),artifact_digest TEXT NOT NULL,status TEXT NOT NULL DEFAULT 'active' CHECK(status IN('active','draining','revoked')),
 last_heartbeat_at TIMESTAMPTZ NOT NULL,lease_expires_at TIMESTAMPTZ NOT NULL,revision BIGINT NOT NULL DEFAULT 1 CHECK(revision>0),CHECK(lease_expires_at>last_heartbeat_at)
);
CREATE INDEX idx_runtime_instances_lease ON runtime_instances(status,lease_expires_at);
CREATE FUNCTION agentsql_bump_policy_revision() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE guarded text:=current_setting('agentsql.policy_mutation_guard',true); BEGIN IF guarded='on' THEN RETURN COALESCE(NEW,OLD); END IF;
UPDATE public.policies SET revision=revision+1,updated_at=now() WHERE id=COALESCE(NEW.policy_id,OLD.policy_id); RETURN COALESCE(NEW,OLD); END $$;
CREATE TRIGGER policy_permission_revision AFTER INSERT OR UPDATE OR DELETE ON policy_column_permissions FOR EACH ROW EXECUTE FUNCTION agentsql_bump_policy_revision();
CREATE TRIGGER policy_binding_revision AFTER INSERT OR UPDATE OR DELETE ON relation_policy_bindings FOR EACH ROW EXECUTE FUNCTION agentsql_bump_policy_revision();
CREATE FUNCTION agentsql_replace_policy_permissions(p_policy_id text,p_expected_revision bigint,p_permissions jsonb) RETURNS bigint LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE next_revision bigint; BEGIN
IF session_user<>current_user AND session_user<>COALESCE(current_setting('agentsql.metadata_writer_role',true),'') THEN RAISE EXCEPTION 'metadata writer role rejected' USING ERRCODE='42501'; END IF;
PERFORM pg_catalog.pg_advisory_xact_lock(4707195423570611779);
IF NOT EXISTS(SELECT 1 FROM public.control_plane_compat WHERE fence_key='global' AND state<>'frozen' AND max_writer_protocol>=2) THEN RAISE EXCEPTION 'control fence rejected' USING ERRCODE='55000'; END IF;
PERFORM pg_catalog.set_config('agentsql.policy_mutation_guard','on',true);
SELECT revision+1 INTO next_revision FROM public.policies WHERE id=p_policy_id AND revision=p_expected_revision FOR UPDATE;
IF next_revision IS NULL THEN RAISE EXCEPTION 'policy revision mismatch' USING ERRCODE='40001'; END IF;
DELETE FROM public.policy_column_permissions WHERE policy_id=p_policy_id;
INSERT INTO public.policy_column_permissions(policy_id,relation_enrollment_id,column_ordinal,column_name,column_type_digest,usage,parent_revision)
SELECT p_policy_id,x.relation_enrollment_id,x.column_ordinal,x.column_name,x.column_type_digest,x.usage,next_revision FROM pg_catalog.jsonb_to_recordset(p_permissions) AS x(relation_enrollment_id text,column_ordinal integer,column_name text,column_type_digest text,usage text);
UPDATE public.policies SET revision=next_revision,updated_at=now() WHERE id=p_policy_id; RETURN next_revision; END $$;
REVOKE ALL ON FUNCTION agentsql_replace_policy_permissions(text,bigint,jsonb) FROM PUBLIC;
REVOKE INSERT,UPDATE,DELETE ON relation_policy_bindings,policy_column_permission_staging,policy_column_permissions,control_plane_compat,runtime_instances FROM PUBLIC;
