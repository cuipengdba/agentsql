ALTER TABLE approvals ADD COLUMN requester_user_id TEXT;
CREATE INDEX idx_approvals_requester_user ON approvals (tenant_id, requester_user_id, status);
