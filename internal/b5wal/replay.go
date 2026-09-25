package b5wal

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
)

var (
	ErrEventUUIDConflict = errors.New("b5wal: AUDIT_EVENT_UUID_CONFLICT")
	ErrTransactionChain  = errors.New("b5wal: transaction chain violation")
	ErrGlobalChain       = errors.New("b5wal: global chain violation")
)

type ReplayEvent struct {
	EventUUID        [16]byte
	PayloadDigest    [32]byte
	TransactionID    string
	TransactionSeq   uint64
	PreviousTxDigest [32]byte
}

type GlobalEntry struct {
	Position       uint64
	PreviousDigest [32]byte
	Digest         [32]byte
	EventUUID      [16]byte
	PayloadDigest  [32]byte
}

type ReplayVerifier struct {
	mu      sync.Mutex
	byUUID  map[[16]byte][32]byte
	txHeads map[string][32]byte
	txSeq   map[string]uint64
	global  []GlobalEntry
}

func NewReplayVerifier() *ReplayVerifier {
	return &ReplayVerifier{byUUID: make(map[[16]byte][32]byte), txHeads: make(map[string][32]byte), txSeq: make(map[string]uint64)}
}

// Ingest validates the caller-supplied per-transaction predecessor and builds
// the independent global chain in actual ingestion order. Same UUID + same
// payload is idempotent; same UUID + different payload fails closed.
func (verifier *ReplayVerifier) Ingest(event ReplayEvent) (GlobalEntry, bool, error) {
	verifier.mu.Lock()
	defer verifier.mu.Unlock()
	if existing, ok := verifier.byUUID[event.EventUUID]; ok {
		if subtle.ConstantTimeCompare(existing[:], event.PayloadDigest[:]) != 1 {
			return GlobalEntry{}, false, ErrEventUUIDConflict
		}
		return GlobalEntry{}, true, nil
	}
	expectedSeq := verifier.txSeq[event.TransactionID] + 1
	expectedPrevious := verifier.txHeads[event.TransactionID]
	if event.TransactionSeq != expectedSeq || subtle.ConstantTimeCompare(expectedPrevious[:], event.PreviousTxDigest[:]) != 1 {
		return GlobalEntry{}, false, fmt.Errorf("%w: expected seq %d", ErrTransactionChain, expectedSeq)
	}
	var previousGlobal [32]byte
	if len(verifier.global) > 0 {
		previousGlobal = verifier.global[len(verifier.global)-1].Digest
	}
	position := uint64(len(verifier.global) + 1)
	digestInput := make([]byte, 0, 32+8+16+32)
	digestInput = append(digestInput, previousGlobal[:]...)
	var encodedPosition [8]byte
	binary.BigEndian.PutUint64(encodedPosition[:], position)
	digestInput = append(digestInput, encodedPosition[:]...)
	digestInput = append(digestInput, event.EventUUID[:]...)
	digestInput = append(digestInput, event.PayloadDigest[:]...)
	entry := GlobalEntry{Position: position, PreviousDigest: previousGlobal, Digest: sha256.Sum256(digestInput), EventUUID: event.EventUUID, PayloadDigest: event.PayloadDigest}
	verifier.global = append(verifier.global, entry)
	verifier.byUUID[event.EventUUID] = event.PayloadDigest
	verifier.txHeads[event.TransactionID] = event.PayloadDigest
	verifier.txSeq[event.TransactionID] = event.TransactionSeq
	return entry, false, nil
}

func (verifier *ReplayVerifier) GlobalChain() []GlobalEntry {
	verifier.mu.Lock()
	defer verifier.mu.Unlock()
	return append([]GlobalEntry(nil), verifier.global...)
}

func VerifyGlobalChain(entries []GlobalEntry) error {
	var previous [32]byte
	for index, entry := range entries {
		position := uint64(index + 1)
		if entry.Position != position || subtle.ConstantTimeCompare(previous[:], entry.PreviousDigest[:]) != 1 {
			return ErrGlobalChain
		}
		input := make([]byte, 0, 32+8+16+32)
		input = append(input, previous[:]...)
		var encodedPosition [8]byte
		binary.BigEndian.PutUint64(encodedPosition[:], position)
		input = append(input, encodedPosition[:]...)
		input = append(input, entry.EventUUID[:]...)
		input = append(input, entry.PayloadDigest[:]...)
		digest := sha256.Sum256(input)
		if subtle.ConstantTimeCompare(digest[:], entry.Digest[:]) != 1 {
			return ErrGlobalChain
		}
		previous = entry.Digest
	}
	return nil
}
