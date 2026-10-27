package chatbi

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"
)

type AccountAPIKey struct {
	ID        ID     `json:"id"`
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
	Created   int64  `json:"create_time"`
	Owner     ID     `json:"uid"`
	Status    bool   `json:"status"`
}

func apiKeyObject(id, owner ID) objectKey {
	return objectKey{"api-key", "system", string(owner), string(id)}
}

func (s *Store) apiKeyAccessIndex(access string) objectKey {
	mac := hmac.New(sha256.New, s.indexKey)
	_, _ = mac.Write([]byte(access))
	return objectKey{"api-key-access", "system", "system", hex.EncodeToString(mac.Sum(nil))}
}

func newAPIKeySecret(size int) (string, error) {
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return "", ErrUnavailable
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func (s *Store) CreateAccountAPIKey(ctx context.Context, p Principal) (ID, error) {
	if !p.User.ID.Valid() || p.EmbeddedApp != "" {
		return "", ErrUnauthenticated
	}
	id, err := newID()
	if err != nil {
		return "", err
	}
	access, err := newAPIKeySecret(16)
	if err != nil {
		return "", err
	}
	secret, err := newAPIKeySecret(32)
	if err != nil {
		return "", err
	}
	item := AccountAPIKey{ID: id, AccessKey: access, SecretKey: secret, Created: time.Now().UTC().UnixMilli(), Owner: p.User.ID, Status: true}
	key, index := apiKeyObject(id, p.User.ID), s.apiKeyAccessIndex(access)
	body, err := s.seal(key, item)
	if err != nil {
		return "", err
	}
	indexBody, err := s.seal(index, map[string]ID{"id": id, "uid": p.User.ID})
	if err != nil {
		return "", err
	}
	auditID, err := newID()
	if err != nil {
		return "", err
	}
	auditKey := objectKey{"management-audit", "system", "system", string(auditID)}
	audit, err := s.seal(auditKey, map[string]any{"action": "api-key.create", "actor": p.User.ID,
		"key": id, "time": time.Now().UTC()})
	if err != nil {
		return "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", ErrUnavailable
	}
	defer tx.Rollback()
	if err := s.verifyAPIKeyActor(ctx, tx, p); err != nil {
		return "", err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM chatbi_objects WHERE kind='api-key' AND scope='system' AND owner=?`, p.User.ID).Scan(&count); err != nil {
		return "", ErrUnavailable
	}
	if count >= 5 {
		return "", ErrConflict
	}
	for _, entry := range []struct {
		key  objectKey
		body []byte
	}{{key, body}, {index, indexBody}, {auditKey, audit}} {
		result, err := tx.ExecContext(ctx, `INSERT INTO chatbi_objects(kind,scope,owner,id,revision,body) VALUES(?,?,?,?,1,?) ON CONFLICT DO NOTHING`,
			entry.key.Kind, entry.key.Scope, entry.key.Owner, entry.key.ID, entry.body)
		if err != nil {
			return "", ErrUnavailable
		}
		rows, err := result.RowsAffected()
		if err != nil || rows != 1 {
			return "", ErrConflict
		}
	}
	if tx.Commit() != nil {
		return "", ErrUnavailable
	}
	return id, nil
}

func (s *Store) ListAccountAPIKeys(ctx context.Context, p Principal) ([]AccountAPIKey, error) {
	if !p.User.ID.Valid() || !p.User.canUse(p.Workspace) || p.EmbeddedApp != "" {
		return nil, ErrUnauthenticated
	}
	raw, err := s.list(ctx, "api-key", "system", string(p.User.ID))
	if err != nil {
		return nil, err
	}
	result := make([]AccountAPIKey, 0, len(raw))
	for _, body := range raw {
		var item AccountAPIKey
		if json.Unmarshal(body, &item) != nil || !item.ID.Valid() || item.Owner != p.User.ID || item.AccessKey == "" || item.SecretKey == "" {
			return nil, ErrUnavailable
		}
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Created > result[j].Created })
	return result, nil
}

func (s *Store) ChangeAccountAPIKey(ctx context.Context, p Principal, id ID, status *bool) error {
	if !id.Valid() || !p.User.ID.Valid() || p.EmbeddedApp != "" {
		return ErrInvalidAccountChange
	}
	key := apiKeyObject(id, p.User.ID)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	defer tx.Rollback()
	if err := s.verifyAPIKeyActor(ctx, tx, p); err != nil {
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
	var item AccountAPIKey
	if s.decode(key, oldBody, &item) != nil || item.ID != id || item.Owner != p.User.ID || item.AccessKey == "" || item.SecretKey == "" {
		return ErrUnavailable
	}
	if status != nil && item.Status == *status {
		return nil
	}
	auditID, err := newID()
	if err != nil {
		return err
	}
	auditKey := objectKey{"management-audit", "system", "system", string(auditID)}
	action := "api-key.delete"
	if status != nil {
		action = "api-key.status"
	}
	audit, err := s.seal(auditKey, map[string]any{"action": action, "actor": p.User.ID,
		"key": id, "time": time.Now().UTC()})
	if err != nil {
		return err
	}
	if status == nil {
		index := s.apiKeyAccessIndex(item.AccessKey)
		var indexBody []byte
		if err := tx.QueryRowContext(ctx, `SELECT body FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=?`,
			index.Kind, index.Scope, index.Owner, index.ID).Scan(&indexBody); err != nil {
			return ErrUnavailable
		}
		var claim map[string]ID
		if s.decode(index, indexBody, &claim) != nil || claim["id"] != id || claim["uid"] != p.User.ID {
			return ErrUnavailable
		}
		for _, target := range []objectKey{key, index} {
			result, err := tx.ExecContext(ctx, `DELETE FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=?`,
				target.Kind, target.Scope, target.Owner, target.ID)
			if err != nil {
				return ErrUnavailable
			}
			rows, err := result.RowsAffected()
			if err != nil || rows != 1 {
				return ErrUnavailable
			}
		}
	} else {
		item.Status = *status
		body, err := s.seal(key, item)
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE chatbi_objects SET revision=revision+1,body=? WHERE kind=? AND scope=? AND owner=? AND id=? AND revision=?`,
			body, key.Kind, key.Scope, key.Owner, key.ID, revision)
		if err != nil {
			return ErrUnavailable
		}
		rows, err := result.RowsAffected()
		if err != nil || rows != 1 {
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
