package b5wal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// KeyRegistry reserves key identities by compare-and-set. A false result means
// the key ID already exists; callers must discard the candidate key and ID.
type KeyRegistry interface {
	Reserve(ctx context.Context, keyID string, segmentID [16]byte, wrappedKey []byte) (bool, error)
}

// KeyWrapper represents the external master-key operation. The raw segment key
// is never written to the manifest by SegmentFactory.
type KeyWrapper interface {
	Wrap(ctx context.Context, segmentKey [32]byte) ([]byte, error)
}

// ContextKeyWrapper is the production envelope-encryption surface. The legacy
// KeyWrapper method remains for isolated S7a tests; production wrappers bind
// ciphertext to the immutable key and segment identity as authenticated data.
type ContextKeyWrapper interface {
	WrapSegmentKey(ctx context.Context, keyID string, segmentID [16]byte, segmentKey [32]byte) ([]byte, error)
}

// ManifestStore must make the manifest durable, including its parent directory,
// before Persist returns nil.
type ManifestStore interface {
	Persist(ctx context.Context, manifest SegmentManifest) error
}

type SegmentManifest struct {
	FormatVersion   uint16   `json:"format_version"`
	SegmentID       [16]byte `json:"segment_id"`
	KeyID           string   `json:"key_id"`
	WrappedKey      []byte   `json:"wrapped_key"`
	NonceDomain     [4]byte  `json:"nonce_domain"`
	OwnerGeneration uint64   `json:"owner_generation"`
}

type Segment struct {
	Manifest SegmentManifest
	Key      [32]byte
}

type SegmentFactory struct {
	Random         io.Reader
	MasterRevision string
	Registry       KeyRegistry
	Wrapper        KeyWrapper
	Manifests      ManifestStore
}

func (factory SegmentFactory) Create(ctx context.Context, ownerGeneration uint64) (Segment, error) {
	var empty Segment
	if factory.Registry == nil || factory.Wrapper == nil || factory.Manifests == nil {
		return empty, errors.New("b5wal: incomplete segment factory")
	}
	if err := validateASCII("master_revision", factory.MasterRevision, 63); err != nil {
		return empty, err
	}
	random := factory.Random
	if random == nil {
		random = rand.Reader
	}
	for attempt := 0; attempt < 8; attempt++ {
		var segment Segment
		if _, err := io.ReadFull(random, segment.Manifest.SegmentID[:]); err != nil {
			return empty, fmt.Errorf("b5wal: segment id: %w", err)
		}
		if _, err := io.ReadFull(random, segment.Key[:]); err != nil {
			return empty, fmt.Errorf("b5wal: segment key: %w", err)
		}
		if _, err := io.ReadFull(random, segment.Manifest.NonceDomain[:]); err != nil {
			return empty, fmt.Errorf("b5wal: nonce domain: %w", err)
		}
		segment.Manifest.FormatVersion = WALFormatVersion
		segment.Manifest.OwnerGeneration = ownerGeneration
		segment.Manifest.KeyID = factory.MasterRevision + "/" + hex.EncodeToString(segment.Manifest.SegmentID[:])
		var wrapped []byte
		var err error
		if contextual, ok := factory.Wrapper.(ContextKeyWrapper); ok {
			wrapped, err = contextual.WrapSegmentKey(ctx, segment.Manifest.KeyID, segment.Manifest.SegmentID, segment.Key)
		} else {
			wrapped, err = factory.Wrapper.Wrap(ctx, segment.Key)
		}
		if err != nil {
			return empty, fmt.Errorf("b5wal: wrap segment key: %w", err)
		}
		segment.Manifest.WrappedKey = append([]byte(nil), wrapped...)
		reserved, err := factory.Registry.Reserve(ctx, segment.Manifest.KeyID, segment.Manifest.SegmentID, wrapped)
		if err != nil {
			return empty, fmt.Errorf("b5wal: key registry CAS: %w", err)
		}
		if !reserved {
			continue
		}
		// Persist includes file fsync and parent-directory fsync. A failure burns
		// the registry identity; it is never retried or used for records.
		if err := factory.Manifests.Persist(ctx, segment.Manifest); err != nil {
			return empty, fmt.Errorf("b5wal: persist segment manifest: %w", err)
		}
		return segment, nil
	}
	return empty, errors.New("b5wal: exhausted duplicate segment IDs")
}

// FileManifestStore is the local manifest implementation. Each manifest is
// create-exclusive, mode 0600, file-synced, then its parent directory is synced.
type FileManifestStore struct{ Directory string }

func (store FileManifestStore) Persist(ctx context.Context, manifest SegmentManifest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	name := hex.EncodeToString(manifest.SegmentID[:]) + ".manifest"
	path := filepath.Join(store.Directory, name)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	data, err := json.Marshal(manifest)
	if err == nil {
		data = append(data, '\n')
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return syncDirectory(store.Directory)
}

// MemoryKeyRegistry is a concurrency-safe contract implementation for tests.
// It models durable CAS state only; it is not a production registry.
type MemoryKeyRegistry struct {
	mu   sync.Mutex
	keys map[string][16]byte
}

func NewMemoryKeyRegistry() *MemoryKeyRegistry {
	return &MemoryKeyRegistry{keys: make(map[string][16]byte)}
}

func (registry *MemoryKeyRegistry) Reserve(_ context.Context, keyID string, segmentID [16]byte, _ []byte) (bool, error) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.keys[keyID]; exists {
		return false, nil
	}
	registry.keys[keyID] = segmentID
	return true, nil
}

// IdentityKeyWrapper is only a test implementation. Production wiring must
// provide an authenticated KMS-backed wrapper before this package is enabled.
type IdentityKeyWrapper struct{}

func (IdentityKeyWrapper) Wrap(_ context.Context, segmentKey [32]byte) ([]byte, error) {
	return append([]byte(nil), segmentKey[:]...), nil
}

// MemoryManifestStore is a test implementation that records the persist order.
type MemoryManifestStore struct {
	mu        sync.Mutex
	Manifests []SegmentManifest
	Err       error
}

func (store *MemoryManifestStore) Persist(_ context.Context, manifest SegmentManifest) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.Err != nil {
		return store.Err
	}
	manifest.WrappedKey = append([]byte(nil), manifest.WrappedKey...)
	store.Manifests = append(store.Manifests, manifest)
	return nil
}
