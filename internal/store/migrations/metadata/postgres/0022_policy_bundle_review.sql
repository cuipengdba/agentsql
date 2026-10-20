CREATE TABLE policy_bundle_proposals (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL REFERENCES tenants(id),
  base_digest TEXT NOT NULL,
  desired_digest TEXT NOT NULL,
  desired_json TEXT NOT NULL,
  author_user_id TEXT NOT NULL,
  reviewer_user_id TEXT,
  status TEXT NOT NULL CHECK (status IN ('pending','approved','rejected','applied')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  reviewed_at TIMESTAMPTZ,
  applied_at TIMESTAMPTZ
);
CREATE INDEX idx_policy_bundle_proposals_tenant ON policy_bundle_proposals(tenant_id,created_at);

CREATE TABLE policy_bundle_baselines (
  tenant_id TEXT PRIMARY KEY REFERENCES tenants(id),
  digest TEXT NOT NULL,
  proposal_id TEXT NOT NULL REFERENCES policy_bundle_proposals(id),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
