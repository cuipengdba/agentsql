CREATE TABLE auth_identities (
  tenant_id TEXT NOT NULL, user_id TEXT NOT NULL,
  provider TEXT NOT NULL CHECK (provider IN ('oidc','ldap')), subject TEXT NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (tenant_id, provider, subject), UNIQUE (tenant_id, user_id, provider),
  FOREIGN KEY (user_id, tenant_id) REFERENCES users(id, tenant_id) ON DELETE CASCADE
);
CREATE TABLE user_mfa (
  tenant_id TEXT NOT NULL, user_id TEXT NOT NULL, secret_ciphertext TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('pending','enabled')), last_counter INTEGER NOT NULL DEFAULT -1,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (tenant_id, user_id), FOREIGN KEY (user_id, tenant_id) REFERENCES users(id, tenant_id) ON DELETE CASCADE
);
CREATE TABLE user_mfa_recovery_codes (
  tenant_id TEXT NOT NULL, user_id TEXT NOT NULL, code_hash TEXT NOT NULL CHECK (length(code_hash)=64),
  used_at TIMESTAMP, created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (tenant_id, user_id, code_hash), FOREIGN KEY (tenant_id, user_id) REFERENCES user_mfa(tenant_id, user_id) ON DELETE CASCADE
);
CREATE TABLE auth_login_challenges (
  challenge_hash TEXT PRIMARY KEY CHECK (length(challenge_hash)=64), tenant_id TEXT NOT NULL,
  user_id TEXT NOT NULL, username TEXT NOT NULL, expires_at TIMESTAMP NOT NULL, used_at TIMESTAMP,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY (user_id, tenant_id) REFERENCES users(id, tenant_id) ON DELETE CASCADE
);
CREATE TABLE oidc_auth_requests (
  state_hash TEXT PRIMARY KEY CHECK (length(state_hash)=64), nonce TEXT NOT NULL, pkce_verifier TEXT NOT NULL,
  return_to TEXT NOT NULL, expires_at TIMESTAMP NOT NULL, used_at TIMESTAMP,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_auth_identities_user ON auth_identities(tenant_id,user_id);
CREATE INDEX idx_auth_challenges_expiry ON auth_login_challenges(expires_at);
CREATE INDEX idx_oidc_requests_expiry ON oidc_auth_requests(expires_at);
