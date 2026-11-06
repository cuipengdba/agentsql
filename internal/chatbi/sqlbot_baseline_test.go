package chatbi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPinnedSQLBotMigrationSnapshots(t *testing.T) {
	for _, item := range []struct {
		version string
		commit  string
	}{
		{"v1.10.2", sqlbotHistoryCommit},
		{"v1.10.3", sqlbotV1103Commit},
	} {
		if !pinnedSQLBotSnapshot(item.version, item.commit) {
			t.Fatalf("pinned version rejected: %s", item.version)
		}
		snapshot := Snapshot{Version: 1, SourceVersion: item.version, SourceCommit: item.commit,
			Keys: IdentityKeys{SigningKey: strings.Repeat("j", 32)}, Users: []User{testUser(t, "1", "one")}}
		path := filepath.Join(t.TempDir(), "identity.private.json")
		raw, _ := json.Marshal(snapshot)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := ReadSnapshot(path); err != nil {
			t.Fatalf("identity preflight %s: %v", item.version, err)
		}
		history := historyFixture()
		history.SourceVersion, history.SourceCommit = item.version, item.commit
		path = filepath.Join(t.TempDir(), "history.private.json")
		raw, _ = json.Marshal(history)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadHistory(path); err != nil {
			t.Fatalf("history preflight %s: %v", item.version, err)
		}
	}
	for _, item := range []struct{ version, commit string }{
		{"v1.10.2", sqlbotV1103Commit},
		{"v1.10.3", sqlbotHistoryCommit},
		{"v1.10.4", sqlbotV1103Commit},
		{"v1.10.3", ""},
	} {
		if pinnedSQLBotSnapshot(item.version, item.commit) {
			t.Fatalf("unverified baseline accepted: %s", item.version)
		}
	}
}
