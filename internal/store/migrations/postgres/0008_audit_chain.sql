ALTER TABLE audit_logs ADD COLUMN chain_seq BIGINT;
ALTER TABLE audit_logs ADD COLUMN prev_hash TEXT;
ALTER TABLE audit_logs ADD COLUMN self_hash TEXT;
ALTER TABLE audit_logs ADD COLUMN chain_key_version INTEGER;
ALTER TABLE audit_logs ADD COLUMN chain_format_version INTEGER;

CREATE TABLE chain_state (
  chain_id TEXT PRIMARY KEY CHECK (chain_id IN ('management','traffic')),
  chain_instance_id TEXT,
  status TEXT NOT NULL CHECK (status IN ('DISABLED','BUILDING','ACTIVE','FAILED')),
  mode TEXT CHECK (mode IS NULL OR mode IN ('keyless','hmac')),
  head_seq BIGINT NOT NULL DEFAULT 0,
  head_id BIGINT,
  head_hash TEXT,
  genesis_at TIMESTAMPTZ,
  protected_since_id BIGINT,
  build_owner TEXT,
  build_lease_until TIMESTAMPTZ,
  build_epoch INTEGER NOT NULL DEFAULT 0,
  last_built_id BIGINT,
  last_built_seq BIGINT,
  last_built_hash TEXT,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE chain_verification (
  chain_id TEXT PRIMARY KEY,
  observed_instance_id TEXT,
  observed_head_hash TEXT,
  result TEXT,
  last_verified_head_seq BIGINT,
  last_verified_at TIMESTAMPTZ,
  break_seq BIGINT,
  break_id BIGINT,
  break_reason TEXT
);

INSERT INTO chain_state (chain_id, status) VALUES ('management', 'DISABLED');
INSERT INTO chain_verification (chain_id) VALUES ('management');
