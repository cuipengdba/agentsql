package chatbi

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

func (s *Store) SystemVariables(ctx context.Context, workspace ID, name string) ([]SystemVariable, error) {
	if !workspace.Valid() {
		return nil, ErrNotFound
	}
	raw, err := s.list(ctx, "system-variable", string(workspace), "workspace")
	if err != nil {
		return nil, err
	}
	result := make([]SystemVariable, 0, len(raw))
	for _, item := range raw {
		var variable SystemVariable
		if json.Unmarshal(item, &variable) != nil || !validSystemVariable(variable) || variable.ID < 1 || variable.Type != "custom" {
			return nil, ErrUnavailable
		}
		if strings.Contains(strings.ToLower(variable.Name), strings.ToLower(name)) {
			result = append(result, variable)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func (s *Store) SaveSystemVariable(ctx context.Context, p Principal, variable SystemVariable) error {
	if !p.Workspace.Valid() || !p.User.ID.Valid() || p.EmbeddedApp != "" || !validSystemVariable(variable) || variable.ID < 1 || variable.Type != "custom" {
		return ErrInvalidAccountChange
	}
	key := systemVariableKey(p.Workspace, variable.ID)
	auditID, err := newID()
	if err != nil {
		return err
	}
	auditKey := objectKey{"management-audit", "system", "system", string(auditID)}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	defer tx.Rollback()
	if err := s.verifyManagedWorkspaceActor(ctx, tx, p); err != nil {
		return err
	}
	var oldBody []byte
	var oldRevision int64
	err = tx.QueryRowContext(ctx, `SELECT revision,body FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=?`,
		key.Kind, key.Scope, key.Owner, key.ID).Scan(&oldRevision, &oldBody)
	if err != nil && !controlNoRows(err) {
		return ErrUnavailable
	}
	if err == nil {
		if !variable.Created.IsZero() {
			return ErrConflict
		}
		var previous SystemVariable
		if s.decode(key, oldBody, &previous) != nil || previous.ID != variable.ID || previous.Type != "custom" {
			return ErrUnavailable
		}
		variable.Created, variable.CreatedBy = previous.Created, previous.CreatedBy
	} else if variable.Created.IsZero() || variable.CreatedBy != p.User.ID {
		return ErrNotFound
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,body FROM chatbi_objects WHERE kind='system-variable' AND scope=? AND owner='workspace'`, p.Workspace)
	if err != nil {
		return ErrUnavailable
	}
	for rows.Next() {
		var otherID string
		var otherBody []byte
		if rows.Scan(&otherID, &otherBody) != nil {
			rows.Close()
			return ErrUnavailable
		}
		otherKey := objectKey{"system-variable", string(p.Workspace), "workspace", otherID}
		var other SystemVariable
		if s.decode(otherKey, otherBody, &other) != nil || !validSystemVariable(other) {
			rows.Close()
			return ErrUnavailable
		}
		if otherID != key.ID && strings.EqualFold(other.Name, variable.Name) {
			rows.Close()
			return ErrConflict
		}
	}
	if rows.Err() != nil {
		rows.Close()
		return ErrUnavailable
	}
	rows.Close()
	sealed, err := s.seal(key, variable)
	if err != nil {
		return err
	}
	action := "system-variable.update"
	if oldRevision == 0 {
		action = "system-variable.create"
	}
	audit, err := s.seal(auditKey, map[string]any{"action": action, "actor": p.User.ID,
		"workspace": p.Workspace, "id": variable.ID, "time": time.Now().UTC()})
	if err != nil {
		return err
	}
	if oldRevision == 0 {
		result, err := tx.ExecContext(ctx, `INSERT INTO chatbi_objects(kind,scope,owner,id,revision,body) VALUES(?,?,?,?,1,?) ON CONFLICT DO NOTHING`,
			key.Kind, key.Scope, key.Owner, key.ID, sealed)
		if err != nil {
			return ErrUnavailable
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return ErrConflict
		}
	} else {
		result, err := tx.ExecContext(ctx, `UPDATE chatbi_objects SET revision=revision+1,body=? WHERE kind=? AND scope=? AND owner=? AND id=? AND revision=?`,
			sealed, key.Kind, key.Scope, key.Owner, key.ID, oldRevision)
		if err != nil {
			return ErrUnavailable
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return ErrConflict
		}
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

func (s *Store) DeleteSystemVariables(ctx context.Context, p Principal, ids []int64) error {
	if !p.Workspace.Valid() || !p.User.ID.Valid() || p.EmbeddedApp != "" || len(ids) == 0 || len(ids) > 100 {
		return ErrInvalidAccountChange
	}
	auditID, err := newID()
	if err != nil {
		return err
	}
	auditKey := objectKey{"management-audit", "system", "system", string(auditID)}
	audit, err := s.seal(auditKey, map[string]any{"action": "system-variable.delete", "actor": p.User.ID,
		"workspace": p.Workspace, "ids": ids, "time": time.Now().UTC()})
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	defer tx.Rollback()
	if err := s.verifyManagedWorkspaceActor(ctx, tx, p); err != nil {
		return err
	}
	for _, id := range ids {
		if id < 1 || id > maxSQLBotBrowserID {
			return ErrInvalidAccountChange
		}
		key := systemVariableKey(p.Workspace, id)
		var body []byte
		if err := tx.QueryRowContext(ctx, `SELECT body FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=?`,
			key.Kind, key.Scope, key.Owner, key.ID).Scan(&body); err != nil {
			if controlNoRows(err) {
				return ErrNotFound
			}
			return ErrUnavailable
		}
		var variable SystemVariable
		if s.decode(key, body, &variable) != nil || variable.ID != id || variable.Type != "custom" {
			return ErrUnavailable
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=?`,
			key.Kind, key.Scope, key.Owner, key.ID); err != nil {
			return ErrUnavailable
		}
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
