DROP INDEX IF EXISTS idx_approvals_requester_user;
ALTER TABLE approvals DROP COLUMN requester_user_id;
