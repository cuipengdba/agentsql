package mask

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

const minimumHashKeyBytes = 32

type hasher struct {
	key []byte
}

func newHasher(key []byte) (*hasher, error) {
	if len(key) == 0 {
		return nil, ErrHashKeyRequired
	}
	if len(key) < minimumHashKeyBytes {
		return nil, ErrHashKeyTooShort
	}
	return &hasher{key: append([]byte(nil), key...)}, nil
}

func (h *hasher) fingerprint(value string) string {
	sum := hmacSHA256(h.key, []byte("agentsql:redaction-hash:v1\x00"), []byte(value))
	return "h." + hex.EncodeToString(sum[:16])
}

func hmacSHA256(key []byte, messages ...[]byte) []byte {
	mac := hmac.New(sha256.New, key)
	for _, message := range messages {
		_, _ = mac.Write(message)
	}
	return mac.Sum(nil)
}
