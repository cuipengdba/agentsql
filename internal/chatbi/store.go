// Package chatbi implements AgentSQL's native data Q&A service. Its identity
// namespace is separate from the gateway's management and Agent credentials.
package chatbi

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound    = errors.New("ChatBI object not found")
	ErrConflict    = errors.New("ChatBI object revision conflict")
	ErrUnavailable = errors.New("ChatBI storage unavailable")
)

// Store persists encrypted documents. Scope and owner are part of both the
// lookup key and AEAD additional data: moving ciphertext cannot change access.
type Store struct {
	db       *sql.DB
	aead     cipher.AEAD
	indexKey []byte
}

type objectKey struct{ Kind, Scope, Owner, ID string }

func (key objectKey) valid() bool {
	for _, value := range []string{key.Kind, key.Scope, key.Owner, key.ID} {
		if value == "" || len(value) > 256 || strings.ContainsRune(value, '\x00') {
			return false
		}
	}
	return true
}

func (key objectKey) aad() []byte {
	return []byte("agentsql:chatbi:v1\x00" + key.Kind + "\x00" + key.Scope + "\x00" + key.Owner + "\x00" + key.ID)
}

func OpenStore(ctx context.Context, path string, secret []byte) (*Store, error) {
	if ctx == nil || len(secret) != 32 || path == "" || path == ":memory:" || strings.ContainsAny(path, "?\x00") {
		return nil, errors.New("ChatBI requires a file path and a 32-byte encryption secret")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, ErrUnavailable
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
		return nil, ErrUnavailable
	}
	file, err := os.OpenFile(abs, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, ErrUnavailable
	}
	if err = file.Close(); err != nil {
		return nil, ErrUnavailable
	}
	block, err := aes.NewCipher(secret)
	if err != nil {
		return nil, ErrUnavailable
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrUnavailable
	}
	db, err := sql.Open("sqlite", abs)
	if err != nil {
		return nil, ErrUnavailable
	}
	db.SetMaxOpenConns(1)
	fail := func() (*Store, error) { _ = db.Close(); return nil, ErrUnavailable }
	for _, statement := range []string{
		"PRAGMA busy_timeout = 5000", "PRAGMA journal_mode = WAL", "PRAGMA synchronous = FULL", "PRAGMA foreign_keys = ON",
		`CREATE TABLE IF NOT EXISTS chatbi_objects (
		 kind TEXT NOT NULL, scope TEXT NOT NULL, owner TEXT NOT NULL, id TEXT NOT NULL,
		 revision INTEGER NOT NULL CHECK (revision > 0), body BLOB NOT NULL,
		 PRIMARY KEY (kind, scope, owner, id))`,
	} {
		if _, err = db.ExecContext(ctx, statement); err != nil {
			return fail()
		}
	}
	indexMAC := hmac.New(sha256.New, secret)
	_, _ = indexMAC.Write([]byte("agentsql:chatbi:account-index:v1"))
	s := &Store{db: db, aead: aead, indexKey: indexMAC.Sum(nil)}
	// A durable encrypted sentinel rejects a wrong key even when no business
	// object has been loaded yet. Never silently reinitialize an existing store.
	key := objectKey{"system", "system", "system", "key-check"}
	var sentinel string
	_, err = s.get(ctx, key, &sentinel)
	if errors.Is(err, ErrNotFound) {
		err = s.put(ctx, key, "agentsql-chatbi-store-v1", 0)
		if errors.Is(err, ErrConflict) {
			_, err = s.get(ctx, key, &sentinel)
		} else {
			sentinel = "agentsql-chatbi-store-v1"
		}
	}
	if err != nil || sentinel != "agentsql-chatbi-store-v1" {
		return fail()
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func controlNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

// Management transactions use the encrypted control-plane store, never a
// business database connection. Keep the database/sql capability here.
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
		if controlNoRows(err) {
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

func (s *Store) seal(key objectKey, value any) ([]byte, error) {
	if !key.valid() {
		return nil, ErrUnavailable
	}
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > 16<<20 {
		return nil, ErrUnavailable
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, ErrUnavailable
	}
	return s.aead.Seal(nonce, nonce, raw, key.aad()), nil
}

func (s *Store) decode(key objectKey, body []byte, value any) error {
	if len(body) < s.aead.NonceSize()+s.aead.Overhead() {
		return ErrUnavailable
	}
	raw, err := s.aead.Open(nil, body[:s.aead.NonceSize()], body[s.aead.NonceSize():], key.aad())
	if err != nil || json.Unmarshal(raw, value) != nil {
		return ErrUnavailable
	}
	return nil
}

func (s *Store) get(ctx context.Context, key objectKey, value any) (int64, error) {
	revision, _, err := s.getWithDigest(ctx, key, value)
	return revision, err
}

// getWithDigest identifies this exact encrypted revision. An object deleted
// and recreated at revision one gets a fresh AEAD nonce and therefore a new
// digest even if its plaintext happens to be identical.
func (s *Store) getWithDigest(ctx context.Context, key objectKey, value any) (int64, [32]byte, error) {
	if !key.valid() {
		return 0, [32]byte{}, ErrNotFound
	}
	var body []byte
	var revision int64
	err := s.db.QueryRowContext(ctx, `SELECT revision, body FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=?`, key.Kind, key.Scope, key.Owner, key.ID).Scan(&revision, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, [32]byte{}, ErrNotFound
	}
	if err != nil {
		return 0, [32]byte{}, ErrUnavailable
	}
	if err = s.decode(key, body, value); err != nil {
		return 0, [32]byte{}, err
	}
	return revision, sha256.Sum256(body), nil
}

// put is insert-only at revision zero and compare-and-swap otherwise.
func (s *Store) put(ctx context.Context, key objectKey, value any, revision int64) error {
	body, err := s.seal(key, value)
	if err != nil || revision < 0 {
		return ErrUnavailable
	}
	var result sql.Result
	if revision == 0 {
		result, err = s.db.ExecContext(ctx, `INSERT INTO chatbi_objects(kind,scope,owner,id,revision,body) VALUES(?,?,?,?,1,?) ON CONFLICT DO NOTHING`, key.Kind, key.Scope, key.Owner, key.ID, body)
	} else {
		result, err = s.db.ExecContext(ctx, `UPDATE chatbi_objects SET revision=revision+1,body=? WHERE kind=? AND scope=? AND owner=? AND id=? AND revision=?`, body, key.Kind, key.Scope, key.Owner, key.ID, revision)
	}
	if err != nil {
		return ErrUnavailable
	}
	n, err := result.RowsAffected()
	if err != nil {
		return ErrUnavailable
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}

func (s *Store) list(ctx context.Context, kind, scope, owner string) ([]json.RawMessage, error) {
	if !(objectKey{kind, scope, owner, "list"}).valid() {
		return nil, ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,body FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? ORDER BY id LIMIT 10001`, kind, scope, owner)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer rows.Close()
	result := make([]json.RawMessage, 0)
	total := 0
	for rows.Next() {
		var id string
		var body []byte
		var raw json.RawMessage
		if err := rows.Scan(&id, &body); err != nil {
			return nil, ErrUnavailable
		}
		total += len(body)
		if total > 64<<20 {
			return nil, errors.New("ChatBI listing byte limit exceeded")
		}
		if err := s.decode(objectKey{kind, scope, owner, id}, body, &raw); err != nil {
			return nil, err
		}
		result = append(result, raw)
		if len(result) > 10000 {
			return nil, fmt.Errorf("ChatBI listing limit exceeded")
		}
	}
	if rows.Err() != nil {
		return nil, ErrUnavailable
	}
	return result, nil
}

func (s *Store) delete(ctx context.Context, key objectKey, revision int64) error {
	if !key.valid() || revision < 1 {
		return ErrNotFound
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=? AND revision=?`, key.Kind, key.Scope, key.Owner, key.ID, revision)
	if err != nil {
		return ErrUnavailable
	}
	n, err := result.RowsAffected()
	if err != nil {
		return ErrUnavailable
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}
