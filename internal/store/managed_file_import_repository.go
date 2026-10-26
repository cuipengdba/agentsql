package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/managedfile"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/google/uuid"
)

var ErrManagedFileState = errors.New("managed file operation unavailable or revision changed")

type ManagedFileImport struct {
	ID              string
	TenantID        string
	BindingID       string
	BindingRevision int64
	WorkspaceID     string
	OwnerUserID     string
	AgentID         string
	DatasourceID    string
	FileSHA256      string
	FileBytes       int64
	SheetCount      int
	Status          string
	Revision        int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type ManagedFileTableRecord struct {
	Name string
	Rows int
}

type ManagedFileImportRepository struct {
	repositoryBase
	outbox *ManagementAuditOutboxRepository
}

func (store *Store) ManagedFileImports() *ManagedFileImportRepository {
	return &ManagedFileImportRepository{repositoryBase: repositoryBase{db: store.metaDB, dialect: store.metaDriver}, outbox: store.Outbox()}
}

const managedFileImportColumns = `id,tenant_id,binding_id,binding_revision,workspace_id,owner_user_id,agent_id,datasource_id,file_sha256,file_bytes,sheet_count,status,revision,created_at,updated_at`

func scanManagedFileImport(row rowScanner) (ManagedFileImport, error) {
	var item ManagedFileImport
	err := row.Scan(&item.ID, &item.TenantID, &item.BindingID, &item.BindingRevision, &item.WorkspaceID,
		&item.OwnerUserID, &item.AgentID, &item.DatasourceID, &item.FileSHA256, &item.FileBytes,
		&item.SheetCount, &item.Status, &item.Revision, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ManagedFileImport{}, ErrNotFound
	}
	return item, err
}

func (r *ManagedFileImportRepository) Get(ctx context.Context, id string) (ManagedFileImport, error) {
	tenant, err := r.requireTenant(ctx, "get managed file import")
	if err != nil {
		return ManagedFileImport{}, err
	}
	return scanManagedFileImport(r.db.QueryRowContext(ctx, r.bind(`SELECT `+managedFileImportColumns+` FROM managed_file_imports WHERE id=? AND tenant_id=?`), id, tenant))
}

// Begin writes a recoverable operation ID and audit before any physical table
// is created. The SQLBot binding is checked in the same transaction; a stale
// or disabled connector cannot reserve a managed file operation.
func (r *ManagedFileImportRepository) Begin(ctx context.Context, binding SQLBotBinding, fileSHA256 string, fileBytes int64, sheetCount int, actor string) (ManagedFileImport, error) {
	tenant, err := r.requireTenant(ctx, "begin managed file import")
	if err != nil {
		return ManagedFileImport{}, err
	}
	digest, err := hex.DecodeString(fileSHA256)
	if binding.TenantID != tenant || !binding.Enabled || binding.ID == "" || binding.Revision < 1 ||
		len(fileSHA256) != 64 || err != nil || len(digest) != 32 || strings.ToLower(fileSHA256) != fileSHA256 ||
		fileBytes < 1 || fileBytes > 4<<20 || sheetCount < 1 || sheetCount > 8 || strings.TrimSpace(actor) == "" || actor != binding.AgentSQLUserID {
		return ManagedFileImport{}, ErrManagedFileState
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return ManagedFileImport{}, err
	}
	id := hex.EncodeToString(raw[:])
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return ManagedFileImport{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, r.bind(`INSERT INTO managed_file_imports
  (id,tenant_id,binding_id,binding_revision,workspace_id,owner_user_id,agent_id,datasource_id,file_sha256,file_bytes,sheet_count,status,revision)
  SELECT ?,tenant_id,id,revision,workspace_id,agentsql_user_id,agent_id,datasource_id,?,?,?,'preparing',1
  FROM sqlbot_bindings WHERE id=? AND tenant_id=? AND enabled=? AND revision=? AND workspace_id=?
    AND agentsql_user_id=? AND agent_id=? AND datasource_id=?`), id, fileSHA256, fileBytes, sheetCount,
		binding.ID, tenant, true, binding.Revision, binding.WorkspaceID, binding.AgentSQLUserID, binding.AgentID, binding.DatasourceID)
	if err != nil {
		return ManagedFileImport{}, err
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return ManagedFileImport{}, ErrManagedFileState
	}
	if err := r.audit(ctx, tx, id, "preparing", actor, fileSHA256, fileBytes, sheetCount); err != nil {
		return ManagedFileImport{}, err
	}
	if err := tx.Commit(); err != nil {
		return ManagedFileImport{}, err
	}
	return r.Get(ctx, id)
}

// MarkStaged atomically records every service-derived physical table name,
// advances the journal, and writes management audit. A missing or altered
// sheet cannot later receive policy_ready status.
func (r *ManagedFileImportRepository) MarkStaged(ctx context.Context, id string, revision int64, tables []ManagedFileTableRecord, actor string) (ManagedFileImport, error) {
	tenant, err := r.requireTenant(ctx, "mark managed file staged")
	if err != nil {
		return ManagedFileImport{}, err
	}
	operation, err := managedfile.ParseOperationID(id)
	if err != nil || revision < 1 || actor == "" || len(tables) == 0 || len(tables) > 8 {
		return ManagedFileImport{}, ErrManagedFileState
	}
	for index, table := range tables {
		expected, nameErr := managedfile.TableName(operation, index)
		if nameErr != nil || table.Name != expected || table.Rows < 0 || table.Rows > 1000 {
			return ManagedFileImport{}, ErrManagedFileState
		}
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return ManagedFileImport{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, r.bind(`UPDATE managed_file_imports SET status='staged',revision=revision+1,updated_at=CURRENT_TIMESTAMP
		WHERE id=? AND tenant_id=? AND owner_user_id=? AND revision=? AND status='preparing' AND sheet_count=?`),
		id, tenant, actor, revision, len(tables))
	if err != nil {
		return ManagedFileImport{}, err
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return ManagedFileImport{}, ErrManagedFileState
	}
	for index, table := range tables {
		_, err := tx.ExecContext(ctx, r.bind(`INSERT INTO managed_file_tables(id,operation_id,tenant_id,sheet_index,table_name,row_count) VALUES(?,?,?,?,?,?)`),
			id+"-"+strconv.Itoa(index), id, tenant, index, table.Name, table.Rows)
		if err != nil {
			return ManagedFileImport{}, err
		}
	}
	if err := r.audit(ctx, tx, id, "staged", actor, "", 0, 0); err != nil {
		return ManagedFileImport{}, err
	}
	if err := tx.Commit(); err != nil {
		return ManagedFileImport{}, err
	}
	return r.Get(ctx, id)
}

var managedFileNextStatus = map[string]map[string]bool{
	"preparing":      {"cleanup_needed": true},
	"staged":         {"cleanup_needed": true},
	"policy_ready":   {"cleanup_needed": true},
	"published":      {"deleting": true},
	"deleting":       {"deleted": true, "cleanup_needed": true},
	"cleanup_needed": {"deleted": true},
}

// Advance is a tenant-scoped compare-and-swap coupled to a management audit.
// It cannot skip the policy-ready publication gate. A staged physical table
// remains invisible while this metadata transition is unavailable.
func (r *ManagedFileImportRepository) Advance(ctx context.Context, id string, revision int64, from, to, actor string) (ManagedFileImport, error) {
	tenant, err := r.requireTenant(ctx, "advance managed file import")
	if err != nil {
		return ManagedFileImport{}, err
	}
	if id == "" || revision < 1 || !managedFileNextStatus[from][to] || strings.TrimSpace(actor) == "" {
		return ManagedFileImport{}, ErrManagedFileState
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return ManagedFileImport{}, err
	}
	defer tx.Rollback()
	query := `UPDATE managed_file_imports SET status=?,revision=revision+1,updated_at=CURRENT_TIMESTAMP WHERE id=? AND tenant_id=? AND owner_user_id=? AND revision=? AND status=?`
	result, err := tx.ExecContext(ctx, r.bind(query),
		to, id, tenant, actor, revision, from)
	if err != nil {
		return ManagedFileImport{}, err
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return ManagedFileImport{}, ErrManagedFileState
	}
	if err := r.audit(ctx, tx, id, to, actor, "", 0, 0); err != nil {
		return ManagedFileImport{}, err
	}
	if err := tx.Commit(); err != nil {
		return ManagedFileImport{}, err
	}
	return r.Get(ctx, id)
}

// PublishedTableOwner checks an exact table mapping and current binding. It
// returns false for all unfinished, revoked, foreign-user, or unknown tables.
// The caller must still validate SQL and database-backed column policy.
func (r *ManagedFileImportRepository) PublishedTableOwner(ctx context.Context, bindingID, datasourceID, agentID, ownerUserID, tableName string) (bool, error) {
	tenant, err := r.requireTenant(ctx, "check managed file table owner")
	if err != nil {
		return false, err
	}
	if bindingID == "" || datasourceID == "" || agentID == "" || ownerUserID == "" || !strings.HasPrefix(tableName, "agentsql_file_") || len(tableName) != 46 {
		return false, ErrManagedFileState
	}
	var count int
	err = r.db.QueryRowContext(ctx, r.bind(`SELECT COUNT(*) FROM managed_file_tables t
		JOIN managed_file_imports i ON i.id=t.operation_id AND i.tenant_id=t.tenant_id
		JOIN sqlbot_bindings b ON b.id=i.binding_id AND b.tenant_id=i.tenant_id
		WHERE t.tenant_id=? AND t.table_name=? AND i.binding_id=? AND i.datasource_id=? AND i.agent_id=? AND i.owner_user_id=?
		AND i.status='published' AND b.enabled=TRUE AND b.revision=i.binding_revision
		AND b.agent_id=i.agent_id AND b.datasource_id=i.datasource_id AND b.agentsql_user_id=i.owner_user_id`),
		tenant, tableName, bindingID, datasourceID, agentID, ownerUserID).Scan(&count)
	return count == 1, err
}

// ListPending excludes successfully published imports so old file tables do
// not starve recovery of unfinished operations when the page is bounded.
func (r *ManagedFileImportRepository) ListPending(ctx context.Context, limit int) ([]ManagedFileImport, error) {
	tenant, err := r.requireTenant(ctx, "list pending managed file imports")
	if err != nil {
		return nil, err
	}
	if limit < 1 || limit > 1000 {
		return nil, ErrManagedFileState
	}
	rows, err := r.db.QueryContext(ctx, r.bind(`SELECT `+managedFileImportColumns+` FROM managed_file_imports WHERE tenant_id=? AND status NOT IN ('published','deleted') ORDER BY created_at,id LIMIT ?`), tenant, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []ManagedFileImport{}
	for rows.Next() {
		item, err := scanManagedFileImport(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// ListRecoverableByDatasource bounds recovery to never-published rows on one
// managed datasource. policy_ready and deleting need separate grant handling.
func (r *ManagedFileImportRepository) ListRecoverableByDatasource(ctx context.Context, datasourceID string, limit int) ([]ManagedFileImport, error) {
	tenant, err := r.requireTenant(ctx, "list pending managed file imports by datasource")
	if err != nil {
		return nil, err
	}
	if datasourceID == "" || limit < 1 || limit > 1000 {
		return nil, ErrManagedFileState
	}
	rows, err := r.db.QueryContext(ctx, r.bind(`SELECT `+managedFileImportColumns+` FROM managed_file_imports WHERE tenant_id=? AND datasource_id=? AND status IN ('preparing','staged','cleanup_needed') ORDER BY created_at,id LIMIT ?`), tenant, datasourceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []ManagedFileImport{}
	for rows.Next() {
		item, err := scanManagedFileImport(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *ManagedFileImportRepository) audit(ctx context.Context, tx *sql.Tx, id, status, actor, fileHash string, bytes int64, sheets int) error {
	details, _ := json.Marshal(map[string]any{"operation_id": id, "status": status, "file_sha256": fileHash, "file_bytes": bytes, "sheet_count": sheets})
	return r.outbox.Append(ctx, tx, model.ManagementAuditOutbox{EventUUID: uuid.NewString(), Action: "managed_file." + status,
		ActorType: "user", ActorID: actor, DetailsJSON: string(details)})
}
