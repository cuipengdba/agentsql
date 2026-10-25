package chatbi

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnapshotPreflightIsReadOnlyAndRejectsCollisions(t *testing.T) {
	snapshot := Snapshot{Version: 1, SourceVersion: "v1.10.2", SourceCommit: "aa41fc1033bb974a703df9200a98ec8b9eec5f25", Keys: IdentityKeys{SigningKey: strings.Repeat("j", 32)}, Users: []User{testUser(t, "1", "one")}}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "snapshot.private.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	parsed, report, err := ReadSnapshot(path)
	if err != nil || report.Users != 1 || report.CompleteMigration {
		t.Fatalf("preflight: %+v %v", report, err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(raw) {
		t.Fatal("preflight changed source snapshot")
	}
	s := testStore(t)
	if err := s.ImportSnapshot(context.Background(), parsed); err != nil {
		t.Fatal(err)
	}
	if err := s.ImportSnapshot(context.Background(), parsed); !errors.Is(err, ErrConflict) {
		t.Fatal("import overwrote existing identities")
	}
	snapshot.Users = append(snapshot.Users, testUser(t, "2", "one"))
	raw, _ = json.Marshal(snapshot)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadSnapshot(path); err == nil {
		t.Fatal("duplicate account passed preflight")
	}
}

func TestIncompleteImportCannotBecomeRunnable(t *testing.T) {
	s := testStore(t)
	snapshot := Snapshot{Keys: IdentityKeys{SigningKey: strings.Repeat("j", 32)}, Users: []User{testUser(t, "1", "one"), testUser(t, "1", "two")}}
	if err := s.ImportSnapshot(context.Background(), snapshot); err == nil {
		t.Fatal("invalid snapshot imported")
	}
	var keys IdentityKeys
	if _, err := s.get(context.Background(), objectKey{"system", "system", "system", "identity-keys"}, &keys); !errors.Is(err, ErrNotFound) {
		t.Fatal("partial import published service keys")
	}
}

// This opt-in test consumes private runtime exports. Its report never includes
// credentials. It is not part of ordinary CI or a substitute for business tests.
func TestExportedSQLBotIdentityFixture(t *testing.T) {
	path := os.Getenv("AGENTSQL_CHATBI_IDENTITY_FIXTURE")
	if path == "" {
		t.Skip("private SQLBot runtime export not provided")
	}
	snapshot, _, err := ReadSnapshot(path)
	if err != nil {
		t.Fatal("runtime snapshot preflight failed")
	}
	s := testStore(t)
	if err := s.ImportSnapshot(context.Background(), snapshot); err != nil {
		t.Fatal("runtime snapshot import failed")
	}
	identity, err := NewIdentity(s, []byte(snapshot.Keys.SigningKey), []byte(snapshot.Keys.LoginPEM))
	if err != nil {
		t.Fatal("runtime keys rejected")
	}
	fixturePath := os.Getenv("AGENTSQL_CHATBI_LOGIN_FIXTURE")
	if fixturePath == "" {
		t.Skip("encrypted original-client fixture not provided")
	}
	raw, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal("cannot load runtime login fixture")
	}
	var fixture struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Token    string `json:"token"`
		ID       ID     `json:"id"`
	}
	if json.Unmarshal(raw, &fixture) != nil {
		t.Fatal("invalid runtime login fixture")
	}
	account, e1 := identity.DecryptLogin(fixture.Username)
	password, e2 := identity.DecryptLogin(fixture.Password)
	if e1 != nil || e2 != nil {
		t.Fatal("original encrypted credentials were rejected")
	}
	user, token, err := identity.Login(context.Background(), account, password)
	if err != nil || user.ID != fixture.ID {
		t.Fatal("original client login failed")
	}
	if _, err := identity.Verify(context.Background(), token); err != nil {
		t.Fatal("native token rejected")
	}
	p, err := identity.Verify(context.Background(), fixture.Token)
	if err != nil || p.User.ID != fixture.ID {
		t.Fatal("original SQLBot token rejected")
	}
}
