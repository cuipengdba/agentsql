package adminapi

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/store"
)

const totpPeriod = int64(30)

func (handler *Handler) authConfig(writer http.ResponseWriter, _ *http.Request) {
	handler.ok(writer, map[string]any{
		"mfa_enabled":  handler.deps.Config.Auth.MFA.Enabled,
		"oidc_enabled": handler.deps.Config.Auth.OIDC.Enabled,
		"ldap_enabled": handler.deps.Config.Auth.LDAP.Enabled,
	})
}

func (handler *Handler) mfaStatus(writer http.ResponseWriter, request *http.Request) {
	principal, ok := requestPrincipal(request)
	if !ok {
		handler.fail(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	record, err := handler.deps.Runtime.Store.HumanAuth().MFA(request.Context(), principal.TenantID, principal.UserID)
	if errors.Is(err, store.ErrNotFound) {
		handler.ok(writer, map[string]any{"enabled": false, "pending": false})
		return
	}
	if err != nil {
		handler.internal(writer, err)
		return
	}
	handler.ok(writer, map[string]any{"enabled": record.Status == "enabled", "pending": record.Status == "pending"})
}

func (handler *Handler) mfaEnroll(writer http.ResponseWriter, request *http.Request) {
	if !handler.deps.Config.Auth.MFA.Enabled {
		handler.fail(writer, http.StatusNotFound, "not found")
		return
	}
	principal, ok := requestPrincipal(request)
	if !ok {
		handler.fail(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	if current, err := handler.deps.Runtime.Store.HumanAuth().MFA(request.Context(), principal.TenantID, principal.UserID); err == nil && current.Status == "enabled" {
		handler.fail(writer, http.StatusConflict, "disable existing MFA before re-enrollment")
		return
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		handler.internal(writer, err)
		return
	}
	secretBytes := make([]byte, 20)
	if _, err := io.ReadFull(rand.Reader, secretBytes); err != nil {
		handler.fail(writer, http.StatusInternalServerError, "internal error")
		return
	}
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secretBytes)
	ciphertext, err := encryptMFASecret(handler.tokenKey, secret)
	if err != nil {
		handler.internal(writer, err)
		return
	}
	codes, hashes, err := generateRecoveryCodes(10)
	if err != nil {
		handler.internal(writer, err)
		return
	}
	if err = handler.deps.Runtime.Store.HumanAuth().PutPendingMFA(request.Context(), store.MFARecord{
		TenantID: principal.TenantID, UserID: principal.UserID, SecretCiphertext: ciphertext,
	}, hashes); err != nil {
		handler.internal(writer, err)
		return
	}
	issuer := handler.deps.Config.Auth.MFA.Issuer
	otpauth := "otpauth://totp/" + url.PathEscape(issuer+":"+principal.Username) + "?" + url.Values{
		"secret": {secret}, "issuer": {issuer}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"},
	}.Encode()
	handler.ok(writer, map[string]any{"secret": secret, "otpauth_uri": otpauth, "recovery_codes": codes})
}

func (handler *Handler) mfaConfirm(writer http.ResponseWriter, request *http.Request) {
	if !handler.deps.Config.Auth.MFA.Enabled {
		handler.fail(writer, http.StatusNotFound, "not found")
		return
	}
	principal, ok := requestPrincipal(request)
	if !ok {
		handler.fail(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	var input struct {
		Code string `json:"code"`
	}
	if decodeJSON(writer, request, &input) != nil {
		handler.fail(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	record, err := handler.deps.Runtime.Store.HumanAuth().MFA(request.Context(), principal.TenantID, principal.UserID)
	if err != nil || record.Status != "pending" {
		handler.fail(writer, http.StatusUnauthorized, "invalid MFA code")
		return
	}
	secret, err := decryptMFASecret(handler.tokenKey, record.SecretCiphertext)
	if err != nil {
		handler.fail(writer, http.StatusUnauthorized, "invalid MFA code")
		return
	}
	counter, valid := validateTOTP(secret, input.Code, time.Now().UTC())
	if !valid || handler.deps.Runtime.Store.HumanAuth().EnableMFA(request.Context(), principal.TenantID, principal.UserID, counter) != nil {
		handler.fail(writer, http.StatusUnauthorized, "invalid MFA code")
		return
	}
	handler.ok(writer, map[string]bool{"enabled": true})
}

func (handler *Handler) mfaVerify(writer http.ResponseWriter, request *http.Request) {
	if !handler.deps.Config.Auth.MFA.Enabled {
		handler.fail(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	var input struct {
		ChallengeToken string `json:"challenge_token"`
		Code           string `json:"code,omitempty"`
		RecoveryCode   string `json:"recovery_code,omitempty"`
	}
	if decodeJSON(writer, request, &input) != nil || (input.Code == "") == (input.RecoveryCode == "") {
		handler.fail(writer, http.StatusBadRequest, "exactly one MFA code is required")
		return
	}
	hash, ok := opaqueTokenHash(input.ChallengeToken)
	if !ok {
		handler.fail(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	// Read the candidate identity without consuming the one-shot challenge.
	// The repository deliberately exposes only consume, so verify after a
	// provisional token lookup by consuming first; a failed factor requires a
	// fresh password login and does not permit online guessing against one token.
	challenge, err := handler.deps.Runtime.Store.HumanAuth().ConsumeLoginChallenge(request.Context(), hash, time.Now().UTC())
	if err != nil {
		handler.fail(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	record, err := handler.deps.Runtime.Store.HumanAuth().MFA(request.Context(), challenge.TenantID, challenge.UserID)
	if err != nil || record.Status != "enabled" {
		handler.fail(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	if input.Code != "" {
		secret, decryptErr := decryptMFASecret(handler.tokenKey, record.SecretCiphertext)
		counter, valid := validateTOTP(secret, input.Code, time.Now().UTC())
		if decryptErr != nil || !valid || counter <= record.LastCounter || handler.deps.Runtime.Store.HumanAuth().AdvanceMFACounter(request.Context(), record.TenantID, record.UserID, counter) != nil {
			handler.fail(writer, http.StatusUnauthorized, "unauthorized")
			return
		}
	} else {
		digest := sha256.Sum256([]byte(normalizeRecoveryCode(input.RecoveryCode)))
		if handler.deps.Runtime.Store.HumanAuth().ConsumeRecoveryCode(request.Context(), record.TenantID, record.UserID, hex.EncodeToString(digest[:]), time.Now().UTC()) != nil {
			handler.fail(writer, http.StatusUnauthorized, "unauthorized")
			return
		}
	}
	principal, err := handler.rbac.Principal(request.Context(), challenge.TenantID, challenge.UserID)
	if err != nil || principal.Username != challenge.Username {
		handler.fail(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	handler.issueSession(writer, request, principal)
}

func (handler *Handler) mfaDisable(writer http.ResponseWriter, request *http.Request) {
	principal, ok := requestPrincipal(request)
	if !ok {
		handler.fail(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	var input struct {
		Code         string `json:"code,omitempty"`
		RecoveryCode string `json:"recovery_code,omitempty"`
	}
	if decodeJSON(writer, request, &input) != nil || (input.Code == "") == (input.RecoveryCode == "") {
		handler.fail(writer, http.StatusBadRequest, "exactly one MFA code is required")
		return
	}
	record, err := handler.deps.Runtime.Store.HumanAuth().MFA(request.Context(), principal.TenantID, principal.UserID)
	if err != nil || record.Status != "enabled" {
		handler.fail(writer, http.StatusUnauthorized, "invalid MFA code")
		return
	}
	if input.Code != "" {
		secret, decryptErr := decryptMFASecret(handler.tokenKey, record.SecretCiphertext)
		counter, valid := validateTOTP(secret, input.Code, time.Now().UTC())
		if decryptErr != nil || !valid || counter <= record.LastCounter {
			handler.fail(writer, http.StatusUnauthorized, "invalid MFA code")
			return
		}
	} else {
		digest := sha256.Sum256([]byte(normalizeRecoveryCode(input.RecoveryCode)))
		if handler.deps.Runtime.Store.HumanAuth().ConsumeRecoveryCode(request.Context(), record.TenantID, record.UserID, hex.EncodeToString(digest[:]), time.Now().UTC()) != nil {
			handler.fail(writer, http.StatusUnauthorized, "invalid MFA code")
			return
		}
	}
	if err = handler.deps.Runtime.Store.HumanAuth().DisableMFA(request.Context(), principal.TenantID, principal.UserID); err != nil {
		handler.internal(writer, err)
		return
	}
	handler.ok(writer, map[string]bool{"enabled": false})
}

func encryptMFASecret(key []byte, plaintext string) (string, error) {
	digest := sha256.Sum256(append(append([]byte(nil), key...), []byte("agentsql-mfa-secret-v1")...))
	block, err := aes.NewCipher(digest[:])
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := aead.Seal(nonce, nonce, []byte(plaintext), []byte("agentsql:mfa:v1"))
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func decryptMFASecret(key []byte, encoded string) (string, error) {
	digest := sha256.Sum256(append(append([]byte(nil), key...), []byte("agentsql-mfa-secret-v1")...))
	block, err := aes.NewCipher(digest[:])
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	sealed, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(sealed) <= aead.NonceSize() {
		return "", errors.New("invalid MFA secret")
	}
	plain, err := aead.Open(nil, sealed[:aead.NonceSize()], sealed[aead.NonceSize():], []byte("agentsql:mfa:v1"))
	if err != nil {
		return "", errors.New("invalid MFA secret")
	}
	return string(plain), nil
}

func validateTOTP(secret, code string, now time.Time) (int64, bool) {
	if len(code) != 6 || strings.Trim(code, "0123456789") != "" {
		return 0, false
	}
	decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(secret))
	if err != nil {
		return 0, false
	}
	provided, _ := strconv.Atoi(code)
	base := now.Unix() / totpPeriod
	for offset := int64(-1); offset <= 1; offset++ {
		counter := base + offset
		var message [8]byte
		binary.BigEndian.PutUint64(message[:], uint64(counter))
		mac := hmac.New(sha1.New, decoded)
		_, _ = mac.Write(message[:])
		sum := mac.Sum(nil)
		index := sum[len(sum)-1] & 0x0f
		value := (int(sum[index])&0x7f)<<24 | int(sum[index+1])<<16 | int(sum[index+2])<<8 | int(sum[index+3])
		candidate := value % 1_000_000
		var a, b [4]byte
		binary.BigEndian.PutUint32(a[:], uint32(candidate))
		binary.BigEndian.PutUint32(b[:], uint32(provided))
		if subtle.ConstantTimeCompare(a[:], b[:]) == 1 {
			return counter, true
		}
	}
	return 0, false
}

func generateRecoveryCodes(count int) ([]string, []string, error) {
	codes, hashes := make([]string, 0, count), make([]string, 0, count)
	for index := 0; index < count; index++ {
		value := make([]byte, 10)
		if _, err := io.ReadFull(rand.Reader, value); err != nil {
			return nil, nil, err
		}
		raw := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(value))
		code := raw[:4] + "-" + raw[4:8] + "-" + raw[8:12] + "-" + raw[12:]
		digest := sha256.Sum256([]byte(normalizeRecoveryCode(code)))
		codes, hashes = append(codes, code), append(hashes, hex.EncodeToString(digest[:]))
	}
	return codes, hashes, nil
}

func normalizeRecoveryCode(code string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))
}

func opaqueTokenHash(token string) (string, bool) {
	if token == "" || strings.TrimSpace(token) != token {
		return "", false
	}
	value, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(value) != 32 || base64.RawURLEncoding.EncodeToString(value) != token {
		return "", false
	}
	digest := sha256.Sum256(value)
	return fmt.Sprintf("%x", digest[:]), true
}
