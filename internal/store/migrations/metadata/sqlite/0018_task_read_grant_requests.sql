CREATE TABLE task_read_grant_requests (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL,
  requester_user_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  datasource_id TEXT NOT NULL,
  sql_sha256 TEXT NOT NULL CHECK (length(sql_sha256) = 64),
  sql_ciphertext TEXT NOT NULL,
  request_digest TEXT NOT NULL CHECK (length(request_digest) = 64),
  idempotency_key TEXT NOT NULL,
  expires_at TIMESTAMP NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected', 'issued')),
  reviewer_user_id TEXT,
  reviewed_at TIMESTAMP,
  issued_at TIMESTAMP,
  grant_id TEXT UNIQUE,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY (requester_user_id, tenant_id) REFERENCES users(id, tenant_id),
  FOREIGN KEY (agent_id, tenant_id) REFERENCES agents(id, tenant_id),
  FOREIGN KEY (datasource_id, tenant_id) REFERENCES datasources(id, tenant_id),
  FOREIGN KEY (reviewer_user_id, tenant_id) REFERENCES users(id, tenant_id),
  FOREIGN KEY (grant_id) REFERENCES task_read_grants(id) DEFERRABLE INITIALLY DEFERRED
);
CREATE UNIQUE INDEX ux_task_read_grant_requests_idempotency ON task_read_grant_requests(tenant_id, requester_user_id, idempotency_key);
CREATE INDEX idx_task_read_grant_requests_tenant_status ON task_read_grant_requests(tenant_id, status, created_at);
