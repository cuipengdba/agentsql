package chatbi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
)

type workspaceOption struct {
	ID      ID     `json:"id"`
	Name    string `json:"name"`
	Created int64  `json:"create_time"`
}

// Imported SQLBot memberships carry the canonical workspace name. Until the
// separate workspace catalog is migrated, disagreeing names close the read.
func (service *Service) workspaceOptions(ctx context.Context, p Principal) ([]workspaceOption, error) {
	if err := service.semanticAdmin(ctx, p); err != nil {
		return nil, err
	}
	return service.allWorkspaceOptions(ctx)
}

func (service *Service) allWorkspaceOptions(ctx context.Context) ([]workspaceOption, error) {
	catalog, err := service.Store.list(ctx, "workspace", "system", "system")
	if err != nil {
		return nil, err
	}
	options := map[ID]workspaceOption{}
	for _, raw := range catalog {
		var item workspaceOption
		if json.Unmarshal(raw, &item) != nil || !item.ID.Valid() || strings.TrimSpace(item.Name) == "" {
			return nil, ErrUnavailable
		}
		options[item.ID] = item
	}
	items, err := service.Store.list(ctx, "user", "system", "system")
	if err != nil {
		return nil, err
	}
	for _, raw := range items {
		var user User
		if json.Unmarshal(raw, &user) != nil || !user.ID.Valid() {
			return nil, ErrUnavailable
		}
		for _, member := range user.Memberships {
			if !member.Workspace.Valid() || strings.TrimSpace(member.Name) == "" {
				return nil, ErrUnavailable
			}
			if known, exists := options[member.Workspace]; exists && known.Name != member.Name {
				return nil, ErrUnavailable
			}
			if _, exists := options[member.Workspace]; !exists {
				options[member.Workspace] = workspaceOption{ID: member.Workspace, Name: member.Name}
			}
		}
	}
	result := make([]workspaceOption, 0, len(options))
	for _, item := range options {
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Name == result[j].Name {
			return result[i].ID < result[j].ID
		}
		return result[i].Name < result[j].Name
	})
	return result, nil
}

func (service *Service) memberAdmin(ctx context.Context, p Principal, workspace ID, action string) error {
	if p.EmbeddedApp != "" {
		return ErrUnauthenticated
	}
	user, _, err := service.Store.User(ctx, p.User.ID)
	if err != nil || user.AuthRevision != p.User.AuthRevision || !user.canUse(p.Workspace) {
		return ErrUnauthenticated
	}
	if user.Admin {
		return nil
	}
	if action == "weight" || workspace != p.Workspace || !user.canUse(workspace) {
		return ErrUnauthenticated
	}
	for _, membership := range user.Memberships {
		if membership.Workspace == workspace && membership.Weight == 1 {
			return nil
		}
	}
	return ErrUnauthenticated
}

func workspaceLegacyView(item workspaceOption) map[string]any {
	return map[string]any{"id": legacyBrowserID(item.ID), "name": item.Name, "create_time": item.Created}
}

func (h *Handler) legacyWorkspaceList(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	items, err := h.service.workspaceOptions(r.Context(), p)
	if err != nil {
		failure(w, err)
		return
	}
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		result = append(result, workspaceLegacyView(item))
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	okay(w, result)
}

func (h *Handler) legacyWorkspaceRead(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	id := ID(r.PathValue("id"))
	if !id.Valid() {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid workspace ID")
		return
	}
	items, err := h.service.workspaceOptions(r.Context(), p)
	if err != nil {
		failure(w, err)
		return
	}
	for _, item := range items {
		if item.ID != id {
			continue
		}
		if err := h.currentIdentity(r.Context(), p, token); err != nil {
			failure(w, err)
			return
		}
		okay(w, workspaceLegacyView(item))
		return
	}
	failure(w, ErrNotFound)
}

type membershipEdit struct {
	UIDs   []ID `json:"uid_list"`
	OID    *ID  `json:"oid"`
	Weight *int `json:"weight"`
}

func (h *Handler) legacyMemberGrant(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	var input membershipEdit
	if !readBody(w, r, &input) {
		return
	}
	workspace := p.Workspace
	if input.OID != nil {
		workspace = *input.OID
	}
	weight := 0
	if input.Weight != nil {
		weight = *input.Weight
	}
	if err := h.service.modifyMemberships(r.Context(), p, input.UIDs, workspace, weight, "grant", func(ctx context.Context) error { return h.currentIdentity(ctx, p, token) }); err != nil {
		failure(w, err)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	okay(w, nil)
}

func (h *Handler) legacyMemberRevoke(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	var input membershipEdit
	if !readBody(w, r, &input) {
		return
	}
	workspace := p.Workspace
	if input.OID != nil {
		workspace = *input.OID
	}
	if err := h.service.modifyMemberships(r.Context(), p, input.UIDs, workspace, 0, "revoke", func(ctx context.Context) error { return h.currentIdentity(ctx, p, token) }); err != nil {
		failure(w, err)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	okay(w, nil)
}

func (h *Handler) legacyMemberWeight(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	var input struct {
		UID    ID  `json:"uid"`
		OID    ID  `json:"oid"`
		Weight int `json:"weight"`
	}
	if !readBody(w, r, &input) {
		return
	}
	if err := h.service.modifyMemberships(r.Context(), p, []ID{input.UID}, input.OID, input.Weight, "weight", func(ctx context.Context) error { return h.currentIdentity(ctx, p, token) }); err != nil {
		failure(w, err)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	okay(w, nil)
}

func (service *Service) modifyMemberships(ctx context.Context, p Principal, ids []ID, workspace ID, weight int, action string, check func(context.Context) error) error {
	if err := service.memberAdmin(ctx, p, workspace, action); err != nil {
		return err
	}
	if action == "grant" && weight == 1 {
		if err := service.semanticAdmin(ctx, p); err != nil {
			return err
		}
	}
	if !workspace.Valid() || weight < 0 || weight > 1 || len(ids) == 0 || len(ids) > 100 ||
		(action != "grant" && action != "revoke" && action != "weight") {
		return ErrInvalidAccountChange
	}
	options, err := service.allWorkspaceOptions(ctx)
	if err != nil {
		return err
	}
	name := ""
	for _, option := range options {
		if option.ID == workspace {
			name = option.Name
			break
		}
	}
	if name == "" {
		return ErrNotFound
	}
	type change struct {
		key      objectKey
		body     []byte
		revision int64
	}
	changes := make([]change, 0, len(ids))
	seen := map[ID]bool{}
	for _, id := range ids {
		if !id.Valid() || seen[id] {
			return ErrInvalidAccountChange
		}
		seen[id] = true
		user, revision, err := service.Store.User(ctx, id)
		if err != nil {
			return err
		}
		if user.Admin || user.AuthRevision == math.MaxInt64 {
			return ErrUnauthenticated
		}
		index := -1
		for i, member := range user.Memberships {
			if member.Workspace == workspace {
				index = i
				break
			}
		}
		switch action {
		case "grant":
			if index >= 0 {
				return ErrConflict
			}
			user.Memberships = append(user.Memberships, Membership{Workspace: workspace, Name: name, Weight: weight})
		case "revoke":
			if index < 0 {
				return ErrNotFound
			}
			if len(user.Memberships) == 1 {
				return ErrConflict // Every enabled account needs a current workspace.
			}
			user.Memberships = append(user.Memberships[:index], user.Memberships[index+1:]...)
			if user.Workspace == workspace {
				user.Workspace = user.Memberships[0].Workspace
			}
		case "weight":
			if index < 0 {
				return ErrNotFound
			}
			user.Memberships[index].Weight = weight
		}
		user.AuthRevision++
		user.AllowLegacyTokens = false
		if err := validateUser(user); err != nil {
			return err
		}
		key := userKey(id)
		body, err := service.Store.seal(key, user)
		if err != nil {
			return err
		}
		changes = append(changes, change{key: key, body: body, revision: revision})
	}
	if err := service.memberAdmin(ctx, p, workspace, action); err != nil {
		return err
	}
	if check == nil || check(ctx) != nil {
		return ErrUnauthenticated
	}
	auditID, err := newID()
	if err != nil {
		return err
	}
	auditKey := objectKey{"management-audit", "system", "system", string(auditID)}
	audit, err := service.Store.seal(auditKey, map[string]any{"action": "workspace.member." + action,
		"workspace": workspace, "actor": p.User.ID, "users": ids, "time": time.Now().UTC()})
	if err != nil {
		return err
	}
	tx, err := service.Store.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	defer tx.Rollback()
	var catalogBody []byte
	err = tx.QueryRowContext(ctx, `SELECT body FROM chatbi_objects WHERE kind='workspace' AND scope='system' AND owner='system' AND id=?`, workspace).Scan(&catalogBody)
	if err == nil {
		var catalog workspaceOption
		if service.Store.decode(objectKey{"workspace", "system", "system", string(workspace)}, catalogBody, &catalog) != nil ||
			catalog.ID != workspace || catalog.Name != name {
			return ErrConflict
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return ErrUnavailable
	}
	for _, item := range changes {
		result, err := tx.ExecContext(ctx, `UPDATE chatbi_objects SET revision=revision+1,body=? WHERE kind=? AND scope=? AND owner=? AND id=? AND revision=?`,
			item.body, item.key.Kind, item.key.Scope, item.key.Owner, item.key.ID, item.revision)
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
