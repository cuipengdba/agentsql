package chatbi

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOriginalAssistantSignEnvelopeIsParseableButNotAnIdentity(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	identity := &Identity{privateKey: key}
	// Anyone can construct this SDK header from the public key. The decoder
	// intentionally returns payload data, never an authenticated principal.
	encode := func(raw []byte) string {
		t.Helper()
		var parts []string
		for start := 0; start < len(raw); start += 245 {
			end := start + 245
			if end > len(raw) {
				end = len(raw)
			}
			ciphertext, err := rsa.EncryptPKCS1v15(rand.Reader, &key.PublicKey, raw[start:end])
			require.NoError(t, err)
			parts = append(parts, base64.StdEncoding.EncodeToString(ciphertext))
		}
		return strings.Join(parts, "|")
	}
	payload := assistantSignPayload{
		AssistantID: "12", SignTime: "2026-10-30T00:00:00+08:00",
		Target: "https://app.example", UserAgent: strings.Repeat("A", 500),
		Timezone: "Asia/Shanghai",
	}
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	header := encode(raw)
	require.Contains(t, header, "|")
	decoded, err := decodeAssistantSign(identity, header)
	require.NoError(t, err)
	require.Equal(t, payload, decoded)

	_, err = decodeAssistantSign(identity, header+"|")
	require.ErrorIs(t, err, ErrUnauthenticated)
	_, err = decodeAssistantSign(identity, header+"evil")
	require.ErrorIs(t, err, ErrUnauthenticated)
	_, err = decodeAssistantSign(identity, strings.Repeat(header, 20))
	require.ErrorIs(t, err, ErrUnauthenticated)
	_, err = decodeAssistantSign(identity, encode([]byte(`{"assistant_id":12,"unexpected":true}`)))
	require.ErrorIs(t, err, ErrUnauthenticated)
	_, err = decodeAssistantSign(identity, encode([]byte(`{"assistant_id":12} trailing`)))
	require.ErrorIs(t, err, ErrUnauthenticated)
	_, err = decodeAssistantSign(identity, encode([]byte(`{"assistant_id":0}`)))
	require.ErrorIs(t, err, ErrUnauthenticated)
}
