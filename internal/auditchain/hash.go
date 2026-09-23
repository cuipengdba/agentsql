package auditchain

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// SHA256Hex returns lowercase hex(SHA-256(envelope)).
func SHA256Hex(envelope []byte) string {
	digest := sha256.Sum256(envelope)
	return hex.EncodeToString(digest[:])
}

// HMACSHA256Hex returns lowercase hex(HMAC-SHA256(key, envelope)).
func HMACSHA256Hex(key, envelope []byte) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(envelope)
	return hex.EncodeToString(mac.Sum(nil))
}

// Hash computes the keyless hash when key is nil and HMAC-SHA256 otherwise.
// A present but empty key is intentionally distinct from a nil key.
func Hash(envelope, key []byte) string {
	if key == nil {
		return SHA256Hex(envelope)
	}
	return HMACSHA256Hex(key, envelope)
}

// HashEnvelope selects the hash algorithm by the envelope mode and rejects a
// missing HMAC key or an unexpected key in keyless mode.
func HashEnvelope(envelope Envelope, encoded, key []byte) (string, error) {
	switch envelope.AlgorithmMode {
	case AlgorithmSHA256:
		if key != nil {
			return "", fmt.Errorf("sha256 mode does not use a key")
		}
		return SHA256Hex(encoded), nil
	case AlgorithmHMACSHA256:
		if key == nil {
			return "", fmt.Errorf("hmac-sha256 key is unavailable for version %d", envelope.KeyVersion)
		}
		return HMACSHA256Hex(key, encoded), nil
	default:
		return "", fmt.Errorf("unknown algorithm mode %q", envelope.AlgorithmMode)
	}
}
