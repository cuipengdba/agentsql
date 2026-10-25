package chatbi

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(context.Background(), filepath.Join(t.TempDir(), "chat.db"), []byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func testUser(t *testing.T, id, account string) User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("Synthetic-password-1"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return User{ID: ID(id), Account: account, Name: account, Workspace: "1", Language: "zh-CN", Status: 1, Memberships: []Membership{{Workspace: "1", Name: "One"}}, PasswordHash: string(hash), PasswordFormat: "bcrypt", AuthRevision: 1}
}

func testIdentity(t *testing.T, s *Store) *Identity {
	t.Helper()
	identity, err := NewIdentity(s, []byte(strings.Repeat("j", 32)), nil)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func TestIdentityRevocationAndLiveMembership(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	identity := testIdentity(t, s)
	user := testUser(t, "7518977644151443457", "analyst")
	if err := s.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	_, token, err := identity.Login(ctx, user.Account, "Synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	p, err := identity.Verify(ctx, token)
	if err != nil || p.User.ID != user.ID || p.Workspace != "1" {
		t.Fatalf("verify: %v %+v", err, p)
	}
	if _, _, err := identity.Login(ctx, user.Account, "incorrect"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("wrong password accepted")
	}
	stored, revision, _ := s.User(ctx, user.ID)
	stored.Memberships = []Membership{{Workspace: "2", Name: "Two"}}
	stored.Workspace = "2"
	if err := s.put(ctx, userKey(user.ID), stored, revision); err != nil {
		t.Fatal(err)
	}
	if _, err := identity.Verify(ctx, token); err == nil {
		t.Fatal("removed workspace accepted")
	}
	_, token, err = identity.Login(ctx, user.Account, "Synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := identity.Logout(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err := identity.Verify(ctx, token); err == nil {
		t.Fatal("revoked token accepted")
	}
	_, token, _ = identity.Login(ctx, user.Account, "Synthetic-password-1")
	stored, revision, _ = s.User(ctx, user.ID)
	stored.Status = 0
	if err := s.put(ctx, userKey(user.ID), stored, revision); err != nil {
		t.Fatal(err)
	}
	if _, err := identity.Verify(ctx, token); err == nil {
		t.Fatal("disabled user accepted")
	}
}

func TestLegacyPasswordMigratesWithoutChangingCredential(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	identity := testIdentity(t, s)
	user := testUser(t, "2", "imported")
	digest := md5.Sum([]byte("old-password"))
	user.PasswordHash = hex.EncodeToString(digest[:])
	user.PasswordFormat = "sqlbot-md5"
	if err := s.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	_, token, err := identity.Login(ctx, user.Account, "old-password")
	if err != nil {
		t.Fatal(err)
	}
	stored, _, err := s.User(ctx, user.ID)
	if err != nil || stored.PasswordFormat != "bcrypt" || stored.AuthRevision != 1 {
		t.Fatalf("migration: %+v %v", stored, err)
	}
	if _, err := identity.Verify(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, _, err := identity.Login(ctx, user.Account, "old-password"); err != nil {
		t.Fatal(err)
	}
}

func TestAccountImportIsAtomicOnCollision(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	if err := s.CreateUser(ctx, testUser(t, "1", "same")); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUser(ctx, testUser(t, "2", "same")); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, _, err := s.User(ctx, "2"); !errors.Is(err, ErrNotFound) {
		t.Fatal("partial imported user survived collision")
	}
	if err := s.CreateUser(ctx, testUser(t, "1", "other")); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	var id ID
	if _, err := s.get(ctx, s.accountKey("other"), &id); !errors.Is(err, ErrNotFound) {
		t.Fatal("partial account reservation survived collision")
	}
}

func TestTokenExpiryTamperingAndAuthRevision(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	identity := testIdentity(t, s)
	user := testUser(t, "1", "one")
	if err := s.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	_, token, err := identity.Login(ctx, user.Account, "Synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(token, ".")
	parts[0] = base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	if _, err := identity.Verify(ctx, strings.Join(parts, ".")); err == nil {
		t.Fatal("algorithm confusion accepted")
	}
	identity.now = func() time.Time { return time.Now().Add(9 * 24 * time.Hour) }
	if _, err := identity.Verify(ctx, token); err == nil {
		t.Fatal("expired token accepted")
	}
	identity.now = time.Now
	stored, revision, _ := s.User(ctx, user.ID)
	stored.AuthRevision++
	if err := s.put(ctx, userKey(user.ID), stored, revision); err != nil {
		t.Fatal(err)
	}
	if _, err := identity.Verify(ctx, token); err == nil {
		t.Fatal("old authentication revision accepted")
	}
}

func TestRSAEncryptedLoginAndMalformedInput(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := NewIdentity(testStore(t), []byte(strings.Repeat("j", 32)), pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := rsa.EncryptPKCS1v15(rand.Reader, &key.PublicKey, []byte("synthetic-account"))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := identity.DecryptLogin(base64.StdEncoding.EncodeToString(ciphertext))
	if err != nil || plain != "synthetic-account" {
		t.Fatal("RSA round trip failed")
	}
	for _, bad := range []string{"plain-account", "", strings.Repeat("a", 3000), base64.StdEncoding.EncodeToString(make([]byte, 256))} {
		if _, err := identity.DecryptLogin(bad); err == nil {
			t.Fatal("malformed encrypted login accepted")
		}
	}
}

func TestStoreEncryptionOwnershipCASAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "chat.db")
	secret := []byte(strings.Repeat("x", 32))
	s, err := OpenStore(ctx, path, secret)
	if err != nil {
		t.Fatal(err)
	}
	key := objectKey{"chat", "workspace-1", "user-1", "1"}
	value := map[string]string{"question": "private-fixture-question"}
	if err := s.put(ctx, key, value, 0); err != nil {
		t.Fatal(err)
	}
	var read map[string]string
	if _, err := s.get(ctx, objectKey{"chat", "workspace-2", "user-1", "1"}, &read); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-workspace read accepted")
	}
	if _, err := s.get(ctx, objectKey{"chat", "workspace-1", "user-2", "1"}, &read); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-user read accepted")
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Go(func() { results <- s.put(ctx, key, value, 1) })
	}
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		} else if !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Fatal("concurrent writers lost CAS protection")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private-fixture-question") {
		t.Fatal("plaintext persisted")
	}
	if wrong, err := OpenStore(ctx, path, []byte(strings.Repeat("y", 32))); err == nil {
		_ = wrong.Close()
		t.Fatal("wrong encryption key accepted")
	}
	s, err = OpenStore(ctx, path, secret)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	revision, err := s.get(ctx, key, &read)
	if err != nil || revision != 2 || read["question"] != "private-fixture-question" {
		t.Fatal("restart lost document")
	}
	_, err = s.db.ExecContext(ctx, `UPDATE chatbi_objects SET owner='user-2' WHERE kind='chat'`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.get(ctx, objectKey{"chat", "workspace-1", "user-2", "1"}, &read); !errors.Is(err, ErrUnavailable) {
		t.Fatal("ciphertext relocation accepted")
	}
}

func TestDecimalIdentityDoesNotRound(t *testing.T) {
	for _, raw := range []string{`7518977644151443457`, `"7518977644151443457"`} {
		var id ID
		if json.Unmarshal([]byte(raw), &id) != nil || id != "7518977644151443457" {
			t.Fatal("large ID rounded")
		}
	}
	for _, raw := range []string{`1.1`, `-1`, `0`, `1e4`, `null`, `"001"`} {
		var id ID
		if json.Unmarshal([]byte(raw), &id) == nil {
			t.Fatalf("invalid ID accepted: %s", raw)
		}
	}
}
