-- A per-tenant transactional counter gives the stream commit order. Audit IDs
-- are allocated before commit and cannot safely serve as a resumable cursor.
CREATE TABLE audit_stream_state (
  tenant_id TEXT PRIMARY KEY,
  next_seq INTEGER NOT NULL CHECK(next_seq >= 0)
);
CREATE TABLE audit_stream_events (
  tenant_id TEXT NOT NULL,
  seq INTEGER NOT NULL CHECK(seq > 0),
  audit_id INTEGER NOT NULL UNIQUE REFERENCES audit_logs(id) ON DELETE CASCADE,
  PRIMARY KEY(tenant_id, seq)
);
INSERT INTO audit_stream_state(tenant_id,next_seq)
SELECT tenant_id, COUNT(*) FROM audit_logs GROUP BY tenant_id;
INSERT INTO audit_stream_events(tenant_id,seq,audit_id)
SELECT tenant_id, ROW_NUMBER() OVER (PARTITION BY tenant_id ORDER BY id), id
FROM audit_logs;
CREATE TRIGGER agentsql_audit_stream_append AFTER INSERT ON audit_logs
BEGIN
  INSERT INTO audit_stream_state(tenant_id,next_seq) VALUES(NEW.tenant_id,1)
  ON CONFLICT(tenant_id) DO UPDATE SET next_seq=next_seq+1;
  INSERT INTO audit_stream_events(tenant_id,seq,audit_id)
  VALUES(NEW.tenant_id,(SELECT next_seq FROM audit_stream_state WHERE tenant_id=NEW.tenant_id),NEW.id);
END;
