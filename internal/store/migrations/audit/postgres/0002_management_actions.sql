ALTER TABLE audit_logs ADD COLUMN action TEXT;
ALTER TABLE audit_logs ADD COLUMN actor_type TEXT;
ALTER TABLE audit_logs ADD COLUMN actor_id TEXT;
ALTER TABLE audit_logs ADD COLUMN details_json TEXT;
