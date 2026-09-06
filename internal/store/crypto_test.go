package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGenerateAPIKey(t *testing.T) {
	firstPlaintext, firstHash, err := GenerateAPIKey()
	require.NoError(t, err)
	secondPlaintext, secondHash, err := GenerateAPIKey()
	require.NoError(t, err)

	require.True(t, strings.HasPrefix(firstPlaintext, apiKeyPrefix))
	require.Len(t, firstHash, sha256.Size*2)
	require.NotEqual(t, firstPlaintext, secondPlaintext)
	require.NotEqual(t, firstHash, secondHash)
	digest := sha256.Sum256([]byte(firstPlaintext))
	require.Equal(t, hex.EncodeToString(digest[:]), firstHash)
	require.NotContains(t, firstHash, firstPlaintext)
}

func TestPasswordCipherRoundTripAndAuthentication(t *testing.T) {
	t.Setenv(secretEnvironmentVariable, testSecret)
	passwordCipher, err := NewPasswordCipherFromEnv()
	require.NoError(t, err)

	plaintext := "database-password"
	first, err := passwordCipher.Encrypt(plaintext)
	require.NoError(t, err)
	second, err := passwordCipher.Encrypt(plaintext)
	require.NoError(t, err)
	require.NotEqual(t, plaintext, first)
	require.NotEqual(t, first, second)

	decrypted, err := passwordCipher.Decrypt(first)
	require.NoError(t, err)
	require.Equal(t, plaintext, decrypted)

	replacement := "A"
	if first[0] == 'A' {
		replacement = "B"
	}
	tampered := replacement + first[1:]
	_, err = passwordCipher.Decrypt(tampered)
	require.True(t, errors.Is(err, ErrInvalidCiphertext))
}

func TestOpenFailsWhenAgentSQLSecretIsMissing(t *testing.T) {
	t.Setenv(secretEnvironmentVariable, "")
	databasePath := filepath.Join(t.TempDir(), "agentsql.db")

	_, err := Open(context.Background(), databasePath)
	require.True(t, errors.Is(err, ErrSecretMissing))
	require.NoFileExists(t, databasePath)
}

func TestPasswordCipherRejectsInvalidSecretLength(t *testing.T) {
	t.Setenv(secretEnvironmentVariable, "too-short")

	_, err := NewPasswordCipherFromEnv()
	require.True(t, errors.Is(err, ErrSecretLength))
}
