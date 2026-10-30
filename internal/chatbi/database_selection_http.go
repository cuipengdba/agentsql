package chatbi

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"time"
)

type databaseTableInput struct {
	Name    string `json:"table_name"`
	Comment string `json:"table_comment"`
}

// The unchanged SQLBot table-selection page submits its form configuration to
// getTablesByConf. For an existing datasource, the ID is only a lookup key;
// the submitted configuration is ignored and cannot instantiate a connection.
// Creating a new database datasource still requires a separate control-plane
// provisioning workflow and is deliberately not accepted here.
func (h *Handler) databaseTablesBySavedBinding(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	var input struct {
		ID   ID     `json:"id"`
		Type string `json:"type"`
	}
	if !readBody(w, r, &input) {
		return
	}
	numeric, err := strconv.ParseInt(string(input.ID), 10, 64)
	if !input.ID.Valid() || err != nil || numeric < 1 || numeric > maxSQLBotBrowserID {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid datasource ID")
		return
	}
	binding, revision, digest, err := h.service.bindingSnapshot(r.Context(), p, input.ID)
	if err != nil || !databaseBinding(binding) || (input.Type != "" && input.Type != binding.Dialect) {
		failure(w, ErrNotFound)
		return
	}
	all := binding
	all.SelectedTables = ""
	columns, policy, err := h.service.gatewaySchemaWithPermissionSnapshot(r.Context(), p, all)
	if err != nil {
		failure(w, ErrGateway)
		return
	}
	if _, err := filterDatabaseSchema(binding, columns); err != nil {
		failure(w, err)
		return
	}
	tables, valid := visibleBrowserTables(input.ID, columns)
	if !valid || len(tables) > 256 {
		failure(w, ErrGateway)
		return
	}
	result := make([]map[string]any, 0, len(tables))
	for _, table := range tables {
		result = append(result, map[string]any{"tableName": table.name, "tableComment": ""})
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	if err := h.service.bindingUnchanged(r.Context(), p, binding, revision, digest); err != nil {
		failure(w, err)
		return
	}
	if err := h.service.dataPermissionUnchanged(r.Context(), p.Workspace, policy); err != nil {
		failure(w, err)
		return
	}
	okay(w, result)
}

// The legacy select-tables request can only narrow a server-owned binding.
// Schema discovery comes from the current gateway authorization, never from a
// browser-supplied connection string or datasource configuration.
func (h *Handler) chooseDatabaseTables(w http.ResponseWriter, r *http.Request, p Principal, token string, binding Binding) {
	var input []databaseTableInput
	if !readBody(w, r, &input) {
		return
	}
	if len(input) > 256 {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid table selection")
		return
	}
	all := binding
	all.SelectedTables = ""
	columns, policy, err := h.service.gatewaySchemaWithPermissionSnapshot(r.Context(), p, all)
	if err != nil {
		failure(w, ErrGateway)
		return
	}
	if _, err := filterDatabaseSchema(binding, columns); err != nil {
		failure(w, err)
		return
	}
	tables, valid := visibleBrowserTables(binding.Datasource, columns)
	if !valid || len(tables) == 0 || len(tables) > 256 {
		failure(w, ErrGateway)
		return
	}
	available := make(map[string]databaseTableChoice, len(tables))
	for _, table := range tables {
		available[table.name] = databaseTableChoice{Schema: table.schema, Table: table.name}
	}
	selected := make([]databaseTableChoice, 0, len(input))
	seen := make(map[string]bool, len(input))
	for _, requested := range input {
		choice, exists := available[requested.Name]
		if !exists || seen[requested.Name] || requested.Comment != "" {
			writeJSON(w, http.StatusUnprocessableEntity, "Invalid table selection")
			return
		}
		seen[requested.Name] = true
		selected = append(selected, choice)
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	if err := h.service.live(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	if err := h.service.dataPermissionUnchanged(r.Context(), p.Workspace, policy); err != nil {
		failure(w, err)
		return
	}
	selection := "!"
	if len(selected) == len(tables) {
		selection = ""
	} else if len(selected) != 0 {
		sort.Slice(selected, func(i, j int) bool { return selected[i].Table < selected[j].Table })
		encoded, err := json.Marshal(selected)
		if err != nil {
			failure(w, ErrUnavailable)
			return
		}
		selection = string(encoded)
	}
	if err := h.service.Store.updateDatabaseTableSelection(r.Context(), p, binding, selection, selected); err != nil {
		failure(w, err)
		return
	}
	okay(w, nil)
}

func (h *Handler) databaseTableChoices(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	id := ID(r.PathValue("id"))
	numeric, err := strconv.ParseInt(string(id), 10, 64)
	if !id.Valid() || err != nil || numeric > maxSQLBotBrowserID {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid datasource ID")
		return
	}
	binding, revision, digest, err := h.service.bindingSnapshot(r.Context(), p, id)
	if err != nil || !databaseBinding(binding) {
		failure(w, ErrNotFound)
		return
	}
	selected, err := databaseSelectedTables(binding)
	if err != nil {
		failure(w, err)
		return
	}
	all := binding
	all.SelectedTables = ""
	columns, policy, err := h.service.gatewaySchemaWithPermissionSnapshot(r.Context(), p, all)
	if err != nil {
		failure(w, ErrGateway)
		return
	}
	if _, err := filterDatabaseSchema(binding, columns); err != nil {
		failure(w, err)
		return
	}
	tables, valid := visibleBrowserTables(id, columns)
	if !valid || len(tables) > 256 {
		failure(w, ErrGateway)
		return
	}
	result := make([]map[string]any, 0, len(tables))
	for _, table := range tables {
		result = append(result, map[string]any{"table_name": table.name, "schema": table.schema,
			"checked": selected == nil || selected[table.name] == table.schema})
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	if err := h.service.bindingUnchanged(r.Context(), p, binding, revision, digest); err != nil {
		failure(w, err)
		return
	}
	if err := h.service.dataPermissionUnchanged(r.Context(), p.Workspace, policy); err != nil {
		failure(w, err)
		return
	}
	okay(w, result)
}

func (s *Store) updateDatabaseTableSelection(ctx context.Context, p Principal, prior Binding, selection string, selected []databaseTableChoice) error {
	if s == nil || ctx == nil || !databaseBinding(prior) || prior.User != p.User.ID ||
		prior.Workspace != p.Workspace || !prior.Datasource.Valid() {
		return ErrNotFound
	}
	if selection != "" && selection != "!" {
		candidate := prior
		candidate.SelectedTables = selection
		parsed, err := databaseSelectedTables(candidate)
		if err != nil || len(parsed) != len(selected) {
			return ErrInvalidSemantic
		}
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
	audit, err := s.seal(auditKey, map[string]any{"action": "database.tables-select", "actor": p.User.ID,
		"workspace": p.Workspace, "datasource": prior.Datasource, "selected": len(selected),
		"tables": selected, "time": time.Now().UTC()})
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
