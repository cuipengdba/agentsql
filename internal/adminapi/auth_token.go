package adminapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	adminTokenLifetime = 12 * time.Hour
	adminTokenPurpose  = "agentsql-admin-token-v1"
)

var (
	ErrInvalidAdminCredentials = errors.New("invalid administrator credentials")
	ErrInvalidAdminToken       = errors.New("invalid administrator token")
)

type tokenPayload struct {
	IssuedAt int64  `json:"iat"`
	Expires  int64  `json:"exp"`
	JTI      string `json:"jti"`
}

// DeriveTokenKey derives the admin-token signing key from AGENTSQL_SECRET.
func DeriveTokenKey(secret []byte) []byte {
	digest := hmac.New(sha256.New, secret)
	_, _ = digest.Write([]byte(adminTokenPurpose))
	return digest.Sum(nil)
}

// AdminCredentialsFromEnv loads the administrator username/password without
// persisting either value.
func AdminCredentialsFromEnv() (username, password string, err error) {
	username = os.Getenv("AGENTSQL_ADMIN_USER")
	if username == "" {
		username = "admin"
	}
	password = os.Getenv("AGENTSQL_ADMIN_PASSWORD")
	if password == "" {
		return "", "", fmt.Errorf("AGENTSQL_ADMIN_PASSWORD is required")
	}
	return username, password, nil
}

func issueAdminToken(key []byte, now time.Time, jti string) (string, time.Time, error) {
	if len(key) == 0 || now.IsZero() || strings.TrimSpace(jti) == "" {
		return "", time.Time{}, fmt.Errorf("issue admin token: invalid token inputs")
	}
	expires := now.Add(adminTokenLifetime)
	payload, err := json.Marshal(tokenPayload{IssuedAt: now.Unix(), Expires: expires.Unix(), JTI: jti})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("marshal admin token: %w", err)
	}
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	signature := signToken(key, encodedPayload)
	return encodedPayload + "." + base64.RawURLEncoding.EncodeToString(signature), expires, nil
}

func validateAdminToken(key []byte, token string, now time.Time) error {
	if len(key) == 0 || strings.TrimSpace(token) != token || token == "" || now.IsZero() {
		return ErrInvalidAdminToken
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ErrInvalidAdminToken
	}
	provided, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(provided) != sha256.Size {
		return ErrInvalidAdminToken
	}
	expected := signToken(key, parts[0])
	if subtle.ConstantTimeCompare(provided, expected) != 1 {
		return ErrInvalidAdminToken
	}
	encodedPayload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return ErrInvalidAdminToken
	}
	var payload tokenPayload
	if err := json.Unmarshal(encodedPayload, &payload); err != nil || payload.IssuedAt <= 0 || payload.Expires <= payload.IssuedAt || strings.TrimSpace(payload.JTI) == "" {
		return ErrInvalidAdminToken
	}
	if now.Unix() >= payload.Expires {
		return ErrInvalidAdminToken
	}
	return nil
}

func signToken(key []byte, encodedPayload string) []byte {
	digest := hmac.New(sha256.New, key)
	_, _ = digest.Write([]byte(encodedPayload))
	return digest.Sum(nil)
}
