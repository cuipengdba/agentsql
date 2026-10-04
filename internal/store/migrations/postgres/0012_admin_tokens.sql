CREATE TABLE IF NOT EXISTS admin_access_revocations (
  jti        TEXT PRIMARY KEY,
  expires_at TIMESTAMPTZ NOT NULL,
  revoked_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS admin_refresh_families (
  family_id  TEXT PRIMARY KEY,
  tenant_id  TEXT NOT NULL,
  user_id    TEXT NOT NULL,
  username   TEXT NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  revoked_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS admin_refresh_tokens (
  token_hash       TEXT PRIMARY KEY CHECK (length(token_hash) = 64),
  family_id        TEXT NOT NULL REFERENCES admin_refresh_families(family_id) ON DELETE CASCADE,
  expires_at       TIMESTAMPTZ NOT NULL,
  state            TEXT NOT NULL CHECK (state IN ('active','rotated','revoked')),
  replaced_by_hash TEXT,
  created_at       TIMESTAMPTZ NOT NULL,
  used_at          TIMESTAMPTZ,
  revoked_at       TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_admin_access_revocations_expiry ON admin_access_revocations(expires_at);
CREATE INDEX IF NOT EXISTS idx_admin_refresh_families_expiry ON admin_refresh_families(expires_at);
CREATE INDEX IF NOT EXISTS idx_admin_refresh_tokens_family ON admin_refresh_tokens(family_id);
