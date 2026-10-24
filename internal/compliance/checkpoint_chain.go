package compliance

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ArchiveCheckpointChainVerifier checks a sequence against an externally
// trusted predecessor and a separately pinned final checkpoint digest.
// It does not prove that an operator exported every audit event or configured
// immutable external storage.
type ArchiveCheckpointChainVerifier struct {
	key          ed25519.PublicKey
	chainID      string
	previous     string
	expectedHead string
	now          time.Time
	maxAge       time.Duration
	last         ArchiveCheckpoint
	count        int
}

func NewArchiveCheckpointChainVerifier(key ed25519.PublicKey, chainID, trustedPrevious, expectedHead string, now time.Time, maxAge time.Duration) (*ArchiveCheckpointChainVerifier, error) {
	if len(key) != ed25519.PublicKeySize || chainID == "" || strings.TrimSpace(chainID) != chainID ||
		!validCheckpointDigest(trustedPrevious, true) || !validCheckpointDigest(expectedHead, false) ||
		now.IsZero() || maxAge <= 0 {
		return nil, errors.New("invalid audit checkpoint chain verification input")
	}
	return &ArchiveCheckpointChainVerifier{key: append(ed25519.PublicKey(nil), key...), chainID: chainID,
		previous: trustedPrevious, expectedHead: expectedHead, now: now, maxAge: maxAge}, nil
}

func (verifier *ArchiveCheckpointChainVerifier) VerifyNext(archive []byte, checkpoint ArchiveCheckpoint) error {
	if verifier == nil {
		return errors.New("audit checkpoint chain verifier is not initialized")
	}
	if checkpoint.ChainID != verifier.chainID {
		return errors.New("audit checkpoint chain ID differs from trusted selection")
	}
	if verifier.count > 0 {
		if !checkpoint.ArchiveCreatedAt.After(verifier.last.ArchiveCreatedAt) ||
			checkpoint.SignedAt.Before(verifier.last.SignedAt) ||
			checkpoint.HeadSeq < verifier.last.HeadSeq ||
			(checkpoint.HeadSeq == verifier.last.HeadSeq && checkpoint.HeadHash != verifier.last.HeadHash) {
			return errors.New("audit checkpoint chain order or head is inconsistent")
		}
	}
	if err := VerifyArchiveCheckpoint(archive, checkpoint, verifier.key, verifier.previous, verifier.now, verifier.maxAge); err != nil {
		return fmt.Errorf("verify audit checkpoint %d: %w", verifier.count+1, err)
	}
	digest, err := ArchiveCheckpointDigest(checkpoint)
	if err != nil {
		return err
	}
	verifier.previous, verifier.last = digest, checkpoint
	verifier.count++
	return nil
}

func (verifier *ArchiveCheckpointChainVerifier) Finish() (string, int, error) {
	if verifier == nil || verifier.count == 0 {
		return "", 0, errors.New("audit checkpoint chain is empty")
	}
	if verifier.previous != verifier.expectedHead {
		return "", verifier.count, errors.New("audit checkpoint chain head differs from trusted state")
	}
	return verifier.previous, verifier.count, nil
}
