package chatbi

import (
	"context"
	"net/http"
	"strconv"
	"time"
)

// The gateway supplies live schema on every request. The original client's
// Sync Fields action therefore forces a fresh gateway read and records what
// was actually visible; no browser-supplied field list is trusted or cached.
func (h *Handler) syncDatasourceFields(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	datasource := ID(r.PathValue("id"))
	tableText := r.PathValue("tableID")
	tableID, parseErr := strconv.ParseInt(tableText, 10, 64)
	if !datasource.Valid() || parseErr != nil || tableID < 1 || tableID > maxSQLBotBrowserID ||
		strconv.FormatInt(tableID, 10) != tableText {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid table ID")
		return
	}
	binding, revision, digest, err := h.service.bindingSnapshot(r.Context(), p, datasource)
	if err != nil {
		failure(w, err)
		return
	}
	managed := binding.Type == "excel" && binding.Dialect == "pg" &&
		managedFileOperationPattern.MatchString(binding.ManagedFileID)
	if !databaseBinding(binding) && !managed {
		failure(w, ErrNotFound)
		return
	}
	columns, err := h.service.schema(r.Context(), p, binding)
	if err != nil {
		failure(w, err)
		return
	}
	tables, valid := visibleBrowserTables(datasource, columns)
	if !valid {
		failure(w, ErrGateway)
		return
	}
	var visible *visibleBrowserTable
	for i := range tables {
		if tables[i].id == tableID && (!managed || tables[i].schema == "agentsql_files") {
			visible = &tables[i]
			break
		}
	}
	if visible == nil {
		failure(w, ErrNotFound)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	if err := h.service.bindingUnchanged(r.Context(), p, binding, revision, digest); err != nil {
		failure(w, err)
		return
	}
	if err := h.service.Store.auditDatasourceSchemaSync(r.Context(), p, binding, visible.schema, visible.name, len(visible.columns)); err != nil {
		failure(w, err)
		return
	}
	okay(w, nil)
}

func (s *Store) auditDatasourceSchemaSync(ctx context.Context, p Principal, prior Binding,
	schema, table string, fields int) error {
	if s == nil || ctx == nil || prior.Workspace != p.Workspace || prior.User != p.User.ID ||
		!prior.Datasource.Valid() || !validDatabaseRemarkName(schema) || !validDatabaseRemarkName(table) || fields < 1 {
		return ErrNotFound
	}
	key := bindingKey(p, prior.Datasource)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	defer tx.Rollback()
	if err := s.verifyManagedWorkspaceActor(ctx, tx, p); err != nil {
		return err
	}
	var body []byte
	if err := tx.QueryRowContext(ctx,
		`SELECT body FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=?`,
		key.Kind, key.Scope, key.Owner, key.ID).Scan(&body); controlNoRows(err) {
		return ErrNotFound
	} else if err != nil {
		return ErrUnavailable
	}
	var current Binding
	if s.decode(key, body, &current) != nil || current != prior {
		return ErrConflict
	}
	auditID, err := newID()
	if err != nil {
		return err
	}
	auditKey := objectKey{"management-audit", "system", "system", string(auditID)}
	audit, err := s.seal(auditKey, map[string]any{"action": "datasource.schema-refresh",
		"actor": p.User.ID, "workspace": p.Workspace, "datasource": prior.Datasource,
		"schema": schema, "table": table, "fields": fields, "time": time.Now().UTC()})
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO chatbi_objects(kind,scope,owner,id,revision,body) VALUES(?,?,?,?,1,?)`,
		auditKey.Kind, auditKey.Scope, auditKey.Owner, auditKey.ID, audit); err != nil {
		return ErrUnavailable
	}
	if tx.Commit() != nil {
		return ErrUnavailable
	}
	return nil
}
