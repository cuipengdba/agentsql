package mask

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

const (
	minimumHashKeyBytes = 32
	legacyHashDomain    = "agentsql:redaction-hash:v1\x00"
)

type ActiveHasher interface {
	Version() int
	Fingerprint(value string) string
}

type legacyHasher struct {
	key []byte
}

func newLegacyHasher(key []byte) (*legacyHasher, error) {
	if err := validateHashKey(key); err != nil {
		return nil, err
	}
	return &legacyHasher{key: append([]byte(nil), key...)}, nil
}

func (h *legacyHasher) Version() int { return 1 }

func (h *legacyHasher) Fingerprint(value string) string {
	sum := hmacSHA256(h.key, []byte(legacyHashDomain), []byte(value))
	return "h." + hex.EncodeToString(sum[:16])
}

type versionedHasher struct {
	version int
	key     []byte
	domain  []byte
}

func newVersionedHasher(version int, key []byte) (*versionedHasher, error) {
	if version < 2 || version > 9999 {
		return nil, fmt.Errorf("hash version must be between 2 and 9999")
	}
	if err := validateHashKey(key); err != nil {
		return nil, err
	}
	return &versionedHasher{
		version: version,
		key:     append([]byte(nil), key...),
		domain:  []byte(fmt.Sprintf("agentsql:redaction-hash:v%d\x00", version)),
	}, nil
}

func (h *versionedHasher) Version() int { return h.version }

func (h *versionedHasher) Fingerprint(value string) string {
	sum := hmacSHA256(h.key, h.domain, []byte(value))
	return fmt.Sprintf("h.%d.%s", h.version, hex.EncodeToString(sum[:16]))
}

func VerifyFingerprint(version int, key []byte, value, candidate string) (bool, error) {
	var active ActiveHasher
	var err error
	if version == 1 {
		active, err = newLegacyHasher(key)
	} else {
		active, err = newVersionedHasher(version, key)
	}
	if err != nil {
		return false, err
	}
	return hmac.Equal([]byte(active.Fingerprint(value)), []byte(candidate)), nil
}

func validateHashKey(key []byte) error {
	if len(key) == 0 {
		return ErrHashKeyRequired
	}
	if len(key) < minimumHashKeyBytes {
		return ErrHashKeyTooShort
	}
	return nil
}

func hmacSHA256(key []byte, messages ...[]byte) [32]byte {
	mac := hmac.New(sha256.New, key)
	for _, message := range messages {
		_, _ = mac.Write(message)
	}
	var digest [32]byte
	copy(digest[:], mac.Sum(nil))
	return digest
}
