CREATE TABLE IF NOT EXISTS mcp_stream_event_cursors (
  session_id       TEXT NOT NULL,
  stream_id        TEXT NOT NULL,
  next_event_index BIGINT NOT NULL DEFAULT 0 CHECK (next_event_index >= 0),
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (session_id, stream_id)
);

CREATE TABLE IF NOT EXISTS mcp_stream_events (
  session_id  TEXT NOT NULL,
  stream_id   TEXT NOT NULL,
  event_index BIGINT NOT NULL CHECK (event_index >= 0),
  data        BYTEA NOT NULL,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (session_id, stream_id, event_index)
);

CREATE INDEX IF NOT EXISTS idx_mcp_stream_events_session ON mcp_stream_events(session_id);
CREATE INDEX IF NOT EXISTS idx_mcp_stream_events_created_at ON mcp_stream_events(created_at);
CREATE INDEX IF NOT EXISTS idx_mcp_stream_event_cursors_updated_at ON mcp_stream_event_cursors(updated_at);
