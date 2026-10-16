package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/google/uuid"
)

var (
	ErrTaskReadGrantRequestInvalid  = errors.New("invalid task read grant request")
	ErrTaskReadGrantRequestConflict = errors.New("task read grant request conflict")
	taskRequestKeyPattern           = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,128}$`)
)

const taskRequestColumns = `id, tenant_id, requester_user_id, agent_id, datasource_id,
  sql_sha256, sql_ciphertext, request_digest, idempotency_key, expires_at, status,
	  reviewer_user_id, reviewed_at, issued_at, grant_id, created_at,
	  (SELECT revoked_at FROM task_read_grants WHERE id = task_read_grant_requests.grant_id)`

// TaskReadGrantRequestRepository keeps approval and one-time issuance in the
// metadata database. The request stores encrypted SQL and no usable API key.
type TaskReadGrantRequestRepository struct {
	repositoryBase
	auditSeparate bool
	outbox        *ManagementAuditOutboxRepository
}

func taskRequestDigest(request model.TaskReadGrantRequest) string {
	input := strings.Join([]string{request.AgentID, request.DatasourceID,
		request.SQLSHA256, request.ExpiresAt.UTC().Format(time.RFC3339Nano)}, "\x00")
	digest := sha256.Sum256([]byte(input))
	return hex.EncodeToString(digest[:])
}

func (repository *TaskReadGrantRequestRepository) Create(ctx context.Context, request model.TaskReadGrantRequest) (model.TaskReadGrantRequest, error) {
	tenantID, err := repository.requireTenant(ctx, "create task read grant request")
	if err != nil {
		return model.TaskReadGrantRequest{}, err
	}
	now := time.Now().UTC()
	if request.TenantID != tenantID || strings.TrimSpace(request.ID) == "" ||
		strings.TrimSpace(request.RequesterUserID) == "" || strings.TrimSpace(request.AgentID) == "" ||
		strings.TrimSpace(request.DatasourceID) == "" || validateAPIKeyHash(request.SQLSHA256) != nil ||
		strings.TrimSpace(request.SQLCiphertext) == "" ||
		!taskRequestKeyPattern.MatchString(request.IdempotencyKey) || !request.ExpiresAt.After(now) ||
		request.ExpiresAt.After(now.Add(MaxTaskReadGrantDuration)) {
		return model.TaskReadGrantRequest{}, ErrTaskReadGrantRequestInvalid
	}
	request.RequestDigest = taskRequestDigest(request)
	request.Status = "pending"
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return model.TaskReadGrantRequest{}, fmt.Errorf("create task read grant request: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, repository.bind(`
INSERT INTO task_read_grant_requests
  (id, tenant_id, requester_user_id, agent_id, datasource_id, sql_sha256,
   sql_ciphertext, request_digest, idempotency_key, expires_at)
SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
WHERE EXISTS (SELECT 1 FROM users WHERE id = ? AND tenant_id = ? AND status = 'active')
  AND EXISTS (SELECT 1 FROM tenants WHERE id = ? AND status = 'active')
  AND EXISTS (SELECT 1 FROM agents WHERE id = ? AND tenant_id = ? AND status = 'active'
              AND (expires_at IS NULL OR expires_at > ?))
  AND EXISTS (SELECT 1 FROM datasources WHERE id = ? AND tenant_id = ?)
ON CONFLICT (tenant_id, requester_user_id, idempotency_key) DO NOTHING`),
		request.ID, tenantID, request.RequesterUserID, request.AgentID, request.DatasourceID,
		request.SQLSHA256, request.SQLCiphertext, request.RequestDigest, request.IdempotencyKey, request.ExpiresAt.UTC(),
		request.RequesterUserID, tenantID, tenantID, request.AgentID, tenantID, now,
		request.DatasourceID, tenantID)
	if err != nil {
		return model.TaskReadGrantRequest{}, fmt.Errorf("create task read grant request: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return model.TaskReadGrantRequest{}, fmt.Errorf("create task read grant request: %w", err)
	}
	if affected == 1 {
		if err := repository.writeAudit(ctx, tx, request, "task_read_grant_request", request.RequesterUserID); err != nil {
			return model.TaskReadGrantRequest{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return model.TaskReadGrantRequest{}, fmt.Errorf("commit task read grant request: %w", err)
	}
	stored, err := repository.byIdempotency(ctx, request.RequesterUserID, request.IdempotencyKey)
	if err != nil {
		return model.TaskReadGrantRequest{}, err
	}
	if stored.RequestDigest != request.RequestDigest {
		return model.TaskReadGrantRequest{}, ErrTaskReadGrantRequestConflict
	}
	return stored, nil
}

func (repository *TaskReadGrantRequestRepository) byIdempotency(ctx context.Context, requesterID, key string) (model.TaskReadGrantRequest, error) {
	tenantID, err := repository.requireTenant(ctx, "get task read grant request")
	if err != nil {
		return model.TaskReadGrantRequest{}, err
	}
	request, err := scanTaskReadGrantRequest(repository.db.QueryRowContext(ctx, repository.bind(`SELECT `+taskRequestColumns+`
FROM task_read_grant_requests WHERE tenant_id = ? AND requester_user_id = ? AND idempotency_key = ?`), tenantID, requesterID, key))
	if errors.Is(err, sql.ErrNoRows) {
		return model.TaskReadGrantRequest{}, ErrTaskReadGrantRequestInvalid
	}
	if err != nil {
		return model.TaskReadGrantRequest{}, fmt.Errorf("get task read grant request: %w", err)
	}
	return request, nil
}

func (repository *TaskReadGrantRequestRepository) Get(ctx context.Context, id string) (model.TaskReadGrantRequest, error) {
	tenantID, err := repository.requireTenant(ctx, "get task read grant request")
	if err != nil {
		return model.TaskReadGrantRequest{}, err
	}
	request, err := scanTaskReadGrantRequest(repository.db.QueryRowContext(ctx, repository.bind(`SELECT `+taskRequestColumns+`
FROM task_read_grant_requests WHERE id = ? AND tenant_id = ?`), id, tenantID))
	if errors.Is(err, sql.ErrNoRows) {
		return model.TaskReadGrantRequest{}, ErrNotFound
	}
	if err != nil {
		return model.TaskReadGrantRequest{}, fmt.Errorf("get task read grant request: %w", err)
	}
	return request, nil
}

func (repository *TaskReadGrantRequestRepository) List(ctx context.Context, requesterID string, reviewer bool) ([]model.TaskReadGrantRequest, error) {
	tenantID, err := repository.requireTenant(ctx, "list task read grant requests")
	if err != nil {
		return nil, err
	}
	query := `SELECT ` + taskRequestColumns + ` FROM task_read_grant_requests WHERE tenant_id = ?`
	args := []any{tenantID}
	if !reviewer {
		query += ` AND requester_user_id = ?`
		args = append(args, requesterID)
	}
	query += ` ORDER BY created_at DESC, id DESC`
	rows, err := repository.db.QueryContext(ctx, repository.bind(query), args...)
	if err != nil {
		return nil, fmt.Errorf("list task read grant requests: %w", err)
	}
	defer rows.Close()
	requests := make([]model.TaskReadGrantRequest, 0)
	for rows.Next() {
		request, scanErr := scanTaskReadGrantRequest(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan task read grant requests: %w", scanErr)
		}
		requests = append(requests, request)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate task read grant requests: %w", err)
	}
	return requests, nil
}

func (repository *TaskReadGrantRequestRepository) Review(ctx context.Context, id, reviewerID string, approve bool) (model.TaskReadGrantRequest, error) {
	tenantID, err := repository.requireTenant(ctx, "review task read grant request")
	if err != nil {
		return model.TaskReadGrantRequest{}, err
	}
	status, action := "rejected", "task_read_grant_reject"
	if approve {
		status, action = "approved", "task_read_grant_approve"
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return model.TaskReadGrantRequest{}, fmt.Errorf("review task read grant request: %w", err)
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, repository.bind(`
UPDATE task_read_grant_requests SET status = ?, reviewer_user_id = ?, reviewed_at = ?
WHERE id = ? AND tenant_id = ? AND requester_user_id <> ? AND status = 'pending'
  AND expires_at > ?
  AND EXISTS (SELECT 1 FROM users WHERE id = ? AND tenant_id = ? AND status = 'active')`),
		status, reviewerID, now, id, tenantID, reviewerID, now, reviewerID, tenantID)
	if err != nil {
		return model.TaskReadGrantRequest{}, fmt.Errorf("review task read grant request: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return model.TaskReadGrantRequest{}, err
	}
	if affected != 1 {
		return model.TaskReadGrantRequest{}, ErrTaskReadGrantRequestConflict
	}
	request, err := scanTaskReadGrantRequest(tx.QueryRowContext(ctx, repository.bind(`SELECT `+taskRequestColumns+`
FROM task_read_grant_requests WHERE id = ? AND tenant_id = ?`), id, tenantID))
	if err != nil {
		return model.TaskReadGrantRequest{}, err
	}
	if err := repository.writeAudit(ctx, tx, request, action, reviewerID); err != nil {
		return model.TaskReadGrantRequest{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.TaskReadGrantRequest{}, fmt.Errorf("commit task read grant review: %w", err)
	}
	return request, nil
}

// Issue atomically marks one approved request and inserts its only grant. A
// repeated or concurrent call cannot create another usable key.
func (repository *TaskReadGrantRequestRepository) Issue(ctx context.Context, id, requesterID, grantID, keyHash string) (model.TaskReadGrantRequest, error) {
	tenantID, err := repository.requireTenant(ctx, "issue task read grant")
	if err != nil {
		return model.TaskReadGrantRequest{}, err
	}
	if grantID == "" || validateAPIKeyHash(keyHash) != nil {
		return model.TaskReadGrantRequest{}, ErrTaskReadGrantRequestInvalid
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return model.TaskReadGrantRequest{}, fmt.Errorf("issue task read grant: %w", err)
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, repository.bind(`
UPDATE task_read_grant_requests SET status = 'issued', issued_at = ?, grant_id = ?
WHERE id = ? AND tenant_id = ? AND requester_user_id = ? AND status = 'approved'
  AND expires_at > ? AND reviewer_user_id IS NOT NULL AND reviewer_user_id <> requester_user_id
  AND EXISTS (SELECT 1 FROM users WHERE id = ? AND tenant_id = ? AND status = 'active')
  AND EXISTS (SELECT 1 FROM users WHERE id = reviewer_user_id AND tenant_id = ? AND status = 'active')`),
		now, grantID, id, tenantID, requesterID, now, requesterID, tenantID, tenantID)
	if err != nil {
		return model.TaskReadGrantRequest{}, fmt.Errorf("issue task read grant: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return model.TaskReadGrantRequest{}, err
	}
	if affected != 1 {
		return model.TaskReadGrantRequest{}, ErrTaskReadGrantRequestConflict
	}
	result, err = tx.ExecContext(ctx, repository.bind(`
INSERT INTO task_read_grants
  (id, tenant_id, issuer_user_id, agent_id, datasource_id, sql_sha256, key_hash, expires_at)
SELECT ?, r.tenant_id, r.requester_user_id, r.agent_id, r.datasource_id, r.sql_sha256, ?, r.expires_at
FROM task_read_grant_requests AS r
WHERE r.id = ? AND r.tenant_id = ? AND r.status = 'issued'
  AND EXISTS (SELECT 1 FROM agents WHERE id = r.agent_id AND tenant_id = r.tenant_id
              AND status = 'active' AND (expires_at IS NULL OR expires_at > ?))
  AND EXISTS (SELECT 1 FROM datasources WHERE id = r.datasource_id AND tenant_id = r.tenant_id)`),
		grantID, keyHash, id, tenantID, now)
	if err != nil {
		return model.TaskReadGrantRequest{}, fmt.Errorf("issue task read grant: %w", err)
	}
	affected, err = result.RowsAffected()
	if err != nil {
		return model.TaskReadGrantRequest{}, err
	}
	if affected != 1 {
		return model.TaskReadGrantRequest{}, ErrTaskReadGrantRequestConflict
	}
	request, err := scanTaskReadGrantRequest(tx.QueryRowContext(ctx, repository.bind(`SELECT `+taskRequestColumns+`
FROM task_read_grant_requests WHERE id = ? AND tenant_id = ?`), id, tenantID))
	if err != nil {
		return model.TaskReadGrantRequest{}, err
	}
	if err := repository.writeAudit(ctx, tx, request, "task_read_grant_issue", requesterID); err != nil {
		return model.TaskReadGrantRequest{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.TaskReadGrantRequest{}, fmt.Errorf("commit task read grant issuance: %w", err)
	}
	return request, nil
}

func (repository *TaskReadGrantRequestRepository) writeAudit(ctx context.Context, tx *sql.Tx, request model.TaskReadGrantRequest, action, actorID string) error {
	details, err := json.Marshal(struct {
		RequestID string  `json:"request_id"`
		GrantID   *string `json:"grant_id,omitempty"`
		SQLSHA256 string  `json:"sql_sha256"`
		Status    string  `json:"status"`
	}{request.ID, request.GrantID, request.SQLSHA256, request.Status})
	if err != nil {
		return err
	}
	eventID := uuid.NewString()
	if repository.auditSeparate {
		return repository.outbox.Append(ctx, tx, model.ManagementAuditOutbox{
			EventUUID: eventID, Action: action, ActorType: "user", ActorID: actorID,
			DetailsJSON: string(details),
		})
	}
	actorType, encoded := "user", string(details)
	_, err = insertAuditLog(ctx, tx, repository.dialect, model.AuditLog{
		TenantID: request.TenantID, AgentID: &request.AgentID, DatasourceID: &request.DatasourceID,
		Decision: string(model.DecisionAllow), Action: &action, ActorType: &actorType,
		ActorID: &actorID, DetailsJSON: &encoded, EventUUID: &eventID,
	})
	return err
}

func scanTaskReadGrantRequest(scanner rowScanner) (model.TaskReadGrantRequest, error) {
	var request model.TaskReadGrantRequest
	var reviewer, grantID sql.NullString
	var expiresAt, reviewedAt, issuedAt, createdAt, grantRevokedAt databaseTimestamp
	if err := scanner.Scan(&request.ID, &request.TenantID, &request.RequesterUserID,
		&request.AgentID, &request.DatasourceID, &request.SQLSHA256, &request.SQLCiphertext,
		&request.RequestDigest, &request.IdempotencyKey, &expiresAt, &request.Status,
		&reviewer, &reviewedAt, &issuedAt, &grantID, &createdAt, &grantRevokedAt); err != nil {
		return model.TaskReadGrantRequest{}, err
	}
	var err error
	request.ExpiresAt, err = expiresAt.required("task_read_grant_requests.expires_at")
	if err != nil {
		return model.TaskReadGrantRequest{}, err
	}
	request.CreatedAt, err = createdAt.required("task_read_grant_requests.created_at")
	if err != nil {
		return model.TaskReadGrantRequest{}, err
	}
	request.ReviewerUserID = stringPointer(reviewer)
	request.GrantID = stringPointer(grantID)
	request.GrantRevokedAt = grantRevokedAt.pointer()
	request.ReviewedAt = reviewedAt.pointer()
	request.IssuedAt = issuedAt.pointer()
	return request, nil
}
