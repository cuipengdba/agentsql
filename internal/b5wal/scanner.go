package b5wal

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type SegmentKeyUnwrapper interface {
	UnwrapSegmentKey(context.Context, SegmentManifest) ([32]byte, error)
}

type QuarantineStore interface {
	Quarantine(context.Context, string, error) error
}

type ScanResult struct {
	Records     []RecoveredRecord
	Quarantined []string
}

// FileScanner accepts only complete authenticated extents. A torn tail,
// manifest mismatch, ordinal gap/reorder, bad AEAD/digest, or duplicate nonce
// quarantines the whole segment; no prefix is replayed from that segment.
type FileScanner struct {
	Directory  string
	Unwrapper  SegmentKeyUnwrapper
	Quarantine QuarantineStore
}

func (scanner FileScanner) Scan(ctx context.Context) (ScanResult, error) {
	var result ScanResult
	if scanner.Directory == "" || scanner.Unwrapper == nil || scanner.Quarantine == nil {
		return result, errors.New("b5wal: incomplete file scanner")
	}
	manifests, err := filepath.Glob(filepath.Join(scanner.Directory, "*.manifest"))
	if err != nil {
		return result, err
	}
	sort.Strings(manifests)
	for _, manifestPath := range manifests {
		records, scanErr := scanner.scanSegment(ctx, manifestPath)
		if errors.Is(scanErr, ErrKeyUnavailable) {
			return result, scanErr // KMS outage is not evidence of corrupt media.
		}
		if scanErr != nil {
			walPath := strings.TrimSuffix(manifestPath, ".manifest") + ".wal"
			if quarantineErr := scanner.Quarantine.Quarantine(ctx, walPath, scanErr); quarantineErr != nil {
				return result, errors.Join(scanErr, quarantineErr)
			}
			result.Quarantined = append(result.Quarantined, walPath)
			continue
		}
		result.Records = append(result.Records, records...)
	}
	return result, nil
}

func (scanner FileScanner) scanSegment(ctx context.Context, manifestPath string) ([]RecoveredRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, err
	}
	var manifest SegmentManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("b5wal: invalid manifest: %w", err)
	}
	if manifest.FormatVersion != WALFormatVersion || manifest.KeyID == "" || manifest.SegmentID == ([16]byte{}) {
		return nil, errors.New("b5wal: invalid manifest identity")
	}
	expectedName := hex.EncodeToString(manifest.SegmentID[:]) + ".manifest"
	if filepath.Base(manifestPath) != expectedName {
		return nil, errors.New("b5wal: manifest filename identity mismatch")
	}
	key, err := scanner.Unwrapper.UnwrapSegmentKey(ctx, manifest)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrKeyUnavailable, err)
	}
	walPath := strings.TrimSuffix(manifestPath, ".manifest") + ".wal"
	file, err := os.Open(walPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return nil, err
	}
	charge := int64(EncodedRecordCharge())
	if stat.Size()%charge != 0 {
		return nil, errors.New("b5wal: torn segment tail")
	}
	records := make([]RecoveredRecord, 0, stat.Size()/charge)
	seenNonce := make(map[[12]byte]struct{})
	for ordinal := uint64(0); int64(ordinal)*charge < stat.Size(); ordinal++ {
		extent := make([]byte, charge)
		if _, err := io.ReadFull(file, extent); err != nil {
			return nil, err
		}
		decoded, err := DecodeRecord(key[:], extent)
		if err != nil {
			return nil, err
		}
		if decoded.SegmentID != manifest.SegmentID || decoded.KeyID != manifest.KeyID || decoded.RecordOrdinal != ordinal || decoded.Nonce[0] != manifest.NonceDomain[0] || decoded.Nonce[1] != manifest.NonceDomain[1] || decoded.Nonce[2] != manifest.NonceDomain[2] || decoded.Nonce[3] != manifest.NonceDomain[3] || binary.BigEndian.Uint64(decoded.Nonce[4:]) != ordinal {
			return nil, errors.New("b5wal: segment identity or ordinal mismatch")
		}
		if _, duplicate := seenNonce[decoded.Nonce]; duplicate {
			return nil, errors.New("b5wal: duplicate segment nonce")
		}
		seenNonce[decoded.Nonce] = struct{}{}
		recovered, err := recoveredRecord(decoded, sha256.Sum256(extent), walPath)
		if err != nil {
			return nil, err
		}
		records = append(records, recovered)
	}
	return records, nil
}

type FileQuarantineStore struct{ Directory string }

func (store FileQuarantineStore) Quarantine(ctx context.Context, source string, cause error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if store.Directory == "" || source == "" || cause == nil {
		return errors.New("b5wal: invalid quarantine request")
	}
	if err := os.MkdirAll(store.Directory, 0o700); err != nil {
		return err
	}
	sources := []string{source}
	if strings.HasSuffix(source, ".wal") {
		sources = append(sources, strings.TrimSuffix(source, ".wal")+".manifest")
	}
	moved := 0
	for _, candidate := range sources {
		target := filepath.Join(store.Directory, filepath.Base(candidate)+".quarantine")
		if err := os.Rename(candidate, target); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		moved++
	}
	if moved == 0 {
		return errors.New("b5wal: quarantine source does not exist")
	}
	reason := []byte(cause.Error() + "\n")
	reasonPath := filepath.Join(store.Directory, filepath.Base(source)+".quarantine.reason")
	reasonFile, err := os.OpenFile(reasonPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err = reasonFile.Write(reason); err == nil {
		err = reasonFile.Sync()
	}
	err = errors.Join(err, reasonFile.Close())
	if err != nil {
		return err
	}
	if err := SyncDirectory(store.Directory); err != nil {
		return err
	}
	if sourceDirectory := filepath.Dir(source); sourceDirectory != store.Directory {
		return SyncDirectory(sourceDirectory)
	}
	return nil
}
