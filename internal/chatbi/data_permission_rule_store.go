package chatbi

import (
	"context"
	"strconv"
	"time"
)

func numericWorkspaceID(workspace ID) int64 {
	id, _ := strconv.ParseInt(string(workspace), 10, 64)
	return id
}

func (s *Store) SaveDataPermissionRule(ctx context.Context, p Principal, rule DataPermissionRule, create bool) (DataPermissionRule, error) {
	if s == nil || ctx == nil || p.EmbeddedApp != "" || !p.Workspace.Valid() || !p.User.ID.Valid() ||
		!validPermissionRule(rule) || rule.Enable && len(rule.Permissions) > 0 ||
		rule.OID != numericWorkspaceID(p.Workspace) {
		return DataPermissionRule{}, ErrInvalidDataPermission
	}
	key := permissionRuleKey(p.Workspace, rule.ID)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DataPermissionRule{}, ErrUnavailable
	}
	defer tx.Rollback()
	if err := s.verifyManagedWorkspaceActor(ctx, tx, p); err != nil {
		return DataPermissionRule{}, err
	}
	var oldBody []byte
	var revision int64
	err = tx.QueryRowContext(ctx,
		`SELECT revision,body FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=?`,
		key.Kind, key.Scope, key.Owner, key.ID).Scan(&revision, &oldBody)
	if err != nil && !controlNoRows(err) {
		return DataPermissionRule{}, ErrUnavailable
	}
	if create {
		if err == nil {
			return DataPermissionRule{}, ErrConflict
		}
		var count int
		if tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM chatbi_objects WHERE kind=? AND scope=? AND owner=?`,
			key.Kind, key.Scope, key.Owner).Scan(&count) != nil || count >= 256 {
			return DataPermissionRule{}, ErrUnavailable
		}
	} else {
		if controlNoRows(err) {
			return DataPermissionRule{}, ErrNotFound
		}
		var previous DataPermissionRule
		if s.decode(key, oldBody, &previous) != nil || !validPermissionRule(previous) ||
			previous.ID != rule.ID || previous.OID != rule.OID {
			return DataPermissionRule{}, ErrUnavailable
		}
		rule.Created = previous.Created
	}
	rule.Permissions, err = normalizePermissionDrafts(rule.Permissions, oldBody, s, key)
	if err != nil {
		return DataPermissionRule{}, err
	}
	if !validPermissionRule(rule) {
		return DataPermissionRule{}, ErrInvalidDataPermission
	}
	sealed, err := s.seal(key, rule)
	if err != nil {
		return DataPermissionRule{}, err
	}
	auditID, err := newID()
	if err != nil {
		return DataPermissionRule{}, err
	}
	auditKey := objectKey{"management-audit", "system", "system", string(auditID)}
	action := "data-permission.update"
	if create {
		action = "data-permission.create"
	}
	audit, err := s.seal(auditKey, map[string]any{"action": action, "actor": p.User.ID,
		"workspace": p.Workspace, "rule": rule.ID, "enabled": rule.Enable,
		"permissions": len(rule.Permissions), "users": len(rule.Users), "time": time.Now().UTC()})
	if err != nil {
		return DataPermissionRule{}, err
	}
	if create {
		result, err := tx.ExecContext(ctx,
			`INSERT INTO chatbi_objects(kind,scope,owner,id,revision,body) VALUES(?,?,?,?,1,?) ON CONFLICT DO NOTHING`,
			key.Kind, key.Scope, key.Owner, key.ID, sealed)
		if err != nil {
			return DataPermissionRule{}, ErrUnavailable
		}
		if count, err := result.RowsAffected(); err != nil || count != 1 {
			return DataPermissionRule{}, ErrConflict
		}
	} else {
		result, err := tx.ExecContext(ctx,
			`UPDATE chatbi_objects SET revision=revision+1,body=? WHERE kind=? AND scope=? AND owner=? AND id=? AND revision=?`,
			sealed, key.Kind, key.Scope, key.Owner, key.ID, revision)
		if err != nil {
			return DataPermissionRule{}, ErrUnavailable
		}
		if count, err := result.RowsAffected(); err != nil || count != 1 {
			return DataPermissionRule{}, ErrConflict
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO chatbi_objects(kind,scope,owner,id,revision,body) VALUES(?,?,?,?,1,?)`,
		auditKey.Kind, auditKey.Scope, auditKey.Owner, auditKey.ID, audit); err != nil {
		return DataPermissionRule{}, ErrUnavailable
	}
	if tx.Commit() != nil {
		return DataPermissionRule{}, ErrUnavailable
	}
	return rule, nil
}

func (s *Store) DeleteDataPermissionRule(ctx context.Context, p Principal, id int64) error {
	if s == nil || ctx == nil || p.EmbeddedApp != "" || !p.Workspace.Valid() || !p.User.ID.Valid() ||
		id < 1 || id > maxSQLBotBrowserID {
		return ErrInvalidDataPermission
	}
	key := permissionRuleKey(p.Workspace, id)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	defer tx.Rollback()
	if err := s.verifyManagedWorkspaceActor(ctx, tx, p); err != nil {
		return err
	}
	var body []byte
	var revision int64
	if err := tx.QueryRowContext(ctx,
		`SELECT revision,body FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=?`,
		key.Kind, key.Scope, key.Owner, key.ID).Scan(&revision, &body); err != nil {
		if controlNoRows(err) {
			return ErrNotFound
		}
		return ErrUnavailable
	}
	var previous DataPermissionRule
	if s.decode(key, body, &previous) != nil || !validPermissionRule(previous) || previous.ID != id ||
		previous.OID != numericWorkspaceID(p.Workspace) {
		return ErrUnavailable
	}
	auditID, err := newID()
	if err != nil {
		return err
	}
	auditKey := objectKey{"management-audit", "system", "system", string(auditID)}
	audit, err := s.seal(auditKey, map[string]any{"action": "data-permission.delete", "actor": p.User.ID,
		"workspace": p.Workspace, "rule": id, "time": time.Now().UTC()})
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx,
		`DELETE FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=? AND revision=?`,
		key.Kind, key.Scope, key.Owner, key.ID, revision)
	if err != nil {
		return ErrUnavailable
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return ErrConflict
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
