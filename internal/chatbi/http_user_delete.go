package chatbi

import (
	"context"
	"net/http"
	"time"
)

func (h *Handler) legacyUserDeleteOne(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	h.deleteManagedUsers(w, r, p, token, []ID{ID(r.PathValue("id"))})
}

func (h *Handler) legacyUserDeleteBatch(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	var ids []ID
	if !readBody(w, r, &ids) {
		return
	}
	h.deleteManagedUsers(w, r, p, token, ids)
}

func (h *Handler) deleteManagedUsers(w http.ResponseWriter, r *http.Request, p Principal, token string, ids []ID) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	if err := h.service.Store.DeleteUsersManaged(r.Context(), ids, p); err != nil {
		failure(w, err)
		return
	}
	okay(w, nil)
}

// DeleteUsersManaged removes the login identity and its scoped private data
// together with the management audit. The old token then has no backing user.
func (s *Store) DeleteUsersManaged(ctx context.Context, ids []ID, p Principal) error {
	if len(ids) == 0 || len(ids) > 100 || !p.User.ID.Valid() || p.EmbeddedApp != "" {
		return ErrInvalidAccountChange
	}
	seen := make(map[ID]bool, len(ids))
	for _, id := range ids {
		if !id.Valid() || id == p.User.ID || seen[id] {
			return ErrInvalidAccountChange
		}
		seen[id] = true
	}
	auditID, err := newID()
	if err != nil {
		return err
	}
	auditKey := objectKey{"management-audit", "system", "system", string(auditID)}
	audit, err := s.seal(auditKey, map[string]any{"action": "user.delete", "actor": p.User.ID,
		"users": ids, "time": time.Now().UTC()})
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
		var userBody []byte
		var revision int64
		err := tx.QueryRowContext(ctx, `SELECT revision,body FROM chatbi_objects WHERE kind='user' AND scope='system' AND owner='system' AND id=?`, id).Scan(&revision, &userBody)
		if controlNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return ErrUnavailable
		}
		var user User
		if err := s.decode(userKey(id), userBody, &user); err != nil || user.ID != id || user.Account == "" {
			return ErrUnavailable
		}
		if user.Admin || user.ID == "1" {
			return ErrUnauthenticated
		}
		accountKey := s.accountKey(user.Account)
		var accountBody []byte
		err = tx.QueryRowContext(ctx, `SELECT body FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=?`,
			accountKey.Kind, accountKey.Scope, accountKey.Owner, accountKey.ID).Scan(&accountBody)
		if err != nil {
			return ErrUnavailable
		}
		var accountID string
		if err := s.decode(accountKey, accountBody, &accountID); err != nil || accountID != string(id) {
			return ErrUnavailable
		}
		// Managed imports own resources in the gateway as well as this store.
		// Their retirement is a separate compensated workflow; dropping only the
		// local receipt would orphan remote tables and connector credentials.
		var managedReceipts int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM chatbi_objects WHERE owner=? AND kind='managed-file-receipt'`, string(id)).Scan(&managedReceipts); err != nil {
			return ErrUnavailable
		}
		if managedReceipts != 0 {
			return ErrConflict
		}
		rows, err := tx.QueryContext(ctx, `SELECT scope,id,body FROM chatbi_objects WHERE owner=? AND kind='binding'`, string(id))
		if err != nil {
			return ErrUnavailable
		}
		for rows.Next() {
			var scope, bindingID string
			var body []byte
			if err := rows.Scan(&scope, &bindingID, &body); err != nil {
				rows.Close()
				return ErrUnavailable
			}
			var binding Binding
			if err := s.decode(objectKey{"binding", scope, string(id), bindingID}, body, &binding); err != nil {
				rows.Close()
				return ErrUnavailable
			}
			if binding.ManagedFileID != "" {
				rows.Close()
				return ErrConflict
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return ErrUnavailable
		}
		rows.Close()
		keyRows, err := tx.QueryContext(ctx, `SELECT id,body FROM chatbi_objects WHERE kind='api-key' AND scope='system' AND owner=?`, string(id))
		if err != nil {
			return ErrUnavailable
		}
		var accountKeys []AccountAPIKey
		for keyRows.Next() {
			var keyID string
			var keyBody []byte
			if err := keyRows.Scan(&keyID, &keyBody); err != nil {
				keyRows.Close()
				return ErrUnavailable
			}
			var item AccountAPIKey
			if s.decode(apiKeyObject(ID(keyID), id), keyBody, &item) != nil || item.ID != ID(keyID) || item.Owner != id {
				keyRows.Close()
				return ErrUnavailable
			}
			accountKeys = append(accountKeys, item)
		}
		if err := keyRows.Err(); err != nil {
			keyRows.Close()
			return ErrUnavailable
		}
		keyRows.Close()
		for _, item := range accountKeys {
			index := s.apiKeyAccessIndex(item.AccessKey)
			var indexBody []byte
			if err := tx.QueryRowContext(ctx, `SELECT body FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=?`, index.Kind, index.Scope, index.Owner, index.ID).Scan(&indexBody); err != nil {
				return ErrUnavailable
			}
			var claim map[string]ID
			if s.decode(index, indexBody, &claim) != nil || claim["id"] != item.ID || claim["uid"] != id {
				return ErrUnavailable
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=?`, index.Kind, index.Scope, index.Owner, index.ID); err != nil {
				return ErrUnavailable
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM chatbi_objects WHERE kind='api-key' AND scope='system' AND owner=?`, string(id)); err != nil {
			return ErrUnavailable
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM chatbi_objects WHERE owner=? AND scope!='system'`, string(id)); err != nil {
			return ErrUnavailable
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM chatbi_objects WHERE kind='user' AND scope='system' AND owner='system' AND id=? AND revision=?`, id, revision)
		if err != nil {
			return ErrUnavailable
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return ErrConflict
		}
		result, err = tx.ExecContext(ctx, `DELETE FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=? AND body=?`,
			accountKey.Kind, accountKey.Scope, accountKey.Owner, accountKey.ID, accountBody)
		if err != nil {
			return ErrUnavailable
		}
		count, err = result.RowsAffected()
		if err != nil || count != 1 {
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
