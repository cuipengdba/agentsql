package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
)

const (
	secretEnvironmentVariable = "AGENTSQL_SECRET"
	requiredSecretBytes        = 32
	passwordAdditionalData     = "agentsql:datasource-password:v1"
)

var (
	// ErrSecretMissing indicates that AGENTSQL_SECRET is unset or empty.
	ErrSecretMissing = errors.New("AGENTSQL_SECRET is required")
	// ErrSecretLength indicates that AGENTSQL_SECRET is not a 32-byte AES key.
	ErrSecretLength = errors.New("AGENTSQL_SECRET must be exactly 32 bytes")
	// ErrInvalidCiphertext indicates that an encrypted password cannot be authenticated.
	ErrInvalidCiphertext = errors.New("invalid datasource password ciphertext")
	// ErrCipherUnavailable indicates that a password cipher was not initialized.
	ErrCipherUnavailable = errors.New("datasource password cipher is not initialized")
)

// PasswordCipher encrypts datasource passwords with AES-256-GCM.
type PasswordCipher struct {
	aead cipher.AEAD
}

// NewPasswordCipherFromEnv constructs a password cipher from AGENTSQL_SECRET.
func NewPasswordCipherFromEnv() (*PasswordCipher, error) {
	secret, present := os.LookupEnv(secretEnvironmentVariable)
	if !present || secret == "" {
		return nil, fmt.Errorf("load encryption secret: %w", ErrSecretMissing)
	}
	if len([]byte(secret)) != requiredSecretBytes {
		return nil, fmt.Errorf("validate encryption secret: %w", ErrSecretLength)
	}

	block, err := aes.NewCipher([]byte(secret))
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create AES-GCM cipher: %w", err)
	}
	return &PasswordCipher{aead: aead}, nil
}

// Encrypt encrypts a plaintext datasource password with a fresh random nonce.
func (passwordCipher *PasswordCipher) Encrypt(plaintext string) (string, error) {
	if passwordCipher == nil || passwordCipher.aead == nil {
		return "", fmt.Errorf("encrypt datasource password: %w", ErrCipherUnavailable)
	}
	nonce := make([]byte, passwordCipher.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate AES-GCM nonce: %w", err)
	}
	sealed := passwordCipher.aead.Seal(nonce, nonce, []byte(plaintext), []byte(passwordAdditionalData))
	return base64.RawStdEncoding.EncodeToString(sealed), nil
}

// Decrypt authenticates and decrypts a datasource password.
func (passwordCipher *PasswordCipher) Decrypt(encoded string) (string, error) {
	if passwordCipher == nil || passwordCipher.aead == nil {
		return "", fmt.Errorf("decrypt datasource password: %w", ErrCipherUnavailable)
	}
	sealed, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decode datasource password: %w", errors.Join(ErrInvalidCiphertext, err))
	}
	nonceSize := passwordCipher.aead.NonceSize()
	if len(sealed) < nonceSize+passwordCipher.aead.Overhead() {
		return "", fmt.Errorf("validate datasource password ciphertext: %w", ErrInvalidCiphertext)
	}

	plaintext, err := passwordCipher.aead.Open(
		nil,
		sealed[:nonceSize],
		sealed[nonceSize:],
		[]byte(passwordAdditionalData),
	)
	if err != nil {
		return "", fmt.Errorf("decrypt datasource password: %w", errors.Join(ErrInvalidCiphertext, err))
	}
	return string(plaintext), nil
}
