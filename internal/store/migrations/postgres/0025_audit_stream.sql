-- The row lock on each tenant's counter is held until the audit insert commits.
-- This makes the stream sequence safe for resume even when audit IDs commit out
-- of allocation order; tenants do not serialize one another.
CREATE TABLE audit_stream_state (
  tenant_id TEXT PRIMARY KEY,
  next_seq BIGINT NOT NULL CHECK(next_seq >= 0)
);
CREATE TABLE audit_stream_events (
  tenant_id TEXT NOT NULL,
  seq BIGINT NOT NULL CHECK(seq > 0),
  audit_id BIGINT NOT NULL UNIQUE REFERENCES audit_logs(id) ON DELETE CASCADE,
  PRIMARY KEY(tenant_id, seq)
);
INSERT INTO audit_stream_state(tenant_id,next_seq)
SELECT tenant_id, COUNT(*) FROM audit_logs GROUP BY tenant_id;
INSERT INTO audit_stream_events(tenant_id,seq,audit_id)
SELECT tenant_id, ROW_NUMBER() OVER (PARTITION BY tenant_id ORDER BY id), id
FROM audit_logs;
CREATE FUNCTION agentsql_audit_stream_append() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE assigned BIGINT;
BEGIN
  INSERT INTO audit_stream_state(tenant_id,next_seq) VALUES(NEW.tenant_id,1)
  ON CONFLICT(tenant_id) DO UPDATE SET next_seq=audit_stream_state.next_seq+1
  RETURNING next_seq INTO assigned;
  INSERT INTO audit_stream_events(tenant_id,seq,audit_id)
  VALUES(NEW.tenant_id,assigned,NEW.id);
  RETURN NEW;
END;
$$;
CREATE TRIGGER agentsql_audit_stream_append AFTER INSERT ON audit_logs
FOR EACH ROW EXECUTE FUNCTION agentsql_audit_stream_append();
