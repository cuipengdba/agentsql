package config

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/stretchr/testify/require"
)

func TestBuildRedactionAssembly(t *testing.T) {
	empty, err := BuildRedactionAssembly(RedactionConfig{})
	require.NoError(t, err)
	require.Equal(t, RedactionAssembly{}, empty)

	keyBytes := []byte("0123456789abcdef0123456789abcdef")
	key := string(keyBytes)
	assembly, err := BuildRedactionAssembly(RedactionConfig{HashKey: key})
	require.NoError(t, err)
	require.Len(t, assembly.PlanOptions, 1)
	require.NotNil(t, assembly.Verify)

	redactor, err := mask.NewRedactor([]mask.Rule{{
		Column: "secret", SensitiveType: mask.TypeGeneric, Algorithm: mask.AlgoHash,
	}}, assembly.PlanOptions...)
	require.NoError(t, err)
	_ = redactor
	keyBytes[0] = 'X'
	candidate := "h.b27b652173ab834484d483e570d532d8"
	ok, err := assembly.Verify(1, "value", candidate)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = assembly.Verify(2, "value", candidate)
	require.Error(t, err)
	require.False(t, ok)
}

func TestBuildManifestAssemblyUsesSingleMaskGate(t *testing.T) {
	activeOne, err := resolveHashKeys(&RedactionHashKeysConfig{
		ActiveVersion: 1,
		Keys: []RedactionHashKeySpec{
			{ID: 1, KeyB64: stringPointer(canonicalTestKey(0))},
			{ID: 2, KeyB64: stringPointer(canonicalTestKey(32))},
		},
	})
	require.NoError(t, err)
	defer activeOne.Clear()
	assembly, err := BuildRedactionAssembly(activeOne)
	require.NoError(t, err)
	require.Equal(t, "available", assembly.Observed.Status)
	require.Equal(t, 1, assembly.Observed.ActiveVersion)
	require.Len(t, assembly.Observed.Keys, 2)

	candidate, err := mask.VerifyFingerprint(2, diverseTestBytes(32), "value", "h.2.not-a-match")
	require.NoError(t, err)
	require.False(t, candidate)
	matched, err := assembly.Verify(2, "value", maskFingerprintForTest(t, 2, diverseTestBytes(32), "value"))
	require.NoError(t, err)
	require.True(t, matched, "standby keys are verify-only and do not pass the active gate")

	activeTwo, err := resolveHashKeys(&RedactionHashKeysConfig{
		ActiveVersion: 2,
		Keys: []RedactionHashKeySpec{
			{ID: 1, KeyB64: stringPointer(canonicalTestKey(0))},
			{ID: 2, KeyB64: stringPointer(canonicalTestKey(32))},
		},
	})
	require.NoError(t, err)
	defer activeTwo.Clear()
	_, err = BuildRedactionAssembly(activeTwo)
	require.ErrorIs(t, err, mask.ErrVersionedActiveNotEnabled)
}

func maskFingerprintForTest(t *testing.T, version int, key []byte, value string) string {
	t.Helper()
	mac := hmac.New(sha256.New, key)
	_, err := mac.Write([]byte(fmt.Sprintf("agentsql:redaction-hash:v%d\x00", version)))
	require.NoError(t, err)
	_, err = mac.Write([]byte(value))
	require.NoError(t, err)
	return fmt.Sprintf("h.%d.%s", version, hex.EncodeToString(mac.Sum(nil)[:16]))
}
