package chatbi

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// AgentSQL's own datasource page can edit the same selected set without
// decoding the legacy browser wire format or exposing connector credentials.
func (h *Handler) managedFileTableChoices(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	id := ID(r.PathValue("id"))
	if !id.Valid() {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid datasource ID")
		return
	}
	binding, revision, digest, err := h.service.bindingSnapshot(r.Context(), p, id)
	if err != nil || binding.Type != "excel" || binding.Dialect != "pg" ||
		!managedFileOperationPattern.MatchString(binding.ManagedFileID) {
		failure(w, ErrNotFound)
		return
	}
	_, sheets, err := decodeSQLBotExcelConfig(binding.Configuration)
	if err != nil {
		failure(w, ErrUnavailable)
		return
	}
	selected, err := managedFileSelectedTables(binding)
	if err != nil {
		failure(w, err)
		return
	}
	result := make([]map[string]any, 0, len(sheets))
	seen := make(map[string]bool, len(sheets))
	for _, sheet := range sheets {
		if seen[sheet.TableName] {
			failure(w, ErrUnavailable)
			return
		}
		seen[sheet.TableName] = true
		result = append(result, map[string]any{"table_name": sheet.TableName,
			"checked": selected == nil || selected[sheet.TableName]})
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	if err := h.service.bindingUnchanged(r.Context(), p, binding, revision, digest); err != nil {
		failure(w, err)
		return
	}
	okay(w, result)
}

// chooseManagedFileTables accepts the original browser's table-selection
// request only for a file datasource already claimed by this user. Selection
// is an additional local restriction; it cannot create gateway permission.
func (h *Handler) chooseManagedFileTables(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	id := ID(r.PathValue("id"))
	numeric, err := strconv.ParseInt(string(id), 10, 64)
	if !id.Valid() || err != nil || numeric < 1 || numeric > maxSQLBotBrowserID {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid datasource ID")
		return
	}
	var input []struct {
		Name    string `json:"table_name"`
		Comment string `json:"table_comment"`
	}
	if !readBody(w, r, &input) {
		return
	}
	if len(input) > 8 {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid table selection")
		return
	}
	binding, _, _, err := h.service.bindingSnapshot(r.Context(), p, id)
	if err != nil || binding.Type != "excel" || binding.Dialect != "pg" ||
		!managedFileOperationPattern.MatchString(binding.ManagedFileID) {
		failure(w, ErrNotFound)
		return
	}
	_, sheets, err := decodeSQLBotExcelConfig(binding.Configuration)
	if err != nil {
		failure(w, ErrUnavailable)
		return
	}
	imported := make(map[string]bool, len(sheets))
	for _, sheet := range sheets {
		if imported[sheet.TableName] {
			failure(w, ErrUnavailable)
			return
		}
		imported[sheet.TableName] = true
	}
	selected := make(map[string]bool, len(input))
	for _, table := range input {
		if !managedFileNamePattern.MatchString(table.Name) || !imported[table.Name] ||
			selected[table.Name] || table.Comment != "" {
			writeJSON(w, http.StatusUnprocessableEntity, "Invalid table selection")
			return
		}
		selected[table.Name] = true
	}
	columns, err := h.service.Gateway.Schema(r.Context(), p, binding)
	if err != nil {
		failure(w, ErrGateway)
		return
	}
	visible := make(map[string]bool, len(selected))
	for _, column := range columns {
		if column.Schema == "agentsql_files" && selected[column.Table] && column.Name != "" {
			visible[column.Table] = true
		}
	}
	if len(visible) != len(selected) {
		failure(w, ErrGateway)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	names := make([]string, 0, len(selected))
	for name := range selected {
		names = append(names, name)
	}
	sort.Strings(names)
	selection := strings.Join(names, ",")
	if len(names) == 0 {
		selection = "!"
	} else if len(names) == len(imported) {
		selection = ""
	}
	if err := h.service.Store.updateManagedFileSelection(r.Context(), p, binding, selection, len(selected)); err != nil {
		failure(w, err)
		return
	}
	okay(w, nil)
}

// updateManagedFileSelection changes the encrypted binding and records the
// management event in the same transaction. A concurrent delete, revocation,
// or selection change leaves the old state intact.
func (s *Store) updateManagedFileSelection(ctx context.Context, p Principal, prior Binding, selection string, selectedCount int) error {
	if s == nil || ctx == nil || prior.Workspace != p.Workspace || prior.User != p.User.ID ||
		!prior.Datasource.Valid() || prior.Type != "excel" || prior.Dialect != "pg" ||
		!managedFileOperationPattern.MatchString(prior.ManagedFileID) ||
		selectedCount < 0 || selectedCount > 8 {
		return ErrNotFound
	}
	_, sheets, err := decodeSQLBotExcelConfig(prior.Configuration)
	if err != nil {
		return ErrUnavailable
	}
	imported := make(map[string]bool, len(sheets))
	for _, sheet := range sheets {
		if imported[sheet.TableName] {
			return ErrUnavailable
		}
		imported[sheet.TableName] = true
	}
	auditTables := make([]string, 0, selectedCount)
	if selection == "" {
		if selectedCount != len(imported) {
			return ErrInvalidSemantic
		}
		for name := range imported {
			auditTables = append(auditTables, name)
		}
	} else {
		candidate := prior
		candidate.SelectedTables = selection
		selected, err := managedFileSelectedTables(candidate)
		if err != nil || selected == nil || len(selected) != selectedCount ||
			selectedCount == len(imported) {
			return ErrInvalidSemantic
		}
		for name := range selected {
			if !imported[name] {
				return ErrInvalidSemantic
			}
			auditTables = append(auditTables, name)
		}
	}
	sort.Strings(auditTables)
	key := bindingKey(p, prior.Datasource)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	defer tx.Rollback()
	if err := s.verifyManagedWorkspaceActor(ctx, tx, p); err != nil {
		return err
	}
	var oldBody []byte
	var revision int64
	err = tx.QueryRowContext(ctx, `SELECT revision,body FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=?`,
		key.Kind, key.Scope, key.Owner, key.ID).Scan(&revision, &oldBody)
	if controlNoRows(err) {
		return ErrNotFound
	}
	if err != nil {
		return ErrUnavailable
	}
	var current Binding
	if s.decode(key, oldBody, &current) != nil || current != prior {
		return ErrConflict
	}
	if current.SelectedTables == selection {
		return nil
	}
	current.SelectedTables = selection
	sealed, err := s.seal(key, current)
	if err != nil {
		return err
	}
	auditID, err := newID()
	if err != nil {
		return err
	}
	auditKey := objectKey{"management-audit", "system", "system", string(auditID)}
	audit, err := s.seal(auditKey, map[string]any{"action": "file-import.tables-select", "actor": p.User.ID,
		"workspace": p.Workspace, "datasource": prior.Datasource, "selected": selectedCount,
		"tables": auditTables, "time": time.Now().UTC()})
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE chatbi_objects SET revision=revision+1,body=? WHERE kind=? AND scope=? AND owner=? AND id=? AND revision=?`,
		sealed, key.Kind, key.Scope, key.Owner, key.ID, revision)
	if err != nil {
		return ErrUnavailable
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO chatbi_objects(kind,scope,owner,id,revision,body) VALUES(?,?,?,?,1,?)`,
		auditKey.Kind, auditKey.Scope, auditKey.Owner, auditKey.ID, audit); err != nil {
		return ErrUnavailable
	}
	if tx.Commit() != nil {
		return ErrUnavailable
	}
	return nil
}
