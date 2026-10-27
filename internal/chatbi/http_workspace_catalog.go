package chatbi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

func validWorkspaceName(name string) bool {
	return name != "" && len(name) <= 100 && !strings.ContainsAny(name, "\r\n\x00")
}

func (h *Handler) legacyWorkspaceCreate(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	var input struct {
		Name string `json:"name"`
	}
	if !readBody(w, r, &input) {
		return
	}
	name := strings.TrimSpace(input.Name)
	if !validWorkspaceName(name) {
		failure(w, ErrInvalidAccountChange)
		return
	}
	id, err := newID()
	if err != nil {
		failure(w, err)
		return
	}
	item := workspaceOption{ID: id, Name: name, Created: time.Now().UTC().UnixMilli()}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	if err := h.service.Store.CreateWorkspaceManaged(r.Context(), item, p); err != nil {
		failure(w, err)
		return
	}
	okay(w, workspaceLegacyView(item))
}

func (h *Handler) legacyWorkspaceUpdate(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	var input struct {
		ID   ID     `json:"id"`
		Name string `json:"name"`
	}
	if !readBody(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if !input.ID.Valid() || !validWorkspaceName(input.Name) {
		failure(w, ErrInvalidAccountChange)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	item, err := h.service.UpdateWorkspaceManaged(r.Context(), input.ID, input.Name, p)
	if err != nil {
		failure(w, err)
		return
	}
	okay(w, workspaceLegacyView(item))
}

func (h *Handler) legacyWorkspaceDelete(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	id := ID(r.PathValue("id"))
	if !id.Valid() || id == "1" {
		failure(w, ErrInvalidAccountChange)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	if err := h.service.Store.DeleteWorkspaceManaged(r.Context(), id, p); err != nil {
		failure(w, err)
		return
	}
	okay(w, nil)
}

func (s *Store) verifyManagedWorkspaceActor(ctx context.Context, tx *sql.Tx, p Principal) error {
	var actorBody []byte
	if err := tx.QueryRowContext(ctx, `SELECT body FROM chatbi_objects WHERE kind='user' AND scope='system' AND owner='system' AND id=?`, p.User.ID).Scan(&actorBody); err != nil {
		return ErrUnauthenticated
	}
	var actor User
	if err := s.decode(userKey(p.User.ID), actorBody, &actor); err != nil || actor.ID != p.User.ID ||
		actor.AuthRevision != p.User.AuthRevision || !actor.Admin || !actor.canUse(p.Workspace) {
		return ErrUnauthenticated
	}
	return nil
}

func (s *Store) verifyWorkspaceNamesTx(ctx context.Context, tx *sql.Tx, memberships []Membership) error {
	for _, membership := range memberships {
		key := objectKey{"workspace", "system", "system", string(membership.Workspace)}
		var body []byte
		err := tx.QueryRowContext(ctx, `SELECT body FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=?`,
			key.Kind, key.Scope, key.Owner, key.ID).Scan(&body)
		if errors.Is(err, sql.ErrNoRows) {
			continue // Imported workspaces may exist only in member records.
		}
		if err != nil {
			return ErrUnavailable
		}
		var item workspaceOption
		if s.decode(key, body, &item) != nil || item.ID != membership.Workspace || item.Name != membership.Name {
			return ErrConflict
		}
	}
	return nil
}

func (service *Service) UpdateWorkspaceManaged(ctx context.Context, id ID, name string, p Principal) (workspaceOption, error) {
	options, err := service.allWorkspaceOptions(ctx)
	if err != nil {
		return workspaceOption{}, err
	}
	for _, item := range options {
		if item.ID == id {
			return service.Store.updateWorkspaceManaged(ctx, item, name, p)
		}
	}
	return workspaceOption{}, ErrNotFound
}

func (s *Store) updateWorkspaceManaged(ctx context.Context, old workspaceOption, name string, p Principal) (workspaceOption, error) {
	if !old.ID.Valid() || !validWorkspaceName(name) || !p.User.ID.Valid() || p.EmbeddedApp != "" {
		return workspaceOption{}, ErrInvalidAccountChange
	}
	key := objectKey{"workspace", "system", "system", string(old.ID)}
	var current workspaceOption
	revision, err := s.get(ctx, key, &current)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return workspaceOption{}, err
	}
	if err == nil && (current.ID != old.ID || current.Name != old.Name) {
		return workspaceOption{}, ErrConflict
	}
	updated := old
	updated.Name = name
	if err == nil {
		updated.Created = current.Created
	}
	workspaceBody, err := s.seal(key, updated)
	if err != nil {
		return workspaceOption{}, err
	}
	type change struct {
		key      objectKey
		body     []byte
		revision int64
	}
	changes := []change{}
	users, err := s.list(ctx, "user", "system", "system")
	if err != nil {
		return workspaceOption{}, err
	}
	for _, raw := range users {
		var user User
		if json.Unmarshal(raw, &user) != nil || !user.ID.Valid() {
			return workspaceOption{}, ErrUnavailable
		}
		index := -1
		for i, membership := range user.Memberships {
			if membership.Workspace == old.ID {
				if membership.Name != old.Name {
					return workspaceOption{}, ErrUnavailable
				}
				index = i
				break
			}
		}
		if index < 0 {
			continue
		}
		stored, userRevision, err := s.User(ctx, user.ID)
		if err != nil {
			return workspaceOption{}, err
		}
		stored.Memberships[index].Name = name
		targetKey := userKey(stored.ID)
		body, err := s.seal(targetKey, stored)
		if err != nil {
			return workspaceOption{}, err
		}
		changes = append(changes, change{key: targetKey, body: body, revision: userRevision})
	}
	auditID, err := newID()
	if err != nil {
		return workspaceOption{}, err
	}
	auditKey := objectKey{"management-audit", "system", "system", string(auditID)}
	audit, err := s.seal(auditKey, map[string]any{"action": "workspace.update", "actor": p.User.ID,
		"workspace": old.ID, "time": time.Now().UTC()})
	if err != nil {
		return workspaceOption{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return workspaceOption{}, ErrUnavailable
	}
	defer tx.Rollback()
	if err := s.verifyManagedWorkspaceActor(ctx, tx, p); err != nil {
		return workspaceOption{}, err
	}
	// A member may have been granted this workspace after the preflight read.
	// Abort rather than leave a catalog name that disagrees with that member.
	changedUsers := make(map[string]bool, len(changes))
	for _, change := range changes {
		changedUsers[change.key.ID] = true
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,body FROM chatbi_objects WHERE kind='user' AND scope='system' AND owner='system'`)
	if err != nil {
		return workspaceOption{}, ErrUnavailable
	}
	for rows.Next() {
		var userID string
		var body []byte
		if err := rows.Scan(&userID, &body); err != nil {
			rows.Close()
			return workspaceOption{}, ErrUnavailable
		}
		var user User
		if err := s.decode(userKey(ID(userID)), body, &user); err != nil {
			rows.Close()
			return workspaceOption{}, ErrUnavailable
		}
		for _, membership := range user.Memberships {
			if membership.Workspace == old.ID && !changedUsers[userID] {
				rows.Close()
				return workspaceOption{}, ErrConflict
			}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return workspaceOption{}, ErrUnavailable
	}
	rows.Close()
	if revision == 0 {
		result, err := tx.ExecContext(ctx, `INSERT INTO chatbi_objects(kind,scope,owner,id,revision,body) VALUES(?,?,?,?,1,?) ON CONFLICT DO NOTHING`,
			key.Kind, key.Scope, key.Owner, key.ID, workspaceBody)
		if err != nil {
			return workspaceOption{}, ErrUnavailable
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return workspaceOption{}, ErrConflict
		}
	} else {
		result, err := tx.ExecContext(ctx, `UPDATE chatbi_objects SET revision=revision+1,body=? WHERE kind=? AND scope=? AND owner=? AND id=? AND revision=?`,
			workspaceBody, key.Kind, key.Scope, key.Owner, key.ID, revision)
		if err != nil {
			return workspaceOption{}, ErrUnavailable
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return workspaceOption{}, ErrConflict
		}
	}
	for _, item := range changes {
		result, err := tx.ExecContext(ctx, `UPDATE chatbi_objects SET revision=revision+1,body=? WHERE kind=? AND scope=? AND owner=? AND id=? AND revision=?`,
			item.body, item.key.Kind, item.key.Scope, item.key.Owner, item.key.ID, item.revision)
		if err != nil {
			return workspaceOption{}, ErrUnavailable
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return workspaceOption{}, ErrConflict
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO chatbi_objects(kind,scope,owner,id,revision,body) VALUES(?,?,?,?,1,?)`,
		auditKey.Kind, auditKey.Scope, auditKey.Owner, auditKey.ID, audit); err != nil {
		return workspaceOption{}, ErrUnavailable
	}
	if err := tx.Commit(); err != nil {
		return workspaceOption{}, ErrUnavailable
	}
	return updated, nil
}

func (s *Store) DeleteWorkspaceManaged(ctx context.Context, id ID, p Principal) error {
	if !id.Valid() || id == "1" || !p.User.ID.Valid() || p.EmbeddedApp != "" {
		return ErrInvalidAccountChange
	}
	key := objectKey{"workspace", "system", "system", string(id)}
	var item workspaceOption
	revision, err := s.get(ctx, key, &item)
	if err != nil {
		return err
	}
	if item.ID != id {
		return ErrUnavailable
	}
	auditID, err := newID()
	if err != nil {
		return err
	}
	auditKey := objectKey{"management-audit", "system", "system", string(auditID)}
	audit, err := s.seal(auditKey, map[string]any{"action": "workspace.delete", "actor": p.User.ID,
		"workspace": id, "time": time.Now().UTC()})
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
	var scopedObjects int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM chatbi_objects WHERE scope=?`, string(id)).Scan(&scopedObjects); err != nil {
		return ErrUnavailable
	}
	if scopedObjects != 0 {
		return ErrConflict
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,body FROM chatbi_objects WHERE kind='user' AND scope='system' AND owner='system'`)
	if err != nil {
		return ErrUnavailable
	}
	for rows.Next() {
		var userID string
		var body []byte
		if err := rows.Scan(&userID, &body); err != nil {
			rows.Close()
			return ErrUnavailable
		}
		var user User
		if err := s.decode(userKey(ID(userID)), body, &user); err != nil {
			rows.Close()
			return err
		}
		for _, membership := range user.Memberships {
			if membership.Workspace == id {
				rows.Close()
				return ErrConflict
			}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return ErrUnavailable
	}
	rows.Close()
	result, err := tx.ExecContext(ctx, `DELETE FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=? AND revision=?`,
		key.Kind, key.Scope, key.Owner, key.ID, revision)
	if err != nil {
		return ErrUnavailable
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO chatbi_objects(kind,scope,owner,id,revision,body) VALUES(?,?,?,?,1,?)`,
		auditKey.Kind, auditKey.Scope, auditKey.Owner, auditKey.ID, audit); err != nil {
		return ErrUnavailable
	}
	if err := tx.Commit(); err != nil {
		return ErrUnavailable
	}
	return nil
}

func (s *Store) CreateWorkspaceManaged(ctx context.Context, item workspaceOption, p Principal) error {
	if !item.ID.Valid() || !validWorkspaceName(item.Name) || !p.User.ID.Valid() || p.EmbeddedApp != "" {
		return ErrInvalidAccountChange
	}
	key := objectKey{"workspace", "system", "system", string(item.ID)}
	body, err := s.seal(key, item)
	if err != nil {
		return err
	}
	auditID, err := newID()
	if err != nil {
		return err
	}
	auditKey := objectKey{"management-audit", "system", "system", string(auditID)}
	audit, err := s.seal(auditKey, map[string]any{"action": "workspace.create", "actor": p.User.ID,
		"workspace": item.ID, "time": time.Now().UTC()})
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
	result, err := tx.ExecContext(ctx, `INSERT INTO chatbi_objects(kind,scope,owner,id,revision,body) VALUES(?,?,?,?,1,?) ON CONFLICT DO NOTHING`,
		key.Kind, key.Scope, key.Owner, key.ID, body)
	if err != nil {
		return ErrUnavailable
	}
	count, err := result.RowsAffected()
	if err != nil {
		return ErrUnavailable
	}
	if count != 1 {
		return ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO chatbi_objects(kind,scope,owner,id,revision,body) VALUES(?,?,?,?,1,?)`,
		auditKey.Kind, auditKey.Scope, auditKey.Owner, auditKey.ID, audit); err != nil {
		return ErrUnavailable
	}
	if err := tx.Commit(); err != nil {
		return ErrUnavailable
	}
	return nil
}
