package chatbi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

func (h *Handler) relationSnapshot(w http.ResponseWriter, r *http.Request, p Principal, token string) (Binding, int64, []Column, bool) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return Binding{}, 0, nil, false
	}
	id := ID(r.PathValue("id"))
	numeric, err := strconv.ParseInt(string(id), 10, 64)
	if !id.Valid() || err != nil || numeric < 1 || numeric > maxSQLBotBrowserID {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid datasource ID")
		return Binding{}, 0, nil, false
	}
	binding, revision, digest, err := h.service.bindingSnapshot(r.Context(), p, id)
	if err != nil {
		failure(w, err)
		return Binding{}, 0, nil, false
	}
	columns, err := h.service.schema(r.Context(), p, binding)
	if err != nil {
		failure(w, err)
		return Binding{}, 0, nil, false
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return Binding{}, 0, nil, false
	}
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return Binding{}, 0, nil, false
	}
	if err := h.service.bindingUnchanged(r.Context(), p, binding, revision, digest); err != nil {
		failure(w, err)
		return Binding{}, 0, nil, false
	}
	return binding, revision, columns, true
}

func (h *Handler) tableRelationGet(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	binding, revision, columns, ok := h.relationSnapshot(w, r, p, token)
	if !ok {
		return
	}
	graph := make([]map[string]any, 0)
	if binding.TableRelations != "" {
		var err error
		graph, err = relationGraph([]byte(binding.TableRelations), binding.Datasource, columns)
		if err != nil {
			failure(w, ErrGateway)
			return
		}
	}
	// The schema or actor may be revoked while the stored graph is parsed.
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	current, currentRevision, _, err := h.service.bindingSnapshot(r.Context(), p, binding.Datasource)
	if err != nil || current != binding || currentRevision != revision {
		failure(w, ErrGateway)
		return
	}
	okay(w, graph)
}

func (h *Handler) tableRelationSave(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	var raw json.RawMessage
	if !readBody(w, r, &raw) {
		return
	}
	binding, revision, columns, ok := h.relationSnapshot(w, r, p, token)
	if !ok {
		return
	}
	graph, err := relationGraph(raw, binding.Datasource, columns)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid table relationship graph")
		return
	}
	encoded, err := json.Marshal(graph)
	if err != nil {
		failure(w, ErrUnavailable)
		return
	}
	if err := h.service.Store.updateTableRelations(r.Context(), p, binding, revision, string(encoded), len(graph)); err != nil {
		failure(w, err)
		return
	}
	okay(w, nil)
}

func (s *Store) updateTableRelations(ctx context.Context, p Principal, prior Binding, revision int64, graph string, cellCount int) error {
	if s == nil || ctx == nil || p.EmbeddedApp != "" || prior.User != p.User.ID || prior.Workspace != p.Workspace ||
		!prior.Datasource.Valid() || revision < 1 || cellCount < 0 || cellCount > 128 || len(graph) > 64<<10 {
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
	var oldBody []byte
	var currentRevision int64
	err = tx.QueryRowContext(ctx, `SELECT revision,body FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=?`,
		key.Kind, key.Scope, key.Owner, key.ID).Scan(&currentRevision, &oldBody)
	if controlNoRows(err) {
		return ErrNotFound
	}
	if err != nil {
		return ErrUnavailable
	}
	var current Binding
	if s.decode(key, oldBody, &current) != nil || current != prior || currentRevision != revision {
		return ErrConflict
	}
	if current.TableRelations == graph {
		return nil
	}
	current.TableRelations = graph
	sealed, err := s.seal(key, current)
	if err != nil {
		return err
	}
	auditID, err := newID()
	if err != nil {
		return err
	}
	auditKey := objectKey{"management-audit", "system", "system", string(auditID)}
	audit, err := s.seal(auditKey, map[string]any{"action": "datasource.table-relations-save", "actor": p.User.ID,
		"workspace": p.Workspace, "datasource": prior.Datasource, "cells": cellCount, "time": time.Now().UTC()})
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
