ALTER TABLE policies ADD COLUMN relation_binding_id TEXT;
ALTER TABLE policies ADD COLUMN revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0);
ALTER TABLE policies ADD COLUMN legacy_unrepresentable INTEGER NOT NULL DEFAULT 0 CHECK (legacy_unrepresentable IN (0,1));

CREATE TABLE relation_policy_bindings (
  id                    TEXT PRIMARY KEY,
  policy_id             TEXT NOT NULL UNIQUE REFERENCES policies(id) ON DELETE CASCADE,
  datasource_id         TEXT NOT NULL REFERENCES datasources(id),
  schema_name           TEXT NOT NULL DEFAULT '',
  relation_name         TEXT NOT NULL,
  stable_object_id      TEXT,
  catalog_fingerprint   TEXT,
  status                TEXT NOT NULL DEFAULT 'staging' CHECK (status IN ('staging','healthy','needs_rebind','unsupported','revoked')),
  revision              INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_relation_policy_bindings_datasource ON relation_policy_bindings(datasource_id, relation_name);

CREATE TABLE policy_column_permission_staging (
  policy_id          TEXT NOT NULL REFERENCES policies(id) ON DELETE CASCADE,
  token_ordinal      INTEGER NOT NULL CHECK (token_ordinal > 0),
  legacy_token       TEXT NOT NULL,
  requested_usage    TEXT NOT NULL CHECK (requested_usage IN ('output','reference')),
  source_csv_sha256  TEXT NOT NULL CHECK (length(source_csv_sha256) = 64),
  bind_status        TEXT NOT NULL DEFAULT 'pending' CHECK (bind_status IN ('pending','bound','error')),
  error_code         TEXT,
  PRIMARY KEY(policy_id, token_ordinal, requested_usage)
);

CREATE TABLE policy_column_permissions (
  policy_id               TEXT NOT NULL REFERENCES policies(id) ON DELETE CASCADE,
  relation_enrollment_id  TEXT NOT NULL,
  column_ordinal          INTEGER NOT NULL CHECK (column_ordinal > 0),
  column_name             TEXT NOT NULL CHECK (column_name <> '' AND column_name <> '*'),
  column_type_digest      TEXT NOT NULL CHECK (column_type_digest <> ''),
  usage                   TEXT NOT NULL CHECK (usage IN ('output','reference')),
  parent_revision         INTEGER NOT NULL CHECK (parent_revision > 0),
  PRIMARY KEY(policy_id, relation_enrollment_id, column_ordinal, usage)
);
CREATE INDEX idx_policy_column_permissions_policy ON policy_column_permissions(policy_id, column_ordinal, usage);

CREATE TABLE control_plane_compat (
  fence_key             TEXT PRIMARY KEY,
  min_reader_protocol   INTEGER NOT NULL DEFAULT 2 CHECK (min_reader_protocol >= 2),
  max_writer_protocol   INTEGER NOT NULL DEFAULT 2 CHECK (max_writer_protocol >= min_reader_protocol),
  fence_epoch           INTEGER NOT NULL DEFAULT 1 CHECK (fence_epoch > 0),
  state                 TEXT NOT NULL DEFAULT 'protocol2' CHECK (state IN ('protocol2','staging','protocol3','frozen')),
  revision              INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  updated_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO control_plane_compat(fence_key) VALUES('global');

CREATE TABLE runtime_instances (
  instance_id       TEXT PRIMARY KEY,
  protocol_version  INTEGER NOT NULL CHECK (protocol_version >= 1),
  artifact_digest   TEXT NOT NULL,
  status            TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','draining','revoked')),
  last_heartbeat_at TIMESTAMP NOT NULL,
  lease_expires_at  TIMESTAMP NOT NULL,
  revision          INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  CHECK (lease_expires_at > last_heartbeat_at)
);
CREATE INDEX idx_runtime_instances_lease ON runtime_instances(status, lease_expires_at);

CREATE TRIGGER policy_permission_insert_bump AFTER INSERT ON policy_column_permissions
BEGIN UPDATE policies SET revision=revision+1, updated_at=CURRENT_TIMESTAMP WHERE id=NEW.policy_id; END;
CREATE TRIGGER policy_permission_update_bump AFTER UPDATE ON policy_column_permissions
BEGIN UPDATE policies SET revision=revision+1, updated_at=CURRENT_TIMESTAMP WHERE id=NEW.policy_id; END;
CREATE TRIGGER policy_permission_delete_bump AFTER DELETE ON policy_column_permissions
BEGIN UPDATE policies SET revision=revision+1, updated_at=CURRENT_TIMESTAMP WHERE id=OLD.policy_id; END;
CREATE TRIGGER policy_binding_insert_bump AFTER INSERT ON relation_policy_bindings
BEGIN UPDATE policies SET relation_binding_id=NEW.id, revision=revision+1, updated_at=CURRENT_TIMESTAMP WHERE id=NEW.policy_id; END;
CREATE TRIGGER policy_binding_update_bump AFTER UPDATE ON relation_policy_bindings
BEGIN UPDATE policies SET revision=revision+1, updated_at=CURRENT_TIMESTAMP WHERE id=NEW.policy_id; END;
CREATE TRIGGER policy_binding_delete_bump AFTER DELETE ON relation_policy_bindings
BEGIN UPDATE policies SET relation_binding_id=NULL, revision=revision+1, updated_at=CURRENT_TIMESTAMP WHERE id=OLD.policy_id; END;
