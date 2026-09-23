ALTER TABLE audit_logs ADD COLUMN event_uuid TEXT;
CREATE UNIQUE INDEX ux_audit_logs_event_uuid
  ON audit_logs(event_uuid) WHERE event_uuid IS NOT NULL;
