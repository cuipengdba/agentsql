package chatbi

import (
	"context"
	"crypto/hmac"
	"crypto/md5" // Imported SQLBot password digests only; new passwords use bcrypt.
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var ErrUnauthenticated = errors.New("invalid or unavailable ChatBI identity")

// ID accepts decimal strings and JSON integers without a float64 round trip.
type ID string

func (id ID) Valid() bool {
	value, err := strconv.ParseInt(string(id), 10, 64)
	return err == nil && value > 0 && strconv.FormatInt(value, 10) == string(id)
}
func (id *ID) UnmarshalJSON(raw []byte) error {
	var text string
	if len(raw) > 0 && raw[0] == '"' {
		if json.Unmarshal(raw, &text) != nil {
			return errors.New("invalid ID")
		}
	} else {
		text = string(raw)
	}
	v := ID(text)
	if !v.Valid() {
		return errors.New("ID must be a positive decimal integer")
	}
	*id = v
	return nil
}

type Membership struct {
	Workspace ID     `json:"oid"`
	Name      string `json:"name"`
	Weight    int    `json:"weight"`
}

type User struct {
	ID                ID           `json:"id"`
	Account           string       `json:"account"`
	Name              string       `json:"name"`
	Workspace         ID           `json:"oid"`
	Language          string       `json:"language"`
	Status            int          `json:"status"`
	Origin            int          `json:"origin"`
	Admin             bool         `json:"isAdmin"`
	Memberships       []Membership `json:"memberships"`
	PasswordHash      string       `json:"password_hash"`
	PasswordFormat    string       `json:"password_format"`
	AuthRevision      int64        `json:"auth_revision"`
	AllowLegacyTokens bool         `json:"allow_legacy_tokens"`
}

func (user User) canUse(workspace ID) bool {
	if user.Status != 1 || !workspace.Valid() {
		return false
	}
	for _, member := range user.Memberships {
		if member.Workspace == workspace {
			return true
		}
	}
	return false
}

type Principal struct {
	User             User
	Workspace        ID
	EmbeddedApp      ID
	EmbeddedRevision int64
}

func userKey(id ID) objectKey { return objectKey{"user", "system", "system", string(id)} }

func (s *Store) accountKey(account string) objectKey {
	mac := hmac.New(sha256.New, s.indexKey)
	_, _ = mac.Write([]byte(account))
	return objectKey{"account", "system", "system", hex.EncodeToString(mac.Sum(nil))}
}

// CreateUser atomically reserves the external account and ID. Importing a
// collision must fail instead of silently remapping an existing identity.
func validateUser(user User) error {
	if !user.ID.Valid() || user.Account == "" || len(user.Account) > 100 || user.AuthRevision < 1 || !user.Workspace.Valid() ||
		(user.Status != 0 && user.Status != 1) || len(user.Memberships) == 0 {
		return ErrUnauthenticated
	}
	seen := map[ID]bool{}
	for _, member := range user.Memberships {
		if !member.Workspace.Valid() || seen[member.Workspace] {
			return ErrUnauthenticated
		}
		seen[member.Workspace] = true
	}
	if !seen[user.Workspace] {
		return ErrUnauthenticated
	}
	switch user.PasswordFormat {
	case "bcrypt":
		if _, err := bcrypt.Cost([]byte(user.PasswordHash)); err != nil {
			return ErrUnauthenticated
		}
	case "sqlbot-md5":
		raw, err := hex.DecodeString(user.PasswordHash)
		if err != nil || len(raw) != 16 {
			return ErrUnauthenticated
		}
	default:
		return ErrUnauthenticated
	}
	return nil
}

func (s *Store) CreateUser(ctx context.Context, user User) error {
	if err := validateUser(user); err != nil {
		return err
	}
	accountKey := s.accountKey(user.Account)
	userBody, err := s.seal(userKey(user.ID), user)
	if err != nil {
		return err
	}
	accountBody, err := s.seal(accountKey, string(user.ID))
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	defer tx.Rollback()
	for _, entry := range []struct {
		key  objectKey
		body []byte
	}{{userKey(user.ID), userBody}, {accountKey, accountBody}} {
		result, err := tx.ExecContext(ctx, `INSERT INTO chatbi_objects(kind,scope,owner,id,revision,body) VALUES(?,?,?,?,1,?) ON CONFLICT DO NOTHING`, entry.key.Kind, entry.key.Scope, entry.key.Owner, entry.key.ID, entry.body)
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
	}
	if tx.Commit() != nil {
		return ErrUnavailable
	}
	return nil
}

func (s *Store) User(ctx context.Context, id ID) (User, int64, error) {
	var user User
	revision, err := s.get(ctx, userKey(id), &user)
	if err != nil {
		return User{}, 0, err
	}
	if user.ID != id {
		return User{}, 0, ErrUnavailable
	}
	return user, revision, nil
}

func HashPassword(password string) (string, error) {
	if len(password) < 12 || len(password) > 72 {
		return "", errors.New("password must contain 12 to 72 bytes")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hash), err
}

type Identity struct {
	store      *Store
	signingKey []byte
	privateKey *rsa.PrivateKey
	now        func() time.Time
}

func NewIdentity(store *Store, signingKey, loginPEM []byte) (*Identity, error) {
	if store == nil || len(signingKey) < 32 {
		return nil, errors.New("ChatBI requires persistent identities and a signing key of at least 32 bytes")
	}
	identity := &Identity{store: store, signingKey: append([]byte(nil), signingKey...), now: time.Now}
	key, err := parseLoginKey(loginPEM)
	if err != nil {
		return nil, err
	}
	identity.privateKey = key
	return identity, nil
}

func parseLoginKey(loginPEM []byte) (*rsa.PrivateKey, error) {
	if len(loginPEM) > 0 {
		block, rest := pem.Decode(loginPEM)
		if block == nil || len(strings.TrimSpace(string(rest))) != 0 {
			return nil, errors.New("invalid login key")
		}
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
		}
		rsaKey, ok := key.(*rsa.PrivateKey)
		if err != nil || !ok || rsaKey.N.BitLen() < 2048 || rsaKey.Validate() != nil {
			return nil, errors.New("login key must be an RSA private key of at least 2048 bits")
		}
		return rsaKey, nil
	}
	return nil, nil
}

func (identity *Identity) DecryptLogin(encoded string) (string, error) {
	if identity.privateKey == nil || len(encoded) > 2048 {
		return "", ErrUnauthenticated
	}
	ciphertext, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(ciphertext) != identity.privateKey.Size() {
		return "", ErrUnauthenticated
	}
	plain, err := rsa.DecryptPKCS1v15(rand.Reader, identity.privateKey, ciphertext)
	if err != nil || len(plain) == 0 || len(plain) > 512 {
		return "", ErrUnauthenticated
	}
	return string(plain), nil
}

func (identity *Identity) Login(ctx context.Context, account, password string) (User, string, error) {
	if len(account) > 100 || len(password) > 512 || password == "" {
		return User{}, "", ErrUnauthenticated
	}
	var id ID
	if _, err := identity.store.get(ctx, identity.store.accountKey(account), &id); err != nil {
		return User{}, "", ErrUnauthenticated
	}
	user, revision, err := identity.store.User(ctx, id)
	if err != nil || user.Account != account || user.Origin != 0 || !user.canUse(user.Workspace) {
		return User{}, "", ErrUnauthenticated
	}
	valid := false
	switch user.PasswordFormat {
	case "bcrypt":
		valid = bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) == nil
	case "sqlbot-md5":
		digest := md5.Sum([]byte(password))
		expected, decodeErr := hex.DecodeString(user.PasswordHash)
		valid = decodeErr == nil && subtle.ConstantTimeCompare(digest[:], expected) == 1
	}
	if !valid {
		return User{}, "", ErrUnauthenticated
	}
	// Preserve the user's password while replacing the weak imported digest.
	if user.PasswordFormat == "sqlbot-md5" && len(password) <= 72 {
		hash, hashErr := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if hashErr != nil {
			return User{}, "", ErrUnavailable
		}
		user.PasswordHash, user.PasswordFormat = string(hash), "bcrypt"
		if err := identity.store.put(ctx, userKey(id), user, revision); err != nil {
			return User{}, "", err
		}
	}
	token, err := identity.issue(user, user.Workspace)
	return user, token, err
}

type tokenClaims struct {
	ID        ID     `json:"id"`
	Account   string `json:"account"`
	Workspace ID     `json:"oid"`
	Expires   int64  `json:"exp"`
	Issued    int64  `json:"iat,omitempty"`
	Revision  int64  `json:"asl_rev,omitempty"`
	Nonce     string `json:"jti,omitempty"`
}

func (identity *Identity) issue(user User, workspace ID) (string, error) {
	nonce := make([]byte, 24)
	if _, err := rand.Read(nonce); err != nil {
		return "", ErrUnavailable
	}
	now := identity.now()
	claims := tokenClaims{user.ID, user.Account, workspace, now.Add(8 * 24 * time.Hour).Unix(), now.Unix(), user.AuthRevision, base64.RawURLEncoding.EncodeToString(nonce)}
	body, err := json.Marshal(claims)
	if err != nil {
		return "", ErrUnavailable
	}
	part := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`)) + "." + base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, identity.signingKey)
	_, _ = mac.Write([]byte(part))
	return part + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func revokedKey(token string) objectKey {
	sum := sha256.Sum256([]byte(token))
	return objectKey{"revoked-token", "system", "system", hex.EncodeToString(sum[:])}
}

func (identity *Identity) Verify(ctx context.Context, token string) (Principal, error) {
	deny := func() (Principal, error) { return Principal{}, ErrUnauthenticated }
	if len(token) > 8192 {
		return deny()
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return deny()
	}
	header, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return deny()
	}
	var h struct {
		Algorithm string   `json:"alg"`
		Type      string   `json:"typ"`
		Critical  []string `json:"crit"`
	}
	if json.Unmarshal(header, &h) != nil || h.Algorithm != "HS256" || h.Type != "JWT" || len(h.Critical) != 0 {
		return deny()
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return deny()
	}
	mac := hmac.New(sha256.New, identity.signingKey)
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return deny()
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return deny()
	}
	var claims tokenClaims
	if json.Unmarshal(body, &claims) != nil {
		return deny()
	}
	now := identity.now().Unix()
	if claims.Expires <= now || claims.Expires > now+int64((8*24*time.Hour).Seconds()) || claims.Issued > now+5 || claims.Issued < 0 {
		return deny()
	}
	user, _, err := identity.store.User(ctx, claims.ID)
	if err != nil || user.Account != claims.Account || !user.canUse(claims.Workspace) {
		return deny()
	}
	if claims.Revision == 0 {
		if !user.AllowLegacyTokens {
			return deny()
		}
	} else if claims.Revision != user.AuthRevision || claims.Nonce == "" || claims.Issued == 0 {
		return deny()
	}
	var revoked int64
	_, err = identity.store.get(ctx, revokedKey(token), &revoked)
	if !errors.Is(err, ErrNotFound) {
		return deny()
	}
	return Principal{User: user, Workspace: claims.Workspace}, nil
}

func (identity *Identity) Logout(ctx context.Context, token string) error {
	if _, err := identity.Verify(ctx, token); err != nil {
		return err
	}
	err := identity.store.put(ctx, revokedKey(token), identity.now().Unix(), 0)
	if errors.Is(err, ErrConflict) {
		return nil
	}
	return err
}
