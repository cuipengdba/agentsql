package chatbi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/mail"
	"strings"
	"time"
)

type newUserRequest struct {
	Account         string            `json:"account"`
	Name            string            `json:"name"`
	Email           string            `json:"email"`
	Status          *int              `json:"status"`
	Origin          *int              `json:"origin"`
	Workspaces      []ID              `json:"oid_list"`
	InitialPassword string            `json:"initial_password"`
	SystemVariables []json.RawMessage `json:"system_variables"`
}

func (h *Handler) legacyUserCreate(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	var input newUserRequest
	if !readBody(w, r, &input) {
		return
	}
	input.Account = strings.TrimSpace(input.Account)
	input.Name = strings.TrimSpace(input.Name)
	input.Email = strings.TrimSpace(input.Email)
	if input.Account == "" || len(input.Account) > 100 || input.Name == "" || len(input.Name) > 100 ||
		len(input.Email) > 100 || len(input.Workspaces) == 0 || len(input.Workspaces) > 32 ||
		len(input.SystemVariables) != 0 || input.Origin != nil && *input.Origin != 0 ||
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
	password := input.InitialPassword
	if password == "" {
		password = h.initialUserPassword
	}
	hash, err := HashPassword(password)
	if err != nil {
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
	memberships := make([]Membership, 0, len(input.Workspaces))
	seen := map[ID]bool{}
	for _, workspace := range input.Workspaces {
		if !workspace.Valid() || seen[workspace] || names[workspace] == "" {
			failure(w, ErrInvalidAccountChange)
			return
		}
		seen[workspace] = true
		memberships = append(memberships, Membership{Workspace: workspace, Name: names[workspace]})
	}
	id, err := newID()
	if err != nil {
		failure(w, err)
		return
	}
	status := 1
	if input.Status != nil {
		status = *input.Status
	}
	user := User{ID: id, Account: input.Account, Name: input.Name, Email: input.Email,
		Workspace: input.Workspaces[0], Language: "zh-CN", Status: status, Origin: 0,
		Memberships: memberships, PasswordHash: hash, PasswordFormat: "bcrypt",
		AuthRevision: 1, Created: time.Now().UTC().UnixMilli()}
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	if err := h.service.Store.CreateUserManaged(r.Context(), user, p); err != nil {
		failure(w, err)
		return
	}
	okay(w, legacyUserView(user))
}

// Managed creation reserves the account and writes the management audit in the
// same transaction; an audit failure never leaves a login-capable account.
func (s *Store) CreateUserManaged(ctx context.Context, user User, p Principal) error {
	if err := validateUser(user); err != nil || !p.User.ID.Valid() || p.EmbeddedApp != "" {
		return ErrInvalidAccountChange
	}
	account := s.accountKey(user.Account)
	userBody, err := s.seal(userKey(user.ID), user)
	if err != nil {
		return err
	}
	accountBody, err := s.seal(account, string(user.ID))
	if err != nil {
		return err
	}
	auditID, err := newID()
	if err != nil {
		return err
	}
	auditKey := objectKey{"management-audit", "system", "system", string(auditID)}
	audit, err := s.seal(auditKey, map[string]any{"action": "user.create", "actor": p.User.ID,
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
	for _, item := range []struct {
		key  objectKey
		body []byte
	}{{userKey(user.ID), userBody}, {account, accountBody}} {
		result, err := tx.ExecContext(ctx, `INSERT INTO chatbi_objects(kind,scope,owner,id,revision,body) VALUES(?,?,?,?,1,?) ON CONFLICT DO NOTHING`,
			item.key.Kind, item.key.Scope, item.key.Owner, item.key.ID, item.body)
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
