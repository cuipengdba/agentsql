package chatbi

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"
)

// The original SQLBot browser SDK encrypts its assistant bootstrap payload in
// 245-byte RSA PKCS#1 v1.5 chunks joined with '|'. Decrypting this public-key
// envelope does not authenticate the browser, host service or business user.
// Callers must not issue a data token from this result alone.
type assistantSignPayload struct {
	AssistantID ID     `json:"assistant_id"`
	SignTime    string `json:"sign_time"`
	Target      string `json:"target"`
	Webdriver   bool   `json:"webdriver"`
	UserAgent   string `json:"user_agent"`
	Timezone    string `json:"timezone"`
}

func decodeAssistantSign(identity *Identity, header string) (assistantSignPayload, error) {
	deny := func() (assistantSignPayload, error) { return assistantSignPayload{}, ErrUnauthenticated }
	if identity == nil || identity.privateKey == nil || header == "" || len(header) > 8192 {
		return deny()
	}
	parts := strings.Split(header, "|")
	if len(parts) == 0 || len(parts) > 16 {
		return deny()
	}
	var plaintext []byte
	for _, part := range parts {
		if part == "" || len(part) > 1024 {
			return deny()
		}
		ciphertext, err := base64.StdEncoding.Strict().DecodeString(part)
		if err != nil || len(ciphertext) != identity.privateKey.Size() {
			return deny()
		}
		chunk, err := rsa.DecryptPKCS1v15(rand.Reader, identity.privateKey, ciphertext)
		if err != nil || len(chunk) == 0 || len(plaintext)+len(chunk) > 4096 {
			return deny()
		}
		plaintext = append(plaintext, chunk...)
	}
	if !utf8.Valid(plaintext) {
		return deny()
	}
	decoder := json.NewDecoder(bytes.NewReader(plaintext))
	decoder.DisallowUnknownFields()
	var payload assistantSignPayload
	if decoder.Decode(&payload) != nil || decoder.Decode(new(any)) != io.EOF ||
		!payload.AssistantID.Valid() || len(payload.SignTime) > 64 ||
		len(payload.Target) > 255 || len(payload.UserAgent) > 1024 ||
		len(payload.Timezone) > 128 {
		return deny()
	}
	return payload, nil
}
