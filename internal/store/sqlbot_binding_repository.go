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
	ParentBindingID    string    `json:"-"`
	ParentRevision     int64     `json:"-"`
	DatasourceDBType   string    `json:"-"` // Resolved for the verified request; never stored or accepted from JSON.
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

const sqlbotBindingColumns = `id,tenant_id,workspace_id,datasource_name,sqlbot_user_id,agentsql_user_id,agent_id,datasource_id,schema_name,numeric_columns,agent_key_env,connector_token_env,connector_token_hash,parent_binding_id,parent_binding_revision,enabled,revision,created_at,updated_at`

// The parent must remain the same enabled root mapping that authorized this
// child. A rotate, disable, update, or delete of that mapping revokes every
// derived connector without relying on a separate cleanup job.
const sqlbotParentActive = `((b.parent_binding_id='' AND b.parent_binding_revision=0) OR EXISTS (
 SELECT 1 FROM sqlbot_bindings parent WHERE parent.id=b.parent_binding_id AND parent.tenant_id=b.tenant_id
 AND parent.parent_binding_id='' AND parent.parent_binding_revision=0 AND parent.enabled=TRUE AND parent.revision=b.parent_binding_revision
 AND parent.workspace_id=b.workspace_id AND parent.sqlbot_user_id=b.sqlbot_user_id
 AND parent.agentsql_user_id=b.agentsql_user_id AND parent.agent_id=b.agent_id
 AND parent.datasource_id=b.datasource_id AND parent.schema_name=b.schema_name))`

func scanSQLBotBinding(scanner rowScanner) (SQLBotBinding, error) {
	var b SQLBotBinding
	var columns string
	err := scanner.Scan(&b.ID, &b.TenantID, &b.WorkspaceID, &b.DatasourceName, &b.SQLBotUserID, &b.AgentSQLUserID, &b.AgentID, &b.DatasourceID, &b.SchemaName, &columns, &b.AgentKeyEnv, &b.ConnectorTokenEnv, &b.ConnectorTokenHash, &b.ParentBindingID, &b.ParentRevision, &b.Enabled, &b.Revision, &b.CreatedAt, &b.UpdatedAt)
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
	if !sqlbotUserID.MatchString(b.WorkspaceID) || len(b.DatasourceName) == 0 || len(b.DatasourceName) > 200 || strings.TrimSpace(b.DatasourceName) != b.DatasourceName || strings.ContainsAny(b.DatasourceName, "\r\n\x00") || !sqlbotUserID.MatchString(b.SQLBotUserID) || b.AgentSQLUserID == "" || b.AgentID == "" || b.DatasourceID == "" || !sqlbotIdentifier.MatchString(b.SchemaName) || !sqlbotEnvironment.MatchString(b.AgentKeyEnv) || !sqlbotEnvironment.MatchString(b.ConnectorTokenEnv) || b.AgentKeyEnv == b.ConnectorTokenEnv || len(b.NumericColumns) > 64 || (b.ParentBindingID == "") != (b.ParentRevision == 0) || b.ParentRevision < 0 {
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
	var kind, databaseName string
	if err := tx.QueryRowContext(ctx, r.bind(`SELECT db_type,database FROM datasources WHERE id=? AND tenant_id=?`), b.DatasourceID, b.TenantID).Scan(&kind, &databaseName); err != nil {
		return err
	}
	if kind != "postgres" && (kind != "mysql" || b.SchemaName != databaseName) {
		return ErrSQLBotBindingInvalid
	}
	if b.ParentBindingID != "" {
		var count int
		err := tx.QueryRowContext(ctx, r.bind(`SELECT COUNT(*) FROM sqlbot_bindings p WHERE p.id=? AND p.tenant_id=? AND p.parent_binding_id='' AND p.parent_binding_revision=0 AND p.enabled=TRUE AND p.revision=? AND p.workspace_id=? AND p.sqlbot_user_id=? AND p.agentsql_user_id=? AND p.agent_id=? AND p.datasource_id=? AND p.schema_name=?`),
			b.ParentBindingID, b.TenantID, b.ParentRevision, b.WorkspaceID, b.SQLBotUserID, b.AgentSQLUserID, b.AgentID, b.DatasourceID, b.SchemaName).Scan(&count)
		if err != nil {
			return err
		}
		if count != 1 {
			return ErrSQLBotBindingDenied
		}
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
	return r.create(ctx, b, actorID, "")
}

// CreateDerived accepts only a gateway-derived 256-bit child credential. It
// allows the same authenticated file request to recover its connector after a
// lost response without persisting token plaintext in the control database.
func (r *SQLBotBindingRepository) CreateDerived(ctx context.Context, b SQLBotBinding, actorID, plainToken string) (SQLBotBinding, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(plainToken)
	if b.ParentBindingID == "" || len(plainToken) != 43 || err != nil || len(decoded) != 32 || base64.RawURLEncoding.EncodeToString(decoded) != plainToken {
		return SQLBotBinding{}, ErrSQLBotBindingInvalid
	}
	created, _, err := r.create(ctx, b, actorID, plainToken)
	return created, err
}

func (r *SQLBotBindingRepository) create(ctx context.Context, b SQLBotBinding, actorID, derivedToken string) (SQLBotBinding, string, error) {
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
	plain, hash := derivedToken, ""
	if plain == "" {
		plain, hash, err = sqlbotCredential()
		if err != nil {
			return SQLBotBinding{}, "", err
		}
	} else {
		digest := sha256.Sum256([]byte(plain))
		hash = hex.EncodeToString(digest[:])
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
	_, err = tx.ExecContext(ctx, r.bind(`INSERT INTO sqlbot_bindings(id,tenant_id,workspace_id,datasource_name,sqlbot_user_id,agentsql_user_id,agent_id,datasource_id,schema_name,numeric_columns,agent_key_env,connector_token_env,connector_token_hash,parent_binding_id,parent_binding_revision,enabled,revision) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1)`), b.ID, b.TenantID, b.WorkspaceID, b.DatasourceName, b.SQLBotUserID, b.AgentSQLUserID, b.AgentID, b.DatasourceID, b.SchemaName, string(columns), b.AgentKeyEnv, b.ConnectorTokenEnv, b.ConnectorTokenHash, b.ParentBindingID, b.ParentRevision, b.Enabled)
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
	// Parentage is immutable and hidden from management JSON. Read the stored
	// fields back before validation so an omitted JSON field cannot bypass a
	// revoked parent when updating a child.
	var parentID string
	var parentRevision int64
	if err = tx.QueryRowContext(ctx, r.bind(`SELECT parent_binding_id,parent_binding_revision FROM sqlbot_bindings WHERE id=? AND tenant_id=?`), b.ID, b.TenantID).Scan(&parentID, &parentRevision); err != nil {
		return SQLBotBinding{}, err
	}
	if b.ParentBindingID != "" && (b.ParentBindingID != parentID || b.ParentRevision != parentRevision) {
		return SQLBotBinding{}, ErrSQLBotBindingInvalid
	}
	b.ParentBindingID, b.ParentRevision = parentID, parentRevision
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
	b, err := scanSQLBotBinding(r.db.QueryRowContext(ctx, r.bind(`SELECT `+sqlbotBindingColumns+` FROM sqlbot_bindings b WHERE b.connector_token_hash=? AND b.enabled=TRUE AND `+sqlbotParentActive), hex.EncodeToString(digest[:])))
	if err != nil || !b.Enabled {
		return SQLBotBinding{}, ErrSQLBotBindingDenied
	}
	return b, nil
}

// RecordImportReplay audits the release of an already issued child token. A
// revoked parent or changed child cannot pass the read check in this audit
// transaction, and an audit failure prevents the token response.
func (r *SQLBotBindingRepository) RecordImportReplay(ctx context.Context, child SQLBotBinding, actor string) error {
	tenant, err := r.requireTenant(ctx, "replay SQLBot import credential")
	if err != nil || tenant != child.TenantID || child.ParentBindingID == "" ||
		actor == "" || actor != child.AgentSQLUserID || child.Revision < 1 {
		return ErrSQLBotBindingDenied
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	err = tx.QueryRowContext(ctx, r.bind(`SELECT COUNT(*) FROM sqlbot_bindings b WHERE b.id=? AND b.tenant_id=?
		AND b.enabled=TRUE AND b.revision=? AND b.agentsql_user_id=? AND b.agent_id=?
		AND b.datasource_id=? AND b.workspace_id=? AND b.parent_binding_id=?
		AND b.parent_binding_revision=? AND `+sqlbotParentActive),
		child.ID, tenant, child.Revision, actor, child.AgentID, child.DatasourceID,
		child.WorkspaceID, child.ParentBindingID, child.ParentRevision).Scan(&count)
	if err != nil || count != 1 {
		return ErrSQLBotBindingDenied
	}
	details, _ := json.Marshal(map[string]any{"binding_id": child.ID, "parent_binding_id": child.ParentBindingID})
	if err := r.outbox.Append(ctx, tx, model.ManagementAuditOutbox{EventUUID: uuid.NewString(),
		Action: "managed_file.replay", ActorType: "user", ActorID: actor, DetailsJSON: string(details)}); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *SQLBotBindingRepository) ValidateRuntime(ctx context.Context, id string, revision int64, agentID, userID string) (SQLBotBinding, error) {
	tenant, err := r.requireTenant(ctx, "validate SQLBot binding")
	if err != nil {
		return SQLBotBinding{}, ErrSQLBotBindingDenied
	}
	b, err := scanSQLBotBinding(r.db.QueryRowContext(ctx, r.bind(`SELECT `+sqlbotBindingColumns+` FROM sqlbot_bindings b WHERE b.id=? AND b.tenant_id=? AND b.enabled=TRUE AND b.revision=? AND b.agent_id=? AND b.agentsql_user_id=? AND `+sqlbotParentActive), id, tenant, revision, agentID, userID))
	if err != nil {
		return SQLBotBinding{}, ErrSQLBotBindingDenied
	}
	return b, nil
}
