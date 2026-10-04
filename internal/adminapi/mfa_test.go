package adminapi

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMFASecretEncryptionAndTOTPWindow(t *testing.T) {
	key := DeriveTokenKey([]byte(adminTestSecret))
	ciphertext, err := encryptMFASecret(key, "JBSWY3DPEHPK3PXP")
	require.NoError(t, err)
	require.NotContains(t, ciphertext, "JBSWY3DPEHPK3PXP")
	plaintext, err := decryptMFASecret(key, ciphertext)
	require.NoError(t, err)
	require.Equal(t, "JBSWY3DPEHPK3PXP", plaintext)
	_, err = decryptMFASecret(DeriveTokenKey([]byte("different-secret-material-123456")), ciphertext)
	require.Error(t, err)

	// RFC 6238 SHA-1 test secret at T=59 yields 94287082 for 8 digits,
	// therefore the six-digit truncation is 287082.
	counter, ok := validateTOTP("GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", "287082", time.Unix(59, 0))
	require.True(t, ok)
	require.Equal(t, int64(1), counter)
	_, ok = validateTOTP("GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", fmt.Sprintf("%06d", 287083), time.Unix(59, 0))
	require.False(t, ok)
}

func TestRecoveryCodesAreNormalizedAndDistinct(t *testing.T) {
	codes, hashes, err := generateRecoveryCodes(10)
	require.NoError(t, err)
	require.Len(t, codes, 10)
	require.Len(t, hashes, 10)
	require.Len(t, mapSet(hashes), 10)
	for _, code := range codes {
		require.Len(t, normalizeRecoveryCode(code), 16)
	}
}

func mapSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}
