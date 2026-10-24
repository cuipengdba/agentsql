package compliance

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"
)

func TestArchiveCheckpointChainRequiresPinnedHeadAndEveryMiddleArchive(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 24, 3, 0, 0, 0, time.UTC)
	firstArchive, _, err := BuildArchive(now.Add(-2*time.Hour), []ArchiveFile{{Name: "audit.csv", MediaType: "text/csv", Data: []byte("id\n1\n")}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := SignArchiveCheckpoint(firstArchive, "tenant-chain", 1, strings.Repeat("a", 64), "", now.Add(-90*time.Minute), private)
	if err != nil {
		t.Fatal(err)
	}
	firstDigest, err := ArchiveCheckpointDigest(first)
	if err != nil {
		t.Fatal(err)
	}
	secondArchive, _, err := BuildArchive(now.Add(-time.Hour), []ArchiveFile{{Name: "audit.csv", MediaType: "text/csv", Data: []byte("id\n2\n")}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := SignArchiveCheckpoint(secondArchive, "tenant-chain", 2, strings.Repeat("b", 64), firstDigest, now.Add(-30*time.Minute), private)
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := ArchiveCheckpointDigest(second)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewArchiveCheckpointChainVerifier(public, "tenant-chain", "", secondDigest, now, 3*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifier.VerifyNext(firstArchive, first); err != nil {
		t.Fatal(err)
	}
	if _, _, err := verifier.Finish(); err == nil {
		t.Fatal("missing final archive matched trusted head")
	}
	if err := verifier.VerifyNext(secondArchive, second); err != nil {
		t.Fatal(err)
	}
	if head, count, err := verifier.Finish(); err != nil || head != secondDigest || count != 2 {
		t.Fatalf("unexpected verified chain: %q %d %v", head, count, err)
	}
	broken, err := NewArchiveCheckpointChainVerifier(public, "tenant-chain", "", secondDigest, now, 3*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := broken.VerifyNext(secondArchive, second); err == nil {
		t.Fatal("missing first checkpoint was accepted")
	}
	broken, err = NewArchiveCheckpointChainVerifier(public, "tenant-chain", "", secondDigest, now, 3*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	changed := append([]byte(nil), firstArchive...)
	changed[0] ^= 1
	if err := broken.VerifyNext(changed, first); err == nil {
		t.Fatal("tampered archive was accepted")
	}
	broken, err = NewArchiveCheckpointChainVerifier(public, "other-chain", "", secondDigest, now, 3*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := broken.VerifyNext(firstArchive, first); err == nil {
		t.Fatal("wrong chain ID was accepted")
	}
}
