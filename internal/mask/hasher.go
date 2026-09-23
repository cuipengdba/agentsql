package mask

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	minimumHashKeyBytes = 32
	legacyHashDomain    = "agentsql:redaction-hash:v1\x00"
	keyCommitmentDomain = "agentsql:redaction-key-commitment:v1\x00"
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
	prefix  string
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
		prefix:  "h." + strconv.Itoa(version) + ".",
	}, nil
}

func (h *versionedHasher) Version() int { return h.version }

func (h *versionedHasher) Fingerprint(value string) string {
	sum := hmacSHA256(h.key, h.domain, []byte(value))
	return h.prefix + hex.EncodeToString(sum[:16])
}

var ErrInvalidFingerprint = errors.New("invalid redaction fingerprint")

// ParseFingerprint validates the canonical fingerprint grammar and returns its
// key version. Legacy fingerprints use version 1; versioned fingerprints use a
// canonical decimal version in the range 2..9999. It does not verify the HMAC.
func ParseFingerprint(candidate string) (int, error) {
	if len(candidate) == 34 && strings.HasPrefix(candidate, "h.") && isLowerHex(candidate[2:]) {
		return 1, nil
	}
	if len(candidate) < 36 || !strings.HasPrefix(candidate, "h.") {
		return 0, ErrInvalidFingerprint
	}
	versionEnd := strings.IndexByte(candidate[2:], '.')
	if versionEnd < 1 {
		return 0, ErrInvalidFingerprint
	}
	versionText := candidate[2 : 2+versionEnd]
	if versionText[0] == '0' {
		return 0, ErrInvalidFingerprint
	}
	version, err := strconv.Atoi(versionText)
	if err != nil || version < 2 || version > 9999 {
		return 0, ErrInvalidFingerprint
	}
	digest := candidate[3+versionEnd:]
	if len(digest) != 32 || !isLowerHex(digest) {
		return 0, ErrInvalidFingerprint
	}
	return version, nil
}

func isLowerHex(value string) bool {
	for index := 0; index < len(value); index++ {
		if (value[index] < '0' || value[index] > '9') && (value[index] < 'a' || value[index] > 'f') {
			return false
		}
	}
	return true
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

// KeyCommitment returns the non-secret, stable commitment stored in the key
// registry. It never retains or mutates keyMaterial.
func KeyCommitment(keyMaterial []byte) string {
	digest := sha256.New()
	_, _ = digest.Write([]byte(keyCommitmentDomain))
	_, _ = digest.Write(keyMaterial)
	return hex.EncodeToString(digest.Sum(nil))
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
