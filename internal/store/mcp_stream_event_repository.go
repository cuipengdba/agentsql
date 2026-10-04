package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// ErrStreamEventsPurged indicates that at least one event required for a
	// contiguous replay is no longer available.
	ErrStreamEventsPurged = errors.New("stream events purged and no longer available")
	// ErrMCPStreamNotFound indicates that the requested stream was never opened
	// or that its expired cursor has already been reclaimed.
	ErrMCPStreamNotFound = errors.New("MCP stream not found")
)

const mcpStreamEventAdvisoryLock int64 = 0x4153514c455654

// MCPStreamEventRepository persists resumable MCP SSE stream events in the
// metadata database. The cursor row survives event purges so event indexes do
// not restart and replay can fail closed when a requested prefix is gone.
type MCPStreamEventRepository struct {
	repositoryBase
}

// OpenStream idempotently creates the durable cursor for a logical SSE stream.
func (repository *MCPStreamEventRepository) OpenStream(ctx context.Context, sessionID, streamID string) error {
	if err := validateMCPStreamInput(ctx, sessionID); err != nil {
		return fmt.Errorf("open MCP stream: %w", err)
	}
	transaction, err := repository.beginEventTransaction(ctx)
	if err != nil {
		return fmt.Errorf("open MCP stream: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	now := time.Now().UTC()
	_, err = transaction.ExecContext(ctx, repository.bind(`
INSERT INTO mcp_stream_event_cursors(session_id,stream_id,next_event_index,updated_at)
VALUES(?,?,0,?)
ON CONFLICT(session_id,stream_id) DO UPDATE SET updated_at=excluded.updated_at`), sessionID, streamID, now)
	if err != nil {
		return fmt.Errorf("open MCP stream: persist cursor: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("open MCP stream: commit: %w", err)
	}
	return nil
}

// AppendStreamEvent atomically allocates the next stream index, appends the
// event, and enforces TTL and aggregate byte bounds.
func (repository *MCPStreamEventRepository) AppendStreamEvent(
	ctx context.Context,
	sessionID string,
	streamID string,
	data []byte,
	maxBytes int,
	ttl time.Duration,
) error {
	if err := validateMCPStreamLimits(ctx, sessionID, maxBytes, ttl); err != nil {
		return fmt.Errorf("append MCP stream event: %w", err)
	}
	transaction, err := repository.beginEventTransaction(ctx)
	if err != nil {
		return fmt.Errorf("append MCP stream event: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	now := time.Now().UTC()
	_, err = transaction.ExecContext(ctx, repository.bind(`
INSERT INTO mcp_stream_event_cursors(session_id,stream_id,next_event_index,updated_at)
VALUES(?,?,0,?)
ON CONFLICT(session_id,stream_id) DO NOTHING`), sessionID, streamID, now)
	if err != nil {
		return fmt.Errorf("append MCP stream event: ensure cursor: %w", err)
	}
	query := `SELECT next_event_index FROM mcp_stream_event_cursors WHERE session_id=? AND stream_id=?`
	if repository.dialect == DialectPostgres {
		query += " FOR UPDATE"
	}
	var eventIndex int64
	if err := transaction.QueryRowContext(ctx, repository.bind(query), sessionID, streamID).Scan(&eventIndex); err != nil {
		return fmt.Errorf("append MCP stream event: read cursor: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, repository.bind(`
INSERT INTO mcp_stream_events(session_id,stream_id,event_index,data,created_at)
VALUES(?,?,?,?,?)`), sessionID, streamID, eventIndex, data, now); err != nil {
		return fmt.Errorf("append MCP stream event: insert event: %w", err)
	}
	result, err := transaction.ExecContext(ctx, repository.bind(`
UPDATE mcp_stream_event_cursors
SET next_event_index=?,updated_at=?
WHERE session_id=? AND stream_id=? AND next_event_index=?`), eventIndex+1, now, sessionID, streamID, eventIndex)
	if err != nil {
		return fmt.Errorf("append MCP stream event: advance cursor: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("append MCP stream event: read cursor update result: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("append MCP stream event: cursor update affected %d rows", affected)
	}
	if err := repository.purgeStreamEventsTx(ctx, transaction, maxBytes, ttl, now); err != nil {
		return fmt.Errorf("append MCP stream event: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("append MCP stream event: commit: %w", err)
	}
	return nil
}

// StreamEventsAfter returns a complete, ordered replay strictly after
// afterIndex. It returns ErrStreamEventsPurged without any events if the
// retained sequence has a gap.
func (repository *MCPStreamEventRepository) StreamEventsAfter(
	ctx context.Context,
	sessionID string,
	streamID string,
	afterIndex int,
) (firstIndex int, events [][]byte, err error) {
	if err := validateMCPStreamInput(ctx, sessionID); err != nil {
		return 0, nil, fmt.Errorf("read MCP stream events: %w", err)
	}
	if afterIndex < -1 {
		return 0, nil, fmt.Errorf("read MCP stream events: after index must be >= -1")
	}
	transaction, err := repository.beginEventTransaction(ctx)
	if err != nil {
		return 0, nil, fmt.Errorf("read MCP stream events: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	var nextIndex int64
	err = transaction.QueryRowContext(ctx, repository.bind(`
SELECT next_event_index FROM mcp_stream_event_cursors WHERE session_id=? AND stream_id=?`), sessionID, streamID).Scan(&nextIndex)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil, fmt.Errorf("read MCP stream events: %w", ErrMCPStreamNotFound)
	}
	if err != nil {
		return 0, nil, fmt.Errorf("read MCP stream events: read cursor: %w", err)
	}
	rows, err := transaction.QueryContext(ctx, repository.bind(`
SELECT event_index,data
FROM mcp_stream_events
WHERE session_id=? AND stream_id=? AND event_index>?
ORDER BY event_index`), sessionID, streamID, afterIndex)
	if err != nil {
		return 0, nil, fmt.Errorf("read MCP stream events: query: %w", err)
	}
	indexes := make([]int64, 0)
	events = make([][]byte, 0)
	for rows.Next() {
		var index int64
		var data []byte
		if err := rows.Scan(&index, &data); err != nil {
			_ = rows.Close()
			return 0, nil, fmt.Errorf("read MCP stream events: scan: %w", err)
		}
		indexes = append(indexes, index)
		events = append(events, data)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, nil, fmt.Errorf("read MCP stream events: iterate: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, nil, fmt.Errorf("read MCP stream events: close rows: %w", err)
	}

	expected := int64(afterIndex) + 1
	for _, index := range indexes {
		if index != expected {
			return 0, nil, fmt.Errorf("read MCP stream events after %d: %w", afterIndex, ErrStreamEventsPurged)
		}
		expected++
	}
	if expected < nextIndex {
		return 0, nil, fmt.Errorf("read MCP stream events after %d: %w", afterIndex, ErrStreamEventsPurged)
	}
	if err := transaction.Commit(ctx); err != nil {
		return 0, nil, fmt.Errorf("read MCP stream events: commit: %w", err)
	}
	if len(indexes) == 0 {
		return int(nextIndex), events, nil
	}
	return int(indexes[0]), events, nil
}

// DeleteSessionStreams removes all cursors and events for a closed session.
func (repository *MCPStreamEventRepository) DeleteSessionStreams(ctx context.Context, sessionID string) error {
	if err := validateMCPStreamInput(ctx, sessionID); err != nil {
		return fmt.Errorf("delete MCP session streams: %w", err)
	}
	transaction, err := repository.beginEventTransaction(ctx)
	if err != nil {
		return fmt.Errorf("delete MCP session streams: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	if _, err := transaction.ExecContext(ctx, repository.bind(
		"DELETE FROM mcp_stream_events WHERE session_id=?",
	), sessionID); err != nil {
		return fmt.Errorf("delete MCP session stream events: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, repository.bind(
		"DELETE FROM mcp_stream_event_cursors WHERE session_id=?",
	), sessionID); err != nil {
		return fmt.Errorf("delete MCP session streams: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("delete MCP session streams: commit: %w", err)
	}
	return nil
}

// PurgeExpiredStreamEvents enforces TTL and aggregate byte limits. It is
// intentionally synchronous and caller-triggered; no cleanup goroutine exists.
func (repository *MCPStreamEventRepository) PurgeExpiredStreamEvents(ctx context.Context, maxBytes int, ttl time.Duration) error {
	if ctx == nil {
		return fmt.Errorf("purge MCP stream events: %w", ErrNilContext)
	}
	if maxBytes <= 0 || ttl <= 0 {
		return fmt.Errorf("purge MCP stream events: positive max bytes and TTL are required")
	}
	transaction, err := repository.beginEventTransaction(ctx)
	if err != nil {
		return fmt.Errorf("purge MCP stream events: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	if err := repository.purgeStreamEventsTx(ctx, transaction, maxBytes, ttl, time.Now().UTC()); err != nil {
		return fmt.Errorf("purge MCP stream events: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("purge MCP stream events: commit: %w", err)
	}
	return nil
}

func (repository *MCPStreamEventRepository) purgeStreamEventsTx(
	ctx context.Context,
	transaction chainTransaction,
	maxBytes int,
	ttl time.Duration,
	now time.Time,
) error {
	cutoff := now.Add(-ttl)
	if _, err := transaction.ExecContext(ctx, repository.bind(
		"DELETE FROM mcp_stream_events WHERE created_at < ?",
	), cutoff); err != nil {
		return fmt.Errorf("delete expired events: %w", err)
	}
	var totalBytes int64
	if err := transaction.QueryRowContext(ctx, "SELECT COALESCE(SUM(length(data)),0) FROM mcp_stream_events").Scan(&totalBytes); err != nil {
		return fmt.Errorf("measure retained events: %w", err)
	}
	if totalBytes > int64(maxBytes) {
		rows, err := transaction.QueryContext(ctx, `
SELECT session_id,stream_id,event_index,length(data)
FROM mcp_stream_events
ORDER BY created_at,session_id,stream_id,event_index`)
		if err != nil {
			return fmt.Errorf("select oldest events: %w", err)
		}
		type eventKey struct {
			sessionID string
			streamID  string
			index     int64
		}
		keys := make([]eventKey, 0)
		removed := int64(0)
		for rows.Next() && totalBytes-removed > int64(maxBytes) {
			var key eventKey
			var size int64
			if err := rows.Scan(&key.sessionID, &key.streamID, &key.index, &size); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan oldest event: %w", err)
			}
			keys = append(keys, key)
			removed += size
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("iterate oldest events: %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close oldest events: %w", err)
		}
		for _, key := range keys {
			if _, err := transaction.ExecContext(ctx, repository.bind(`
DELETE FROM mcp_stream_events WHERE session_id=? AND stream_id=? AND event_index=?`), key.sessionID, key.streamID, key.index); err != nil {
				return fmt.Errorf("delete oldest event: %w", err)
			}
		}
	}
	if _, err := transaction.ExecContext(ctx, repository.bind(`
DELETE FROM mcp_stream_event_cursors
WHERE updated_at < ?
  AND NOT EXISTS (
    SELECT 1 FROM mcp_stream_events e
    WHERE e.session_id=mcp_stream_event_cursors.session_id
      AND e.stream_id=mcp_stream_event_cursors.stream_id
  )`), cutoff); err != nil {
		return fmt.Errorf("delete expired stream cursors: %w", err)
	}
	return nil
}

func (repository *MCPStreamEventRepository) beginEventTransaction(ctx context.Context) (chainTransaction, error) {
	switch repository.dialect {
	case DialectPostgres:
		transaction, err := repository.db.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		if _, err := transaction.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", mcpStreamEventAdvisoryLock); err != nil {
			_ = transaction.Rollback()
			return nil, fmt.Errorf("lock MCP event store: %w", err)
		}
		return postgresChainTransaction{Tx: transaction}, nil
	case DialectSQLite:
		connection, err := repository.db.Conn(ctx)
		if err != nil {
			return nil, err
		}
		if _, err := connection.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
			_ = connection.Close()
			return nil, fmt.Errorf("set SQLite busy timeout: %w", err)
		}
		if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
			_ = connection.Close()
			return nil, err
		}
		return &sqliteChainTransaction{connection: connection}, nil
	default:
		return nil, fmt.Errorf("unsupported metadata dialect %q", repository.dialect)
	}
}

func validateMCPStreamInput(ctx context.Context, sessionID string) error {
	if ctx == nil {
		return ErrNilContext
	}
	if strings.TrimSpace(sessionID) == "" {
		return errors.New("session ID is required")
	}
	return nil
}

func validateMCPStreamLimits(ctx context.Context, sessionID string, maxBytes int, ttl time.Duration) error {
	if err := validateMCPStreamInput(ctx, sessionID); err != nil {
		return err
	}
	if maxBytes <= 0 || ttl <= 0 {
		return errors.New("positive max bytes and TTL are required")
	}
	return nil
}
