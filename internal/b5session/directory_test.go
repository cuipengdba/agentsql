package b5session

import (
	"context"
	"crypto/sha256"
	"sync"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/b5"
)

func TestDirectoryOwnerEpochStickyRouteAndWrongOwnerNoTouch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)
	shared := NewMemorySessionStore()
	sealer, err := NewAESGCMSealer("test-kek", bytes(32, 7))
	if err != nil {
		t.Fatal(err)
	}
	directory, err := NewDirectory(DirectoryConfig{Store: shared, Sealer: sealer, IdleTTL: 10 * time.Minute, AbsoluteTTL: time.Hour, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	created, err := directory.Create(ctx, CreateSession{AgentID: "agent", TenantID: "tenant", PrincipalID: "principal", OwnerInstanceID: "owner-a", StickyRoute: "route-a"})
	if err != nil {
		t.Fatal(err)
	}
	body := sha256.Sum256([]byte("body"))
	proof, err := SignContinuation(created.ContinuationSecret, created.Session.SessionID, "execute", "request-1", 1, nil, body)
	if err != nil {
		t.Fatal(err)
	}
	wrong := ContinuationInput{AgentSQLSessionID: created.Session.SessionID, McpSessionID: "transport-only", PrincipalID: "principal", InstanceID: "owner-b", Method: "execute", RequestID: "request-1", OwnerEpoch: 1, BodyDigest: body, Proof: proof}
	decision, err := directory.Touch(ctx, wrong)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Code != b5.ErrorSessionWrongInstance || !decision.RetrySameRequest || decision.StickyRoute != "route-a" || decision.Authorized {
		t.Fatalf("wrong-owner decision = %+v", decision)
	}
	afterWrong, _ := shared.Get(ctx, created.Session.SessionID)
	if afterWrong.Revision != created.Session.Revision || !afterWrong.IdleExpiresAt.Equal(created.Session.IdleExpiresAt) {
		t.Fatalf("wrong owner touched row: before=%+v after=%+v", created.Session, afterWrong)
	}

	current := wrong
	current.InstanceID = "owner-a"
	transferred, err := directory.TransferOwner(ctx, current, "owner-b", "route-b")
	if err != nil {
		t.Fatal(err)
	}
	if transferred.OwnerEpoch != 2 || transferred.OwnerInstanceID != "owner-b" || transferred.StickyRoute != "route-b" {
		t.Fatalf("transfer = %+v", transferred)
	}
	stale, err := directory.Lookup(ctx, current)
	if err != nil {
		t.Fatal(err)
	}
	if stale.Code != b5.ErrorSessionOwnerEpochStale {
		t.Fatalf("stale = %+v", stale)
	}
	proof2, _ := SignContinuation(created.ContinuationSecret, created.Session.SessionID, "execute", "request-2", 2, nil, body)
	decision, err = directory.Lookup(ctx, ContinuationInput{AgentSQLSessionID: created.Session.SessionID, McpSessionID: "mcp-id", PrincipalID: "principal", InstanceID: "owner-b", Method: "execute", RequestID: "request-2", OwnerEpoch: 2, BodyDigest: body, Proof: proof2})
	if err != nil || !decision.Authorized {
		t.Fatalf("new owner decision=%+v err=%v", decision, err)
	}
}

func TestDirectoryRejectsMCPIDAliasAndOwnerTransferCASRace(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	shared := NewMemorySessionStore()
	sealer, _ := NewAESGCMSealer("k", bytes(32, 1))
	directory, _ := NewDirectory(DirectoryConfig{Store: shared, Sealer: sealer, IdleTTL: time.Minute, AbsoluteTTL: time.Hour})
	created, err := directory.Create(ctx, CreateSession{AgentID: "a", TenantID: "t", PrincipalID: "p", OwnerInstanceID: "o", StickyRoute: "r"})
	if err != nil {
		t.Fatal(err)
	}
	body := sha256.Sum256(nil)
	proof, _ := SignContinuation(created.ContinuationSecret, created.Session.SessionID, "transfer", "r", 1, nil, body)
	alias, _ := directory.Lookup(ctx, ContinuationInput{AgentSQLSessionID: created.Session.SessionID, McpSessionID: created.Session.SessionID, PrincipalID: "p", InstanceID: "o", Method: "transfer", RequestID: "r", OwnerEpoch: 1, BodyDigest: body, Proof: proof})
	if alias.Code != b5.ErrorSessionProofRequired {
		t.Fatalf("alias decision = %+v", alias)
	}
	input := ContinuationInput{AgentSQLSessionID: created.Session.SessionID, PrincipalID: "p", InstanceID: "o", Method: "transfer", RequestID: "r", OwnerEpoch: 1, BodyDigest: body, Proof: proof}
	var group sync.WaitGroup
	errs := make(chan error, 2)
	for _, owner := range []string{"new-a", "new-b"} {
		owner := owner
		group.Add(1)
		go func() {
			defer group.Done()
			_, transferErr := directory.TransferOwner(ctx, input, owner, owner+"-route")
			errs <- transferErr
		}()
	}
	group.Wait()
	close(errs)
	succeeded := 0
	for transferErr := range errs {
		if transferErr == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("successful transfers = %d", succeeded)
	}
	final, _ := shared.Get(ctx, created.Session.SessionID)
	if final.OwnerEpoch != 2 {
		t.Fatalf("owner epoch = %d", final.OwnerEpoch)
	}
}

func TestDirectoryTTLAndCloseCAS(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 9, 25, 1, 0, 0, 0, time.UTC)
	shared := NewMemorySessionStore()
	sealer, _ := NewAESGCMSealer("k", bytes(32, 2))
	directory, _ := NewDirectory(DirectoryConfig{Store: shared, Sealer: sealer, IdleTTL: time.Minute, AbsoluteTTL: time.Hour, Now: func() time.Time { return now }})
	created, err := directory.Create(ctx, CreateSession{AgentID: "a", TenantID: "t", PrincipalID: "p", OwnerInstanceID: "o", StickyRoute: "r"})
	if err != nil {
		t.Fatal(err)
	}
	body := sha256.Sum256(nil)
	proof, _ := SignContinuation(created.ContinuationSecret, created.Session.SessionID, "close", "request", 1, nil, body)
	input := ContinuationInput{AgentSQLSessionID: created.Session.SessionID, PrincipalID: "p", InstanceID: "o", Method: "close", RequestID: "request", OwnerEpoch: 1, BodyDigest: body, Proof: proof}
	closed, err := directory.Close(ctx, input)
	if err != nil || closed.Session.Status != b5.SessionTerminal {
		t.Fatalf("closed=%+v err=%v", closed, err)
	}

	created, err = directory.Create(ctx, CreateSession{AgentID: "a", TenantID: "t", PrincipalID: "p", OwnerInstanceID: "o", StickyRoute: "r"})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	expired, err := directory.Expire(ctx, 10)
	if err != nil || expired != 1 {
		t.Fatalf("expired=%d err=%v", expired, err)
	}
	row, _ := shared.Get(ctx, created.Session.SessionID)
	if row.Status != b5.SessionExpired {
		t.Fatalf("status=%s", row.Status)
	}
}

func bytes(size int, value byte) []byte {
	result := make([]byte, size)
	for i := range result {
		result[i] = value
	}
	return result
}
