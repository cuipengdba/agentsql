package b5wal

import (
	"crypto/sha256"
	"errors"
	"testing"
)

func replayEvent(uuid byte, transaction string, sequence uint64, previous [32]byte, payload string) ReplayEvent {
	var eventUUID [16]byte
	eventUUID[0] = uuid
	return ReplayEvent{EventUUID: eventUUID, PayloadDigest: sha256.Sum256([]byte(payload)), TransactionID: transaction, TransactionSeq: sequence, PreviousTxDigest: previous}
}

func TestUUIDIdempotencyAndDoubleChains(t *testing.T) {
	verifier := NewReplayVerifier()
	first := replayEvent(1, "tx-a", 1, [32]byte{}, "a1")
	entry1, duplicate, err := verifier.Ingest(first)
	if err != nil || duplicate {
		t.Fatalf("first: duplicate=%v err=%v", duplicate, err)
	}
	otherTx := replayEvent(2, "tx-b", 1, [32]byte{}, "b1")
	entry2, duplicate, err := verifier.Ingest(otherTx)
	if err != nil || duplicate || entry2.PreviousDigest != entry1.Digest {
		t.Fatalf("interleaved global chain: entry=%+v duplicate=%v err=%v", entry2, duplicate, err)
	}
	second := replayEvent(3, "tx-a", 2, first.PayloadDigest, "a2")
	if _, duplicate, err := verifier.Ingest(second); err != nil || duplicate {
		t.Fatalf("second tx event: duplicate=%v err=%v", duplicate, err)
	}
	if _, duplicate, err := verifier.Ingest(first); err != nil || !duplicate {
		t.Fatalf("same UUID/same payload: duplicate=%v err=%v", duplicate, err)
	}
	conflict := first
	conflict.PayloadDigest = sha256.Sum256([]byte("conflict"))
	if _, _, err := verifier.Ingest(conflict); !errors.Is(err, ErrEventUUIDConflict) {
		t.Fatalf("UUID conflict = %v", err)
	}
	if err := VerifyGlobalChain(verifier.GlobalChain()); err != nil {
		t.Fatal(err)
	}
	broken := verifier.GlobalChain()
	broken[1].PreviousDigest[0] ^= 1
	if err := VerifyGlobalChain(broken); !errors.Is(err, ErrGlobalChain) {
		t.Fatalf("broken global chain = %v", err)
	}
}

func TestOutOfOrderAndTransactionForkRejected(t *testing.T) {
	verifier := NewReplayVerifier()
	if _, _, err := verifier.Ingest(replayEvent(1, "tx", 2, [32]byte{}, "second")); !errors.Is(err, ErrTransactionChain) {
		t.Fatalf("out of order = %v", err)
	}
	first := replayEvent(1, "tx", 1, [32]byte{}, "first")
	if _, _, err := verifier.Ingest(first); err != nil {
		t.Fatal(err)
	}
	wrongPrevious := sha256.Sum256([]byte("fork"))
	if _, _, err := verifier.Ingest(replayEvent(2, "tx", 2, wrongPrevious, "second")); !errors.Is(err, ErrTransactionChain) {
		t.Fatalf("fork = %v", err)
	}
}
