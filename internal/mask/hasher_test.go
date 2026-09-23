package mask

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestHMACSHA256RFC4231Vector(t *testing.T) {
	key := make([]byte, 20)
	for index := range key {
		key[index] = 0x0b
	}

	digest := hmacSHA256(key, []byte("Hi There"))
	actual := hex.EncodeToString(digest[:])
	require.Equal(t, "b0344c61d8db38535ca8afceaf0bf12b881dc200c9833da726e9376c2e32cff7", actual)
}

func TestHasherKeyValidationAndDefensiveCopy(t *testing.T) {
	_, err := newLegacyHasher(nil)
	require.ErrorIs(t, err, ErrHashKeyRequired)
	_, err = newLegacyHasher([]byte{})
	require.ErrorIs(t, err, ErrHashKeyRequired)
	_, err = newLegacyHasher([]byte(strings.Repeat("k", 31)))
	require.ErrorIs(t, err, ErrHashKeyTooShort)

	key := []byte("0123456789abcdef0123456789abcdef")
	hash, err := newLegacyHasher(key)
	require.NoError(t, err)
	expected := hash.Fingerprint("Alice Zhang")
	key[0] = 'X'
	require.Equal(t, expected, hash.Fingerprint("Alice Zhang"))

	optionKey := []byte("abcdef0123456789abcdef0123456789")
	optionKeySnapshot := append([]byte(nil), optionKey...)
	option := WithHashKey(optionKey)
	optionKey[0] = 'X'
	redactor, err := NewRedactor([]Rule{{
		Column: "name", SensitiveType: TypeGeneric, Algorithm: AlgoHash,
	}}, option)
	require.NoError(t, err)
	expectedHasher, err := newLegacyHasher(optionKeySnapshot)
	require.NoError(t, err)
	result, _ := redactor.Apply(model.QueryResult{Columns: []string{"name"}, Rows: [][]string{{"Alice Zhang"}}})
	require.Equal(t, expectedHasher.Fingerprint("Alice Zhang"), result.Rows[0][0])
}

func TestHasherFingerprintGoldenShapeDeterminismAndDomain(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	hash, err := newLegacyHasher(key)
	require.NoError(t, err)

	fingerprint := hash.Fingerprint("Alice Zhang")
	require.Equal(t, "h.d119be750b2bb0be4dd7e2fc9fdae30f", fingerprint)
	require.Len(t, fingerprint, 34)
	require.True(t, strings.HasPrefix(fingerprint, "h."))
	require.Equal(t, strings.ToLower(fingerprint), fingerprint)
	_, err = hex.DecodeString(fingerprint[2:])
	require.NoError(t, err)
	require.Equal(t, fingerprint, hash.Fingerprint("Alice Zhang"))
	require.NotEqual(t, fingerprint, hash.Fingerprint("Alice Zhang!"))
	require.NotEqual(t, hash.Fingerprint("Alice"), hash.Fingerprint("alice"))
	require.NotEqual(t, hash.Fingerprint("é"), hash.Fingerprint("e\u0301"))

	otherHash, err := newLegacyHasher([]byte("abcdef0123456789abcdef0123456789"))
	require.NoError(t, err)
	require.NotEqual(t, fingerprint, otherHash.Fingerprint("Alice Zhang"))

	rawSum := hmacSHA256(key, []byte("Alice Zhang"))
	rawFingerprint := "h." + hex.EncodeToString(rawSum[:16])
	require.NotEqual(t, rawFingerprint, fingerprint)
}

func TestGenericHashUsesRawBytesAndSkipsEmptySentinels(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	redactor, err := NewRedactor([]Rule{{
		Column: "name", SensitiveType: TypeGeneric, Algorithm: AlgoHash,
	}}, WithHashKey(key))
	require.NoError(t, err)

	input := model.QueryResult{
		Columns: []string{"name"},
		Rows: [][]string{
			{"Alice Zhang"},
			{" alice "},
			{"alice"},
			{""},
			{" \t "},
			{"NULL"},
			{" null "},
			{"<nil>"},
			{" <NIL> "},
		},
	}
	result, report := redactor.Apply(input)

	require.Equal(t, "h.d119be750b2bb0be4dd7e2fc9fdae30f", result.Rows[0][0])
	require.Equal(t, "h.ee9f89cd24a1a0c7e654b6b9c32d3686", result.Rows[1][0])
	require.Equal(t, "h.98f07b770db27cae56166382bac903ed", result.Rows[2][0])
	require.NotEqual(t, result.Rows[1][0], result.Rows[2][0])
	for rowIndex := 3; rowIndex < len(input.Rows); rowIndex++ {
		require.Equal(t, input.Rows[rowIndex][0], result.Rows[rowIndex][0])
	}
	require.Equal(t, map[int]SensitiveType{0: TypeGeneric}, report.TouchedColumns)
	require.Equal(t, 3, report.MaskedCells)
}

func TestRedactorConcurrentApplyIsStable(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	redactor, err := NewRedactor([]Rule{{
		Column: "value", SensitiveType: TypeGeneric, Algorithm: AlgoHash,
	}}, WithHashKey(key))
	require.NoError(t, err)

	values := []string{"same", "same", "different", " alice ", "NULL"}
	expected := make([]string, len(values))
	for index, value := range values {
		result, _ := redactor.Apply(model.QueryResult{Columns: []string{"value"}, Rows: [][]string{{value}}})
		expected[index] = result.Rows[0][0]
	}

	const goroutines = 64
	errorsChannel := make(chan error, goroutines)
	var waitGroup sync.WaitGroup
	for goroutine := 0; goroutine < goroutines; goroutine++ {
		waitGroup.Add(1)
		go func(offset int) {
			defer waitGroup.Done()
			for iteration := 0; iteration < 100; iteration++ {
				index := (offset + iteration) % len(values)
				result, _ := redactor.Apply(model.QueryResult{
					Columns: []string{"value"}, Rows: [][]string{{values[index]}},
				})
				if result.Rows[0][0] != expected[index] {
					errorsChannel <- fmt.Errorf("value %q: got %q, want %q", values[index], result.Rows[0][0], expected[index])
					return
				}
			}
		}(goroutine)
	}
	waitGroup.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		require.NoError(t, err)
	}
	require.Equal(t, expected[0], expected[1])
	require.NotEqual(t, expected[0], expected[2])
}

func TestHashKeyErrorsSupportErrorsIs(t *testing.T) {
	_, err := newLegacyHasher(nil)
	require.True(t, errors.Is(err, ErrHashKeyRequired))
	_, err = newLegacyHasher([]byte(strings.Repeat("k", 31)))
	require.True(t, errors.Is(err, ErrHashKeyTooShort))
}

func TestHasherUTF8AndVersionedGolden(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	legacy, err := newLegacyHasher(key)
	require.NoError(t, err)
	require.Equal(t, "h.14c8fcca44f1275bb73f78a452c87917", legacy.Fingerprint("数据库🔐"))

	versioned, err := newVersionedHasher(2, key)
	require.NoError(t, err)
	require.Equal(t, 2, versioned.Version())
	require.Equal(t, "h.2.60743d03cb3db32dd8406a8ae910d3d4", versioned.Fingerprint("数据库🔐"))
	ok, err := VerifyFingerprint(2, key, "数据库🔐", versioned.Fingerprint("数据库🔐"))
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = VerifyFingerprint(2, key, "数据库🔐", "h.2.deadbeef")
	require.NoError(t, err)
	require.False(t, ok)
}

func TestVersionedHasherValidation(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	for _, version := range []int{-1, 0, 10000} {
		_, err := newVersionedHasher(version, key)
		require.Error(t, err)
		_, err = VerifyFingerprint(version, key, "value", "candidate")
		require.Error(t, err)
	}
	_, err := newVersionedHasher(2, nil)
	require.ErrorIs(t, err, ErrHashKeyRequired)
}
