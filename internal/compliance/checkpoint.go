package compliance

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const ArchiveCheckpointSchema = "agentsql.audit-checkpoint/v1"

// ArchiveCheckpoint is a signed external witness for one verified ZIP. The
// operator must keep the public key and checkpoint history outside AgentSQL's
// mutable metadata and audit stores.
type ArchiveCheckpoint struct {
	SchemaVersion    string    `json:"schema_version"`
	ArchiveSHA256    string    `json:"archive_sha256"`
	ArchiveCreatedAt time.Time `json:"archive_created_at"`
	SignedAt         time.Time `json:"signed_at"`
	ChainID          string    `json:"chain_id"`
	HeadSeq          int64     `json:"head_seq"`
	HeadHash         string    `json:"head_hash"`
	PreviousSHA256   string    `json:"previous_sha256,omitempty"`
	Signature        string    `json:"signature"`
}

type checkpointPayload struct {
	SchemaVersion    string    `json:"schema_version"`
	ArchiveSHA256    string    `json:"archive_sha256"`
	ArchiveCreatedAt time.Time `json:"archive_created_at"`
	SignedAt         time.Time `json:"signed_at"`
	ChainID          string    `json:"chain_id"`
	HeadSeq          int64     `json:"head_seq"`
	HeadHash         string    `json:"head_hash"`
	PreviousSHA256   string    `json:"previous_sha256,omitempty"`
}

func (checkpoint ArchiveCheckpoint) payload() checkpointPayload {
	return checkpointPayload{checkpoint.SchemaVersion, checkpoint.ArchiveSHA256, checkpoint.ArchiveCreatedAt,
		checkpoint.SignedAt, checkpoint.ChainID, checkpoint.HeadSeq, checkpoint.HeadHash, checkpoint.PreviousSHA256}
}

func validCheckpointDigest(value string, optional bool) bool {
	if optional && value == "" {
		return true
	}
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validateCheckpoint(checkpoint ArchiveCheckpoint) error {
	if checkpoint.SchemaVersion != ArchiveCheckpointSchema || checkpoint.ArchiveCreatedAt.IsZero() || checkpoint.SignedAt.IsZero() ||
		checkpoint.ChainID == "" || strings.TrimSpace(checkpoint.ChainID) != checkpoint.ChainID || checkpoint.HeadSeq < 0 ||
		!validCheckpointDigest(checkpoint.ArchiveSHA256, false) || !validCheckpointDigest(checkpoint.PreviousSHA256, true) ||
		(checkpoint.HeadSeq == 0 && checkpoint.HeadHash != "") || (checkpoint.HeadSeq > 0 && !validCheckpointDigest(checkpoint.HeadHash, false)) {
		return errors.New("audit checkpoint fields are invalid")
	}
	return nil
}

func checkpointMessage(checkpoint ArchiveCheckpoint) ([]byte, error) {
	if err := validateCheckpoint(checkpoint); err != nil {
		return nil, err
	}
	return json.Marshal(checkpoint.payload())
}

func SignArchiveCheckpoint(archive []byte, chainID string, headSeq int64, headHash, previousSHA256 string, signedAt time.Time, key ed25519.PrivateKey) (ArchiveCheckpoint, error) {
	if len(key) != ed25519.PrivateKeySize {
		return ArchiveCheckpoint{}, errors.New("invalid checkpoint signing key")
	}
	manifest, err := VerifyArchive(archive)
	if err != nil {
		return ArchiveCheckpoint{}, fmt.Errorf("verify archive before signing: %w", err)
	}
	archiveDigest := sha256.Sum256(archive)
	checkpoint := ArchiveCheckpoint{SchemaVersion: ArchiveCheckpointSchema, ArchiveSHA256: hex.EncodeToString(archiveDigest[:]),
		ArchiveCreatedAt: manifest.GeneratedAt.UTC(), SignedAt: signedAt.UTC().Truncate(time.Second),
		ChainID: chainID, HeadSeq: headSeq, HeadHash: headHash, PreviousSHA256: previousSHA256}
	message, err := checkpointMessage(checkpoint)
	if err != nil {
		return ArchiveCheckpoint{}, err
	}
	checkpoint.Signature = hex.EncodeToString(ed25519.Sign(key, message))
	return checkpoint, nil
}

// VerifyArchiveCheckpoint checks the ZIP, signer, expected predecessor and
// archive age. A caller with no externally retained predecessor cannot detect
// replay of an otherwise valid first checkpoint.
func VerifyArchiveCheckpoint(archive []byte, checkpoint ArchiveCheckpoint, key ed25519.PublicKey, expectedPrevious string, now time.Time, maxAge time.Duration) error {
	if len(key) != ed25519.PublicKeySize || maxAge <= 0 || now.IsZero() || !validCheckpointDigest(expectedPrevious, true) {
		return errors.New("invalid checkpoint verification input")
	}
	message, err := checkpointMessage(checkpoint)
	if err != nil {
		return err
	}
	if checkpoint.PreviousSHA256 != expectedPrevious {
		return errors.New("audit checkpoint predecessor differs from trusted state")
	}
	if now.Before(checkpoint.SignedAt) || now.Sub(checkpoint.SignedAt) > maxAge ||
		now.Before(checkpoint.ArchiveCreatedAt) || now.Sub(checkpoint.ArchiveCreatedAt) > maxAge ||
		checkpoint.SignedAt.Before(checkpoint.ArchiveCreatedAt) {
		return errors.New("audit checkpoint timestamp is outside the accepted window")
	}
	signature, err := hex.DecodeString(checkpoint.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize || !ed25519.Verify(key, message, signature) {
		return errors.New("audit checkpoint signature is invalid")
	}
	manifest, err := VerifyArchive(archive)
	if err != nil {
		return fmt.Errorf("verify checkpoint archive: %w", err)
	}
	if !manifest.GeneratedAt.Equal(checkpoint.ArchiveCreatedAt) {
		return errors.New("audit checkpoint archive time differs")
	}
	digest := sha256.Sum256(archive)
	if hex.EncodeToString(digest[:]) != checkpoint.ArchiveSHA256 {
		return errors.New("audit checkpoint archive digest differs")
	}
	return nil
}

func ArchiveCheckpointDigest(checkpoint ArchiveCheckpoint) (string, error) {
	if _, err := checkpointMessage(checkpoint); err != nil {
		return "", err
	}
	signature, err := hex.DecodeString(checkpoint.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return "", errors.New("invalid checkpoint signature")
	}
	bytes, err := json.Marshal(checkpoint)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(bytes)
	return hex.EncodeToString(digest[:]), nil
}
