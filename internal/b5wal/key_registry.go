package b5wal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var ErrKeyUnavailable = errors.New("b5wal: segment key unavailable")

// EnvelopeKMS is intentionally independent of the B3 redaction registry. It
// follows the same revisioned-key/envelope style but never shares key material
// or activation state with redaction keys.
type EnvelopeKMS interface {
	Encrypt(ctx context.Context, masterRevision string, plaintext, aad []byte) ([]byte, error)
	Decrypt(ctx context.Context, masterRevision string, ciphertext, aad []byte) ([]byte, error)
}

// KMSKeyWrapper wraps one independently generated key per segment. A KMS
// outage is returned as ErrKeyUnavailable; callers must fail closed.
type KMSKeyWrapper struct {
	KMS            EnvelopeKMS
	MasterRevision string
}

func (wrapper KMSKeyWrapper) Wrap(context.Context, [32]byte) ([]byte, error) {
	return nil, errors.New("b5wal: KMS wrapper requires segment identity")
}

func (wrapper KMSKeyWrapper) WrapSegmentKey(ctx context.Context, keyID string, segmentID [16]byte, key [32]byte) ([]byte, error) {
	if wrapper.KMS == nil || wrapper.MasterRevision == "" || !strings.HasPrefix(keyID, wrapper.MasterRevision+"/") {
		return nil, ErrKeyUnavailable
	}
	wrapped, err := wrapper.KMS.Encrypt(ctx, wrapper.MasterRevision, key[:], segmentKeyAAD(keyID, segmentID))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrKeyUnavailable, err)
	}
	if len(wrapped) == 0 {
		return nil, fmt.Errorf("%w: empty KMS ciphertext", ErrKeyUnavailable)
	}
	return wrapped, nil
}

func (wrapper KMSKeyWrapper) UnwrapSegmentKey(ctx context.Context, manifest SegmentManifest) ([32]byte, error) {
	var key [32]byte
	if wrapper.KMS == nil || wrapper.MasterRevision == "" || len(manifest.WrappedKey) == 0 || !strings.HasPrefix(manifest.KeyID, wrapper.MasterRevision+"/") {
		return key, ErrKeyUnavailable
	}
	plaintext, err := wrapper.KMS.Decrypt(ctx, wrapper.MasterRevision, manifest.WrappedKey, segmentKeyAAD(manifest.KeyID, manifest.SegmentID))
	if err != nil {
		return key, fmt.Errorf("%w: %v", ErrKeyUnavailable, err)
	}
	if len(plaintext) != len(key) {
		return key, fmt.Errorf("%w: unwrapped key has %d bytes", ErrKeyUnavailable, len(plaintext))
	}
	copy(key[:], plaintext)
	return key, nil
}

func segmentKeyAAD(keyID string, segmentID [16]byte) []byte {
	value := make([]byte, 0, len("agentsql.b5.wal-segment-key.v1")+1+len(keyID)+16)
	value = append(value, "agentsql.b5.wal-segment-key.v1"...)
	value = append(value, 0)
	value = append(value, keyID...)
	value = append(value, segmentID[:]...)
	return value
}

type fileRegistryEntry struct {
	KeyID            string   `json:"key_id"`
	SegmentID        [16]byte `json:"segment_id"`
	WrappedKeyDigest [32]byte `json:"wrapped_key_digest"`
}

// FileKeyRegistry is a durable, process-shared CAS registry for a WAL volume.
// Create-exclusive files provide the CAS; file and parent directory fsync make
// a successful reservation survive restart. The registry stores only a digest
// of KMS ciphertext, never plaintext segment keys.
type FileKeyRegistry struct{ Directory string }

func (registry FileKeyRegistry) Reserve(ctx context.Context, keyID string, segmentID [16]byte, wrappedKey []byte) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if registry.Directory == "" || len(wrappedKey) == 0 {
		return false, errors.New("b5wal: invalid file key registry configuration")
	}
	entry := fileRegistryEntry{KeyID: keyID, SegmentID: segmentID, WrappedKeyDigest: sha256.Sum256(wrappedKey)}
	data, err := json.Marshal(entry)
	if err != nil {
		return false, err
	}
	path := filepath.Join(registry.Directory, hex.EncodeToString(segmentID[:])+".key")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	data = append(data, '\n')
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return false, err
	}
	if closeErr != nil {
		return false, closeErr
	}
	if err := SyncDirectory(registry.Directory); err != nil {
		return false, err
	}
	return true, nil
}

