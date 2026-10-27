package chatbi

import (
	"context"
	"math"
	"net/http"
	"net/mail"
	"strings"
	"time"
)

type editUserRequest struct {
	ID              ID     `json:"id"`
	Account         string `json:"account"`
	Name            string `json:"name"`
	Email           string `json:"email"`
	Status          *int   `json:"status"`
	Origin          *int   `json:"origin"`
	Workspaces      []ID   `json:"oid_list"`
	SystemVariables []any  `json:"system_variables"`
}

func (h *Handler) legacyUserUpdate(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	var input editUserRequest
	if !readBody(w, r, &input) {
		return
	}
	input.Name, input.Email = strings.TrimSpace(input.Name), strings.TrimSpace(input.Email)
	if !input.ID.Valid() || input.Name == "" || len(input.Name) > 100 || len(input.Email) > 100 ||
		len(input.Workspaces) == 0 || len(input.Workspaces) > 32 || len(input.SystemVariables) != 0 ||
		input.Status != nil && *input.Status != 0 && *input.Status != 1 {
		failure(w, ErrInvalidAccountChange)
		return
	}
	if input.Email != "" {
		address, err := mail.ParseAddress(input.Email)
		if err != nil || address.Address != input.Email {
			failure(w, ErrInvalidAccountChange)
			return
		}
	}
	user, revision, err := h.service.Store.User(r.Context(), input.ID)
	if err != nil {
		failure(w, err)
		return
	}
	if user.Admin || user.ID == p.User.ID || user.AuthRevision == math.MaxInt64 {
		failure(w, ErrUnauthenticated)
		return
	}
	if input.Account != user.Account || input.Origin != nil && *input.Origin != user.Origin {
		failure(w, ErrInvalidAccountChange)
		return
	}
	options, err := h.service.allWorkspaceOptions(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	names := make(map[ID]string, len(options))
	for _, option := range options {
		names[option.ID] = option.Name
	}
	weights := make(map[ID]int, len(user.Memberships))
	for _, membership := range user.Memberships {
		weights[membership.Workspace] = membership.Weight
	}
	memberships := make([]Membership, 0, len(input.Workspaces))
	seen := map[ID]bool{}
	for _, workspace := range input.Workspaces {
		if !workspace.Valid() || seen[workspace] || names[workspace] == "" {
			failure(w, ErrInvalidAccountChange)
			return
		}
		seen[workspace] = true
		memberships = append(memberships, Membership{Workspace: workspace, Name: names[workspace], Weight: weights[workspace]})
	}
	user.Name, user.Email, user.Memberships = input.Name, input.Email, memberships
	if !seen[user.Workspace] {
		user.Workspace = memberships[0].Workspace
	}
	if input.Status != nil {
		user.Status = *input.Status
	}
	user.AuthRevision++
	user.AllowLegacyTokens = false
	if err := h.service.Store.UpdateUserManaged(r.Context(), user, revision, p); err != nil {
		failure(w, err)
		return
	}
	okay(w, legacyUserView(user))
}

func (s *Store) UpdateUserManaged(ctx context.Context, user User, revision int64, p Principal) error {
	if err := validateUser(user); err != nil || revision < 1 || !p.User.ID.Valid() || p.EmbeddedApp != "" || user.ID == p.User.ID {
		return ErrInvalidAccountChange
	}
	key := userKey(user.ID)
	body, err := s.seal(key, user)
	if err != nil {
		return err
	}
	auditID, err := newID()
	if err != nil {
		return err
	}
	auditKey := objectKey{"management-audit", "system", "system", string(auditID)}
	audit, err := s.seal(auditKey, map[string]any{"action": "user.update", "actor": p.User.ID,
		"user": user.ID, "time": time.Now().UTC()})
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	defer tx.Rollback()
	var actorBody []byte
	if err := tx.QueryRowContext(ctx, `SELECT body FROM chatbi_objects WHERE kind='user' AND scope='system' AND owner='system' AND id=?`, p.User.ID).Scan(&actorBody); err != nil {
		return ErrUnauthenticated
	}
	var actor User
	if err := s.decode(userKey(p.User.ID), actorBody, &actor); err != nil || actor.ID != p.User.ID ||
		actor.AuthRevision != p.User.AuthRevision || !actor.Admin || !actor.canUse(p.Workspace) {
		return ErrUnauthenticated
	}
	result, err := tx.ExecContext(ctx, `UPDATE chatbi_objects SET revision=revision+1,body=? WHERE kind=? AND scope=? AND owner=? AND id=? AND revision=?`,
		body, key.Kind, key.Scope, key.Owner, key.ID, revision)
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
