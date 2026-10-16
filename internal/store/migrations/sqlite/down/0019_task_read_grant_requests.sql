UPDATE task_read_grants SET revoked_at = CURRENT_TIMESTAMP WHERE id IN (SELECT grant_id FROM task_read_grant_requests WHERE grant_id IS NOT NULL) AND revoked_at IS NULL;
DROP TABLE task_read_grant_requests;
