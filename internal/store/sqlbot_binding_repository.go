package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/google/uuid"
)

var (
	ErrSQLBotBindingInvalid = errors.New("invalid SQLBot binding")
	ErrSQLBotBindingStale   = errors.New("SQLBot binding revision changed")
	ErrSQLBotBindingDenied  = errors.New("SQLBot binding unavailable")
	sqlbotIdentifier        = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)
	sqlbotEnvironment       = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,95}$`)
	sqlbotUserID            = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)
)

// SQLBotBinding grants no database privileges. It restricts a connector to one
// existing Agent, human, workspace and datasource. Credentials are stored only
// as hashes and never serialized by the management API.
type SQLBotBinding struct {
	ID                 string    `json:"id"`
	TenantID           string    `json:"tenant_id"`
	WorkspaceID        string    `json:"workspace_id"`
	DatasourceName     string    `json:"datasource_name"`
	SQLBotUserID       string    `json:"sqlbot_user_id"`
	AgentSQLUserID     string    `json:"agentsql_user_id"`
	AgentID            string    `json:"agent_id"`
	DatasourceID       string    `json:"datasource_id"`
	SchemaName         string    `json:"schema_name"`
	NumericColumns     []string  `json:"numeric_columns"`
	AgentKeyEnv        string    `json:"agent_key_env"`
	ConnectorTokenEnv  string    `json:"connector_token_env"`
	ConnectorTokenHash string    `json:"-"`
	Enabled            bool      `json:"enabled"`
	Revision           int64     `json:"revision"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type SQLBotBindingRepository struct {
	repositoryBase
	outbox *ManagementAuditOutboxRepository
}

func (store *Store) SQLBotBindings() *SQLBotBindingRepository {
	return &SQLBotBindingRepository{repositoryBase: repositoryBase{db: store.metaDB, dialect: store.metaDriver}, outbox: store.Outbox()}
}

const sqlbotBindingColumns = `id,tenant_id,workspace_id,datasource_name,sqlbot_user_id,agentsql_user_id,agent_id,datasource_id,schema_name,numeric_columns,agent_key_env,connector_token_env,connector_token_hash,enabled,revision,created_at,updated_at`

func scanSQLBotBinding(scanner rowScanner) (SQLBotBinding, error) {
	var b SQLBotBinding
	var columns string
	err := scanner.Scan(&b.ID, &b.TenantID, &b.WorkspaceID, &b.DatasourceName, &b.SQLBotUserID, &b.AgentSQLUserID, &b.AgentID, &b.DatasourceID, &b.SchemaName, &columns, &b.AgentKeyEnv, &b.ConnectorTokenEnv, &b.ConnectorTokenHash, &b.Enabled, &b.Revision, &b.CreatedAt, &b.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return b, ErrNotFound
	}
	if err != nil {
		return b, err
	}
	if err = json.Unmarshal([]byte(columns), &b.NumericColumns); err != nil {
		return SQLBotBinding{}, ErrSQLBotBindingInvalid
	}
	return b, nil
}

func (r *SQLBotBindingRepository) Get(ctx context.Context, id string) (SQLBotBinding, error) {
	tenant, err := r.requireTenant(ctx, "get SQLBot binding")
	if err != nil {
		return SQLBotBinding{}, err
	}
	return scanSQLBotBinding(r.db.QueryRowContext(ctx, r.bind(`SELECT `+sqlbotBindingColumns+` FROM sqlbot_bindings WHERE id=? AND tenant_id=?`), id, tenant))
}

func (r *SQLBotBindingRepository) List(ctx context.Context) ([]SQLBotBinding, error) {
	tenant, err := r.requireTenant(ctx, "list SQLBot bindings")
	if err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, r.bind(`SELECT `+sqlbotBindingColumns+` FROM sqlbot_bindings WHERE tenant_id=? ORDER BY created_at,id`), tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []SQLBotBinding{}
	for rows.Next() {
		b, err := scanSQLBotBinding(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, b)
	}
	return result, rows.Err()
}

func normalizeSQLBotBinding(b *SQLBotBinding) error {
	if b.AgentKeyEnv == "" {
		b.AgentKeyEnv = "AGENTSQL_SQLBOT_AGENT_KEY"
	}
	if b.ConnectorTokenEnv == "" {
		b.ConnectorTokenEnv = "AGENTSQL_SQLBOT_CONNECTOR_TOKEN"
	}
	if b.NumericColumns == nil {
		b.NumericColumns = []string{}
	}
	if !sqlbotUserID.MatchString(b.WorkspaceID) || len(b.DatasourceName) == 0 || len(b.DatasourceName) > 200 || strings.TrimSpace(b.DatasourceName) != b.DatasourceName || strings.ContainsAny(b.DatasourceName, "\r\n\x00") || !sqlbotUserID.MatchString(b.SQLBotUserID) || b.AgentSQLUserID == "" || b.AgentID == "" || b.DatasourceID == "" || !sqlbotIdentifier.MatchString(b.SchemaName) || !sqlbotEnvironment.MatchString(b.AgentKeyEnv) || !sqlbotEnvironment.MatchString(b.ConnectorTokenEnv) || b.AgentKeyEnv == b.ConnectorTokenEnv || len(b.NumericColumns) > 64 {
		return ErrSQLBotBindingInvalid
	}
	seen := map[string]bool{}
	for _, c := range b.NumericColumns {
		if !sqlbotIdentifier.MatchString(c) || seen[c] {
			return ErrSQLBotBindingInvalid
		}
		seen[c] = true
	}
	return nil
}

// References are checked within the mutation transaction so the binding never
// imports a datasource, Agent or human from another tenant.
func (r *SQLBotBindingRepository) validateReferences(ctx context.Context, tx *sql.Tx, b SQLBotBinding) error {
	for _, v := range [][2]string{{"agents", b.AgentID}, {"datasources", b.DatasourceID}, {"users", b.AgentSQLUserID}} {
		var count int
		if err := tx.QueryRowContext(ctx, r.bind(`SELECT COUNT(*) FROM `+v[0]+` WHERE id=? AND tenant_id=?`), v[1], b.TenantID).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return ErrSQLBotBindingInvalid
		}
	}
	var kind string
	if err := tx.QueryRowContext(ctx, r.bind(`SELECT db_type FROM datasources WHERE id=? AND tenant_id=?`), b.DatasourceID, b.TenantID).Scan(&kind); err != nil {
		return err
	}
	if kind != "postgres" {
		return ErrSQLBotBindingInvalid
	}
	return nil
}

func sqlbotCredential() (string, string, error) {
	var bytes [32]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", "", err
	}
	plain := base64.RawURLEncoding.EncodeToString(bytes[:])
	digest := sha256.Sum256([]byte(plain))
	return plain, hex.EncodeToString(digest[:]), nil
}

func (r *SQLBotBindingRepository) audit(ctx context.Context, tx *sql.Tx, b SQLBotBinding, actor, action string) error {
	if strings.TrimSpace(actor) == "" {
		return ErrSQLBotBindingInvalid
	}
	details, _ := json.Marshal(map[string]any{"binding_id": b.ID, "revision": b.Revision, "enabled": b.Enabled, "agent_id": b.AgentID, "datasource_id": b.DatasourceID, "user_id": b.AgentSQLUserID})
	return r.outbox.Append(ctx, tx, model.ManagementAuditOutbox{EventUUID: uuid.NewString(), Action: action, ActorType: "admin", ActorID: actor, DetailsJSON: string(details)})
}

func (r *SQLBotBindingRepository) Create(ctx context.Context, b SQLBotBinding, actorID string) (SQLBotBinding, string, error) {
	tenant, err := r.requireTenant(ctx, "create SQLBot binding")
	if err != nil {
		return SQLBotBinding{}, "", err
	}
	if b.TenantID != "" && b.TenantID != tenant {
		return SQLBotBinding{}, "", ErrSQLBotBindingInvalid
	}
	b.TenantID = tenant
	if err = normalizeSQLBotBinding(&b); err != nil {
		return SQLBotBinding{}, "", err
	}
	b.ID = uuid.NewString()
	b.Revision = 1
	plain, hash, err := sqlbotCredential()
	if err != nil {
		return SQLBotBinding{}, "", err
	}
	b.ConnectorTokenHash = hash
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return SQLBotBinding{}, "", err
	}
	defer tx.Rollback()
	if err = r.validateReferences(ctx, tx, b); err != nil {
		return SQLBotBinding{}, "", err
	}
	columns, _ := json.Marshal(b.NumericColumns)
	_, err = tx.ExecContext(ctx, r.bind(`INSERT INTO sqlbot_bindings(id,tenant_id,workspace_id,datasource_name,sqlbot_user_id,agentsql_user_id,agent_id,datasource_id,schema_name,numeric_columns,agent_key_env,connector_token_env,connector_token_hash,enabled,revision) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,1)`), b.ID, b.TenantID, b.WorkspaceID, b.DatasourceName, b.SQLBotUserID, b.AgentSQLUserID, b.AgentID, b.DatasourceID, b.SchemaName, string(columns), b.AgentKeyEnv, b.ConnectorTokenEnv, b.ConnectorTokenHash, b.Enabled)
	if err != nil {
		return SQLBotBinding{}, "", ErrSQLBotBindingInvalid
	}
	if err = r.audit(ctx, tx, b, actorID, "sqlbot_binding.create"); err != nil {
		return SQLBotBinding{}, "", err
	}
	if err = tx.Commit(); err != nil {
		return SQLBotBinding{}, "", err
	}
	b, err = r.Get(ctx, b.ID)
	return b, plain, err
}

func (r *SQLBotBindingRepository) Update(ctx context.Context, b SQLBotBinding, actorID string) (SQLBotBinding, error) {
	tenant, err := r.requireTenant(ctx, "update SQLBot binding")
	if err != nil {
		return SQLBotBinding{}, err
	}
	if b.TenantID != "" && b.TenantID != tenant {
		return SQLBotBinding{}, ErrSQLBotBindingInvalid
	}
	b.TenantID = tenant
	if err = normalizeSQLBotBinding(&b); err != nil {
		return SQLBotBinding{}, err
	}
	if b.Revision < 1 {
		return SQLBotBinding{}, ErrSQLBotBindingStale
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return SQLBotBinding{}, err
	}
	defer tx.Rollback()
	columns, _ := json.Marshal(b.NumericColumns)
	result, err := tx.ExecContext(ctx, r.bind(`UPDATE sqlbot_bindings SET workspace_id=?,datasource_name=?,sqlbot_user_id=?,agentsql_user_id=?,agent_id=?,datasource_id=?,schema_name=?,numeric_columns=?,agent_key_env=?,connector_token_env=?,enabled=?,revision=revision+1,updated_at=CURRENT_TIMESTAMP WHERE id=? AND tenant_id=? AND revision=?`), b.WorkspaceID, b.DatasourceName, b.SQLBotUserID, b.AgentSQLUserID, b.AgentID, b.DatasourceID, b.SchemaName, string(columns), b.AgentKeyEnv, b.ConnectorTokenEnv, b.Enabled, b.ID, b.TenantID, b.Revision)
	if err != nil {
		return SQLBotBinding{}, ErrSQLBotBindingInvalid
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return SQLBotBinding{}, ErrSQLBotBindingStale
	}
	// Reserve SQLite's writer before reading references. A concurrent audit
	// append can otherwise invalidate a deferred read transaction and make its
	// later UPDATE fail with SQLITE_BUSY_SNAPSHOT. Invalid references still roll
	// the UPDATE back because validation runs before commit.
	if err = r.validateReferences(ctx, tx, b); err != nil {
		return SQLBotBinding{}, err
	}
	b.Revision++
	if err = r.audit(ctx, tx, b, actorID, "sqlbot_binding.update"); err != nil {
		return SQLBotBinding{}, err
	}
	if err = tx.Commit(); err != nil {
		return SQLBotBinding{}, err
	}
	return r.Get(ctx, b.ID)
}

func (r *SQLBotBindingRepository) Rotate(ctx context.Context, id string, revision int64, actorID string) (SQLBotBinding, string, error) {
	b, err := r.Get(ctx, id)
	if err != nil {
		return SQLBotBinding{}, "", err
	}
	if b.Revision != revision {
		return SQLBotBinding{}, "", ErrSQLBotBindingStale
	}
	plain, hash, err := sqlbotCredential()
	if err != nil {
		return SQLBotBinding{}, "", err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return SQLBotBinding{}, "", err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, r.bind(`UPDATE sqlbot_bindings SET connector_token_hash=?,revision=revision+1,updated_at=CURRENT_TIMESTAMP WHERE id=? AND tenant_id=? AND revision=?`), hash, id, b.TenantID, revision)
	if err != nil {
		return SQLBotBinding{}, "", err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return SQLBotBinding{}, "", ErrSQLBotBindingStale
	}
	b.Revision++
	if err = r.audit(ctx, tx, b, actorID, "sqlbot_binding.rotate"); err != nil {
		return SQLBotBinding{}, "", err
	}
	if err = tx.Commit(); err != nil {
		return SQLBotBinding{}, "", err
	}
	b, err = r.Get(ctx, id)
	return b, plain, err
}

func (r *SQLBotBindingRepository) Delete(ctx context.Context, id string, revision int64, actorID string) error {
	b, err := r.Get(ctx, id)
	if err != nil {
		return err
	}
	if b.Revision != revision {
		return ErrSQLBotBindingStale
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, r.bind(`DELETE FROM sqlbot_bindings WHERE id=? AND tenant_id=? AND revision=?`), id, b.TenantID, revision)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return ErrSQLBotBindingStale
	}
	if err = r.audit(ctx, tx, b, actorID, "sqlbot_binding.delete"); err != nil {
		return err
	}
	return tx.Commit()
}

// ResolveConnector is a credential lookup, not a general cross-tenant lookup.
// Callers must still check the trusted SQLBot identity and live Agent/RBAC state.
func (r *SQLBotBindingRepository) ResolveConnector(ctx context.Context, plainToken string) (SQLBotBinding, error) {
	if ctx == nil || len(plainToken) != 43 {
		return SQLBotBinding{}, ErrSQLBotBindingDenied
	}
	decoded, err := base64.RawURLEncoding.DecodeString(plainToken)
	if err != nil || len(decoded) != 32 {
		return SQLBotBinding{}, ErrSQLBotBindingDenied
	}
	digest := sha256.Sum256([]byte(plainToken))
	b, err := scanSQLBotBinding(r.db.QueryRowContext(ctx, r.bind(`SELECT `+sqlbotBindingColumns+` FROM sqlbot_bindings WHERE connector_token_hash=?`), hex.EncodeToString(digest[:])))
	if err != nil || !b.Enabled {
		return SQLBotBinding{}, ErrSQLBotBindingDenied
	}
	return b, nil
}

func (r *SQLBotBindingRepository) ValidateRuntime(ctx context.Context, id string, revision int64, agentID, userID string) (SQLBotBinding, error) {
	b, err := r.Get(ctx, id)
	if err != nil || !b.Enabled || b.Revision != revision || b.AgentID != agentID || b.AgentSQLUserID != userID {
		return SQLBotBinding{}, ErrSQLBotBindingDenied
	}
	return b, nil
}
