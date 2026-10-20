package compliance

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"
)

func TestArchiveCheckpointAuthenticatesBytesAndPredecessor(t *testing.T) {
	now := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	archive, _, err := BuildArchive(now.Add(-time.Minute), []ArchiveFile{{Name: "audit.csv", MediaType: "text/csv", Data: []byte("id\n1\n")}})
	if err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := SignArchiveCheckpoint(archive, "traffic", 1, strings.Repeat("a", 64), strings.Repeat("b", 64), now, private)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyArchiveCheckpoint(archive, checkpoint, public, strings.Repeat("b", 64), now.Add(time.Minute), time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := VerifyArchiveCheckpoint(archive, checkpoint, public, "", now.Add(time.Minute), time.Hour); err == nil {
		t.Fatal("missing trusted predecessor accepted")
	}
	if err := VerifyArchiveCheckpoint(archive, checkpoint, public, strings.Repeat("b", 64), now.Add(2*time.Hour), time.Hour); err == nil {
		t.Fatal("stale checkpoint accepted")
	}
	oldArchive, _, err := BuildArchive(now.Add(-2*time.Hour), []ArchiveFile{{Name: "audit.csv", MediaType: "text/csv", Data: []byte("id\n1\n")}})
	if err != nil {
		t.Fatal(err)
	}
	oldCheckpoint, err := SignArchiveCheckpoint(oldArchive, "traffic", 1, strings.Repeat("a", 64), "", now, private)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyArchiveCheckpoint(oldArchive, oldCheckpoint, public, "", now, time.Hour); err == nil {
		t.Fatal("old archive freshly signed was accepted")
	}
	tampered := append([]byte(nil), archive...)
	tampered[len(tampered)-1] ^= 1
	if err := VerifyArchiveCheckpoint(tampered, checkpoint, public, strings.Repeat("b", 64), now.Add(time.Minute), time.Hour); err == nil {
		t.Fatal("altered archive accepted")
	}
	checkpoint.HeadHash = strings.Repeat("c", 64)
	if err := VerifyArchiveCheckpoint(archive, checkpoint, public, strings.Repeat("b", 64), now.Add(time.Minute), time.Hour); err == nil {
		t.Fatal("altered chain head accepted")
	}
}
