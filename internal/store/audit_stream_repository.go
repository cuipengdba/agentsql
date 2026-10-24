package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/cuipengdba/agentsql/internal/model"
)

// AuditStreamItem binds a durable per-tenant commit-order sequence to its
// immutable audit record. The sequence is for SSE resume, not an audit ID.
type AuditStreamItem struct {
	Sequence int64
	Audit    model.AuditLog
}

// LatestStreamSequence reads the current committed cursor for the authenticated
// tenant. A tenant without audits starts at zero.
func (repository *AuditLogRepository) LatestStreamSequence(ctx context.Context) (int64, error) {
	if repository == nil || repository.db == nil {
		return 0, fmt.Errorf("latest audit stream sequence: repository is not initialized")
	}
	tenantID, err := repository.requireTenant(ctx, "latest audit stream sequence")
	if err != nil {
		return 0, err
	}
	var sequence int64
	err = repository.db.QueryRowContext(ctx, repository.bind(
		"SELECT next_seq FROM audit_stream_state WHERE tenant_id = ?"), tenantID).Scan(&sequence)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("latest audit stream sequence: %w", err)
	}
	if sequence < 0 {
		return 0, fmt.Errorf("latest audit stream sequence: negative cursor")
	}
	return sequence, nil
}

// StreamAfter reads one bounded page strictly after the tenant's last delivered
// sequence. Trigger-assigned sequences commit in order for each tenant.
func (repository *AuditLogRepository) StreamAfter(ctx context.Context, after int64, limit int) ([]AuditStreamItem, error) {
	if repository == nil || repository.db == nil {
		return nil, fmt.Errorf("read audit stream: repository is not initialized")
	}
	if after < 0 || limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("read audit stream: invalid cursor or limit")
	}
	tenantID, err := repository.requireTenant(ctx, "read audit stream")
	if err != nil {
		return nil, err
	}
	query := `
SELECT e.seq, a.id, a.tenant_id, a.ts, a.agent_id, a.datasource_id, a.session_id,
       a.conversation_id, a.mcp_tool, a.db_type, a.sql_raw, a.sql_norm,
       a.stmt_type, a.objects, a.decision, a.rule_hits, a.risk_level,
       a.est_rows, a.rows_returned, a.latency_ms, a.client_ip, a.model_name,
       a.error_msg, a.error_code, a.action, a.actor_type, a.actor_id,
       a.details_json, a.event_uuid
FROM audit_stream_events e JOIN audit_logs a
  ON a.id = e.audit_id AND a.tenant_id = e.tenant_id
WHERE e.tenant_id = ? AND e.seq > ?
ORDER BY e.seq ASC
LIMIT ?`
	rows, err := repository.db.QueryContext(ctx, repository.bind(query), tenantID, after, limit)
	if err != nil {
		return nil, fmt.Errorf("read audit stream: %w", err)
	}
	items := make([]AuditStreamItem, 0, limit)
	expected := after + 1
	for rows.Next() {
		var sequence int64
		audit, scanErr := scanAuditLog(auditStreamScanner{rows: rows, sequence: &sequence})
		if scanErr != nil {
			return nil, fmt.Errorf("scan audit stream: %w", closeRowsAfterError(rows, scanErr))
		}
		if audit.TenantID != tenantID || sequence != expected {
			return nil, fmt.Errorf("read audit stream: tenant or sequence gap: %w", closeRowsAfterError(rows, errors.New("audit stream is inconsistent")))
		}
		items = append(items, AuditStreamItem{Sequence: sequence, Audit: audit})
		expected++
	}
	iterationErr := rows.Err()
	closeErr := rows.Close()
	if iterationErr != nil || closeErr != nil {
		return nil, fmt.Errorf("finish audit stream: %w", errors.Join(iterationErr, closeErr))
	}
	return items, nil
}

type auditStreamScanner struct {
	rows     *sql.Rows
	sequence *int64
}

func (scanner auditStreamScanner) Scan(destinations ...any) error {
	all := make([]any, 0, len(destinations)+1)
	all = append(all, scanner.sequence)
	all = append(all, destinations...)
	return scanner.rows.Scan(all...)
}
