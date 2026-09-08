package store

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

// APIKeyPrefix identifies AgentSQL plaintext API keys.
const APIKeyPrefix = "asql_"

var errInvalidAPIKeyHash = errors.New("api_key_hash must be a SHA-256 hexadecimal digest")

// GenerateAPIKey creates a random API key and its SHA-256 hexadecimal digest.
// Callers must return the plaintext once and persist only the digest.
func GenerateAPIKey() (plaintext string, hash string, err error) {
	randomBytes := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, randomBytes); err != nil {
		return "", "", fmt.Errorf("generate API key randomness: %w", err)
	}

	plaintext = APIKeyPrefix + base64.RawURLEncoding.EncodeToString(randomBytes)
	return plaintext, HashAPIKey(plaintext), nil
}

// HashAPIKey returns the lowercase hexadecimal SHA-256 digest of plaintext.
func HashAPIKey(plaintext string) string {
	digest := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(digest[:])
}

func validateAPIKeyHash(hash string) error {
	if len(hash) != sha256.Size*2 {
		return fmt.Errorf("validate API key hash length: %w", errInvalidAPIKeyHash)
	}
	decoded, err := hex.DecodeString(hash)
	if err != nil {
		return fmt.Errorf("decode API key hash: %w", errors.Join(errInvalidAPIKeyHash, err))
	}
	if len(decoded) != sha256.Size {
		return fmt.Errorf("validate API key hash size: %w", errInvalidAPIKeyHash)
	}
	return nil
}
