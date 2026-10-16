package adminapi

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
)

const taskGrantSQLDomain = "agentsql:task-grant-sql:v1"

func taskGrantSQLAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) == 0 {
		return nil, errors.New("task grant encryption key unavailable")
	}
	digest := sha256.Sum256(append(append([]byte(nil), key...), []byte(taskGrantSQLDomain)...))
	block, err := aes.NewCipher(digest[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func sealTaskGrantSQL(key []byte, sqlText string) (string, error) {
	aead, err := taskGrantSQLAEAD(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := aead.Seal(nonce, nonce, []byte(sqlText), []byte(taskGrantSQLDomain))
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func openTaskGrantSQL(key []byte, encoded string) (string, error) {
	aead, err := taskGrantSQLAEAD(key)
	if err != nil {
		return "", err
	}
	sealed, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(sealed) <= aead.NonceSize() {
		return "", errors.New("invalid task grant SQL ciphertext")
	}
	plain, err := aead.Open(nil, sealed[:aead.NonceSize()], sealed[aead.NonceSize():], []byte(taskGrantSQLDomain))
	if err != nil {
		return "", errors.New("invalid task grant SQL ciphertext")
	}
	return string(plain), nil
}
