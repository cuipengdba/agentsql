CREATE TABLE b5_sessions (
  session_id TEXT PRIMARY KEY CHECK (length(session_id) BETWEEN 1 AND 128),
  agent_id TEXT NOT NULL REFERENCES agents(id),
  tenant_id TEXT NOT NULL CHECK (length(tenant_id) BETWEEN 1 AND 128),
  principal_id TEXT NOT NULL CHECK (length(principal_id) BETWEEN 1 AND 128),
  owner_instance_id TEXT NOT NULL CHECK (length(owner_instance_id) BETWEEN 1 AND 128),
  owner_epoch BIGINT NOT NULL CHECK (owner_epoch > 0),
  continuation_schema_id TEXT NOT NULL CHECK (length(continuation_schema_id) BETWEEN 1 AND 64),
  continuation_schema_version INTEGER NOT NULL CHECK (continuation_schema_version > 0),
  continuation_key_ciphertext TEXT NOT NULL,
  continuation_hmac_digest BYTEA NOT NULL CHECK (length(continuation_hmac_digest) = 32),
  sticky_route TEXT NOT NULL CHECK (length(sticky_route) BETWEEN 1 AND 256),
  status TEXT NOT NULL CHECK (status IN ('READY','ACTIVE','TERMINAL','EXPIRED')),
  idle_expires_at TIMESTAMPTZ NOT NULL,
  absolute_expires_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
  CHECK (idle_expires_at <= absolute_expires_at)
);
CREATE INDEX idx_b5_sessions_owner ON b5_sessions(owner_instance_id, owner_epoch, status);
CREATE INDEX idx_b5_sessions_ttl ON b5_sessions(status, idle_expires_at, absolute_expires_at);

CREATE TABLE b5_transactions (
  transaction_id TEXT PRIMARY KEY CHECK (length(transaction_id) BETWEEN 1 AND 128),
  session_id TEXT NOT NULL REFERENCES b5_sessions(session_id) ON DELETE CASCADE,
  datasource_id TEXT NOT NULL REFERENCES datasources(id),
  status TEXT NOT NULL CHECK (status IN ('PENDING','ACTIVE','ROLLBACK_ONLY','TERMINAL')),
  phase TEXT NOT NULL CHECK (phase IN ('READY','PLAN_READY','APPROVAL_CONSUMED','CONNECTION_PINNED','NATIVE_BEGUN','CONTEXT_FIXED','SEALED_IN_TX','BEGIN_AUDITING','ACTIVE','BEGIN_FAIL_TERMINATING','ROLLBACK_ONLY','COMMITTING','ROLLING_BACK','TERMINAL','FINAL_FENCE')),
  plan_digest BYTEA NOT NULL CHECK (length(plan_digest) = 32),
  approval_id TEXT,
  owner_epoch BIGINT NOT NULL CHECK (owner_epoch > 0),
  idle_deadline TIMESTAMPTZ NOT NULL,
  wall_deadline TIMESTAMPTZ NOT NULL,
  statement_deadline TIMESTAMPTZ,
  backend_pid INTEGER,
  backend_secret_digest BYTEA CHECK (backend_secret_digest IS NULL OR length(backend_secret_digest) = 32),
  backend_started_at TIMESTAMPTZ,
  connection_generation BIGINT NOT NULL DEFAULT 0 CHECK (connection_generation >= 0),
  lease_generation BIGINT NOT NULL DEFAULT 0 CHECK (lease_generation >= 0),
  statement_count INTEGER NOT NULL DEFAULT 0 CHECK (statement_count BETWEEN 0 AND 32),
  transaction_seq BIGINT NOT NULL DEFAULT 0 CHECK (transaction_seq >= 0),
  previous_tx_event_digest BYTEA CHECK (previous_tx_event_digest IS NULL OR length(previous_tx_event_digest) = 32),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
  CHECK (idle_deadline <= wall_deadline)
);
CREATE UNIQUE INDEX idx_b5_transactions_one_live_session ON b5_transactions(session_id) WHERE status <> 'TERMINAL';
CREATE INDEX idx_b5_transactions_deadline ON b5_transactions(status, idle_deadline, wall_deadline);
CREATE INDEX idx_b5_transactions_datasource ON b5_transactions(datasource_id, status);

CREATE TABLE b5_dml_grants (
  grant_id TEXT PRIMARY KEY CHECK (length(grant_id) BETWEEN 1 AND 128),
  policy_id TEXT NOT NULL REFERENCES policies(id) ON DELETE CASCADE,
  policy_revision BIGINT NOT NULL CHECK (policy_revision > 0),
  principal_id TEXT NOT NULL CHECK (length(principal_id) BETWEEN 1 AND 128),
  datasource_id TEXT NOT NULL REFERENCES datasources(id),
  effect TEXT NOT NULL CHECK (effect IN ('ALLOW','DENY')),
  grant_element TEXT NOT NULL CHECK (grant_element IN ('ACTION','WRITE_TARGET','REFERENCE')),
  action TEXT NOT NULL CHECK (action IN ('INSERT','UPDATE','DELETE')),
  database_oid BIGINT NOT NULL CHECK (database_oid BETWEEN 1 AND 4294967295),
  relation_oid BIGINT NOT NULL CHECK (relation_oid BETWEEN 1 AND 4294967295),
  relation_kind TEXT NOT NULL CHECK (length(relation_kind) = 1),
  schema_name TEXT NOT NULL CHECK (schema_name <> ''),
  relation_name TEXT NOT NULL CHECK (relation_name <> ''),
  catalog_fingerprint TEXT NOT NULL CHECK (catalog_fingerprint <> ''),
  write_target_kind TEXT CHECK (write_target_kind IS NULL OR write_target_kind IN ('COLUMN','ROW')),
  column_attnum INTEGER CHECK (column_attnum IS NULL OR column_attnum BETWEEN -32768 AND 32767),
  column_name TEXT,
  column_type_oid BIGINT CHECK (column_type_oid IS NULL OR column_type_oid BETWEEN 1 AND 4294967295),
  column_type_modifier INTEGER,
  column_collation_oid BIGINT CHECK (column_collation_oid IS NULL OR column_collation_oid BETWEEN 0 AND 4294967295),
  reference_kind TEXT CHECK (reference_kind IS NULL OR reference_kind IN ('COLUMN','WHOLE_ROW','ROW_COUNT','RECORD','COMPOSITE','SYSTEM_COLUMN')),
  proof_schema_id TEXT NOT NULL CHECK (length(proof_schema_id) BETWEEN 1 AND 64),
  proof_schema_version INTEGER NOT NULL CHECK (proof_schema_version > 0),
  proof_digest BYTEA NOT NULL CHECK (length(proof_digest) = 32),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
  CHECK (
    (grant_element = 'ACTION' AND write_target_kind IS NULL AND column_attnum IS NULL AND reference_kind IS NULL) OR
    (grant_element = 'WRITE_TARGET' AND write_target_kind IS NOT NULL AND reference_kind IS NULL AND
      ((write_target_kind = 'ROW' AND action = 'DELETE' AND column_attnum IS NULL) OR
       (write_target_kind = 'COLUMN' AND column_attnum > 0 AND column_name IS NOT NULL AND column_type_oid > 0))) OR
    (grant_element = 'REFERENCE' AND write_target_kind IS NULL AND reference_kind IS NOT NULL)
  )
);
CREATE UNIQUE INDEX idx_b5_dml_grants_identity ON b5_dml_grants(policy_id, policy_revision, grant_element, action, relation_oid, COALESCE(column_attnum,0), COALESCE(write_target_kind,''), COALESCE(reference_kind,''));
CREATE INDEX idx_b5_dml_grants_lookup ON b5_dml_grants(principal_id, datasource_id, action, effect);

CREATE TABLE b5_result_receipts (
  session_id TEXT NOT NULL REFERENCES b5_sessions(session_id) ON DELETE CASCADE,
  request_id TEXT NOT NULL CHECK (length(request_id) BETWEEN 1 AND 128),
  event_uuid BYTEA NOT NULL CHECK (length(event_uuid) = 16),
  attempt_generation BIGINT NOT NULL CHECK (attempt_generation > 0),
  schema_id TEXT NOT NULL CHECK (length(schema_id) BETWEEN 1 AND 64),
  schema_version INTEGER NOT NULL CHECK (schema_version > 0),
  business_event_digest BYTEA NOT NULL CHECK (length(business_event_digest) = 32),
  wal_append_receipt_digest BYTEA NOT NULL CHECK (length(wal_append_receipt_digest) = 32),
  reported_durability TEXT NOT NULL CHECK (reported_durability IN ('DURABLE','AUDIT_PENDING','DURABILITY_LOST')),
  append_confirmation TEXT NOT NULL DEFAULT 'UNKNOWN' CHECK (append_confirmation IN ('UNKNOWN','TIMEOUT_UNCONFIRMED','LATE_CONFIRMED_AFTER_RESPONSE','RECOVERED_ON_RESTART')),
  reconciliation TEXT NOT NULL DEFAULT 'NONE' CHECK (reconciliation IN ('NONE','REPLAY_STAGED','PRIMARY_DURABLE')),
  delivery_status TEXT NOT NULL DEFAULT 'PREPARED' CHECK (delivery_status IN ('PREPARED','SEND_STARTED','SEND_COMPLETED')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
  PRIMARY KEY(session_id, request_id, event_uuid, attempt_generation)
);
CREATE UNIQUE INDEX idx_b5_result_receipts_event ON b5_result_receipts(event_uuid, business_event_digest);
CREATE INDEX idx_b5_result_receipts_reconcile ON b5_result_receipts(reconciliation, append_confirmation, updated_at);
CREATE INDEX idx_b5_result_receipts_delivery ON b5_result_receipts(delivery_status, updated_at);

CREATE TABLE b5_tx_events (
  transaction_id TEXT NOT NULL REFERENCES b5_transactions(transaction_id) ON DELETE CASCADE,
  transaction_seq BIGINT NOT NULL CHECK (transaction_seq > 0),
  event_uuid BYTEA NOT NULL UNIQUE CHECK (length(event_uuid) = 16),
  event_type TEXT NOT NULL CHECK (length(event_type) BETWEEN 1 AND 64),
  event_schema_id TEXT NOT NULL CHECK (length(event_schema_id) BETWEEN 1 AND 64),
  event_schema_version INTEGER NOT NULL CHECK (event_schema_version > 0),
  previous_tx_event_digest BYTEA CHECK (previous_tx_event_digest IS NULL OR length(previous_tx_event_digest) = 32),
  event_digest BYTEA NOT NULL CHECK (length(event_digest) = 32),
  canonical_event BYTEA NOT NULL CHECK (length(canonical_event) BETWEEN 1 AND 16384),
  terminal_evidence_text TEXT,
  disposition_proof_text TEXT,
  audit_log_id BIGINT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY(transaction_id, transaction_seq)
);
CREATE INDEX idx_b5_tx_events_audit ON b5_tx_events(audit_log_id) WHERE audit_log_id IS NOT NULL;
REVOKE INSERT, UPDATE, DELETE ON b5_sessions, b5_transactions, b5_dml_grants, b5_result_receipts, b5_tx_events FROM PUBLIC;
