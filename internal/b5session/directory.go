package b5session

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"io"
	"time"

	"github.com/cuipengdba/agentsql/internal/b5"
	"github.com/cuipengdba/agentsql/internal/store"
)

const (
	continuationSecretBytes = 32
	sessionIDBytes          = 32
	maxIdentityBytes        = 128
	maxMethodBytes          = 64
	maxRouteBytes           = 256
)

var (
	ErrInvalidDirectoryConfig = errors.New("b5session: invalid directory configuration")
	ErrInvalidContinuation    = errors.New("b5session: invalid continuation request")
)

type SessionStore interface {
	Create(context.Context, store.B5Session) (store.B5Session, error)
	Get(context.Context, string) (store.B5Session, error)
	CASStatus(context.Context, string, int64, b5.SessionStatus, b5.SessionStatus, time.Time) (store.B5Session, error)
	CASOwner(context.Context, string, int64, string, uint64, string, string, string, []byte) (store.B5Session, error)
	ListExpired(context.Context, time.Time, int) ([]store.B5Session, error)
}

// KeySealer keeps the continuation key encrypted in the directory. AAD is
// canonical and includes the owner epoch, so an owner transfer must re-seal in
// the same CAS that increments the epoch.
type KeySealer interface {
	Seal(plaintext, aad []byte) (string, error)
	Open(ciphertext string, aad []byte) ([]byte, error)
	KeyID() string
}

type AESGCMSealer struct {
	aead  cipher.AEAD
	keyID string
}

func NewAESGCMSealer(keyID string, key []byte) (*AESGCMSealer, error) {
	if keyID == "" || len(key) != 32 {
		return nil, ErrInvalidDirectoryConfig
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &AESGCMSealer{aead: aead, keyID: keyID}, nil
}

func (s *AESGCMSealer) KeyID() string { return s.keyID }

func (s *AESGCMSealer) Seal(plaintext, aad []byte) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := s.aead.Seal(append([]byte(nil), nonce...), nonce, plaintext, aad)
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (s *AESGCMSealer) Open(encoded string, aad []byte) ([]byte, error) {
	sealed, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(sealed) < s.aead.NonceSize() {
		return nil, ErrInvalidContinuation
	}
	nonce := sealed[:s.aead.NonceSize()]
	plaintext, err := s.aead.Open(nil, nonce, sealed[s.aead.NonceSize():], aad)
	if err != nil {
		return nil, ErrInvalidContinuation
	}
	return plaintext, nil
}

type DirectoryConfig struct {
	Store       SessionStore
	Sealer      KeySealer
	IdleTTL     time.Duration
	AbsoluteTTL time.Duration
	Now         func() time.Time
}

type Directory struct {
	store       SessionStore
	sealer      KeySealer
	idleTTL     time.Duration
	absoluteTTL time.Duration
	now         func() time.Time
}

func NewDirectory(config DirectoryConfig) (*Directory, error) {
	if config.Store == nil || config.Sealer == nil || config.IdleTTL <= 0 || config.AbsoluteTTL <= 0 || config.IdleTTL > config.AbsoluteTTL {
		return nil, ErrInvalidDirectoryConfig
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &Directory{store: config.Store, sealer: config.Sealer, idleTTL: config.IdleTTL, absoluteTTL: config.AbsoluteTTL, now: config.Now}, nil
}

type CreateSession struct {
	AgentID, TenantID, PrincipalID string
	OwnerInstanceID, StickyRoute   string
}

type CreatedSession struct {
	Session store.B5Session
	// ContinuationSecret is returned exactly once and is never persisted.
	ContinuationSecret string
}

func (d *Directory) Create(ctx context.Context, input CreateSession) (CreatedSession, error) {
	if ctx == nil || !bounded(input.AgentID, maxIdentityBytes) || !bounded(input.TenantID, maxIdentityBytes) || !bounded(input.PrincipalID, maxIdentityBytes) || !bounded(input.OwnerInstanceID, maxIdentityBytes) || !bounded(input.StickyRoute, maxRouteBytes) {
		return CreatedSession{}, ErrInvalidContinuation
	}
	sessionID, err := randomToken(sessionIDBytes)
	if err != nil {
		return CreatedSession{}, err
	}
	secretBytes := make([]byte, continuationSecretBytes)
	if _, err := io.ReadFull(rand.Reader, secretBytes); err != nil {
		return CreatedSession{}, err
	}
	key := deriveSessionKey(secretBytes, sessionID)
	now := d.now().UTC()
	value := store.B5Session{
		SessionID: sessionID, AgentID: input.AgentID, TenantID: input.TenantID,
		PrincipalID: input.PrincipalID, OwnerInstanceID: input.OwnerInstanceID,
		OwnerEpoch: 1, ContinuationSchemaID: b5.ContinuationProofSchemaID,
		ContinuationSchemaVersion: b5.ContinuationProofVersion,
		StickyRoute:               input.StickyRoute, Status: b5.SessionReady,
		IdleExpiresAt: now.Add(d.idleTTL), AbsoluteExpiresAt: now.Add(d.absoluteTTL),
	}
	value.ContinuationKeyCiphertext, err = d.sealer.Seal(key, d.aad(value))
	if err != nil {
		return CreatedSession{}, err
	}
	digest := sha256.Sum256(key)
	value.ContinuationHMACDigest = digest[:]
	value, err = d.store.Create(ctx, value)
	if err != nil {
		return CreatedSession{}, err
	}
	return CreatedSession{Session: value, ContinuationSecret: base64.RawURLEncoding.EncodeToString(secretBytes)}, nil
}

// ContinuationInput intentionally has separate MCP and AgentSQL identifiers.
// McpSessionID is transport metadata only and is never a fallback lookup key.
type ContinuationInput struct {
	AgentSQLSessionID string
	McpSessionID      string
	PrincipalID       string
	InstanceID        string
	Method            string
	RequestID         string
	OwnerEpoch        uint64
	ExpectedSeq       *uint64
	BodyDigest        [32]byte
	Proof             string
}

type RouteDecision struct {
	Session          store.B5Session
	Code             b5.ErrorCode
	RetrySameRequest bool
	StickyRoute      string
	Authorized       bool
}

func (d *Directory) Lookup(ctx context.Context, input ContinuationInput) (RouteDecision, error) {
	if err := validateContinuationInput(input); err != nil {
		return denied(b5.ErrorSessionProofRequired), nil
	}
	value, err := d.store.Get(ctx, input.AgentSQLSessionID)
	if err != nil {
		return denied(b5.ErrorSessionNotFoundOrDenied), nil
	}
	key, err := d.openKey(value)
	if err != nil || value.PrincipalID != input.PrincipalID || !verifyProof(key, input) {
		return denied(b5.ErrorSessionNotFoundOrDenied), nil
	}
	if input.OwnerEpoch != value.OwnerEpoch {
		return denied(b5.ErrorSessionOwnerEpochStale), nil
	}
	now := d.now().UTC()
	if !now.Before(value.IdleExpiresAt) || !now.Before(value.AbsoluteExpiresAt) || value.Status == b5.SessionExpired {
		return RouteDecision{Session: value, Code: b5.ErrorSessionExpired}, nil
	}
	if value.Status == b5.SessionTerminal {
		return RouteDecision{Session: value, Code: b5.ErrorSessionTerminalRecordExpired}, nil
	}
	if input.InstanceID != value.OwnerInstanceID {
		// This branch deliberately performs no mutation and returns no cleanup
		// capability. The authenticated route hint is safe to expose.
		return RouteDecision{Session: value, Code: b5.ErrorSessionWrongInstance, RetrySameRequest: true, StickyRoute: value.StickyRoute}, nil
	}
	return RouteDecision{Session: value, Authorized: true, StickyRoute: value.StickyRoute}, nil
}

func (d *Directory) Touch(ctx context.Context, input ContinuationInput) (RouteDecision, error) {
	decision, err := d.Lookup(ctx, input)
	if err != nil || !decision.Authorized {
		return decision, err
	}
	next := d.now().UTC().Add(d.idleTTL)
	if next.After(decision.Session.AbsoluteExpiresAt) {
		next = decision.Session.AbsoluteExpiresAt
	}
	updated, err := d.store.CASStatus(ctx, decision.Session.SessionID, decision.Session.Revision, decision.Session.Status, decision.Session.Status, next)
	if err != nil {
		return RouteDecision{}, err
	}
	decision.Session = updated
	return decision, nil
}

func (d *Directory) Close(ctx context.Context, input ContinuationInput) (RouteDecision, error) {
	decision, err := d.Lookup(ctx, input)
	if err != nil || !decision.Authorized {
		return decision, err
	}
	updated, err := d.store.CASStatus(ctx, decision.Session.SessionID, decision.Session.Revision, decision.Session.Status, b5.SessionTerminal, decision.Session.IdleExpiresAt)
	if err != nil {
		return RouteDecision{}, err
	}
	decision.Session = updated
	return decision, nil
}

// TransferOwner increments the owner epoch exactly once. The request is
// authenticated against the old owner/epoch; the same key is re-sealed with
// new-epoch AAD before the atomic directory CAS.
func (d *Directory) TransferOwner(ctx context.Context, input ContinuationInput, newOwner, newRoute string) (store.B5Session, error) {
	decision, err := d.Lookup(ctx, input)
	if err != nil {
		return store.B5Session{}, err
	}
	if !decision.Authorized || !bounded(newOwner, maxIdentityBytes) || !bounded(newRoute, maxRouteBytes) {
		return store.B5Session{}, ErrInvalidContinuation
	}
	key, err := d.openKey(decision.Session)
	if err != nil {
		return store.B5Session{}, err
	}
	next := decision.Session
	next.OwnerEpoch++
	next.OwnerInstanceID = newOwner
	next.StickyRoute = newRoute
	ciphertext, err := d.sealer.Seal(key, d.aad(next))
	if err != nil {
		return store.B5Session{}, err
	}
	digest := sha256.Sum256(key)
	return d.store.CASOwner(ctx, next.SessionID, decision.Session.Revision, decision.Session.OwnerInstanceID, decision.Session.OwnerEpoch, newOwner, newRoute, ciphertext, digest[:])
}

func (d *Directory) Expire(ctx context.Context, limit int) (int, error) {
	now := d.now().UTC()
	values, err := d.store.ListExpired(ctx, now, limit)
	if err != nil {
		return 0, err
	}
	expired := 0
	for _, value := range values {
		if _, casErr := d.store.CASStatus(ctx, value.SessionID, value.Revision, value.Status, b5.SessionExpired, value.IdleExpiresAt); casErr == nil {
			expired++
		} else if !errors.Is(casErr, store.ErrB5CASConflict) {
			return expired, casErr
		}
	}
	return expired, nil
}

func SignContinuation(secret string, sessionID, method, requestID string, ownerEpoch uint64, expectedSeq *uint64, bodyDigest [32]byte) (string, error) {
	secretBytes, err := base64.RawURLEncoding.DecodeString(secret)
	if err != nil || len(secretBytes) != continuationSecretBytes || !bounded(sessionID, maxIdentityBytes) || !bounded(method, maxMethodBytes) || !bounded(requestID, maxIdentityBytes) || ownerEpoch == 0 {
		return "", ErrInvalidContinuation
	}
	key := deriveSessionKey(secretBytes, sessionID)
	input := ContinuationInput{AgentSQLSessionID: sessionID, Method: method, RequestID: requestID, OwnerEpoch: ownerEpoch, ExpectedSeq: expectedSeq, BodyDigest: bodyDigest}
	mac := hmac.New(sha256.New, key)
	writeContinuation(mac, input)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func verifyProof(key []byte, input ContinuationInput) bool {
	provided, err := base64.RawURLEncoding.DecodeString(input.Proof)
	if err != nil || len(provided) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, key)
	writeContinuation(mac, input)
	return subtle.ConstantTimeCompare(provided, mac.Sum(nil)) == 1
}

func writeContinuation(h hash.Hash, input ContinuationInput) {
	writeField(h, b5.ContinuationProofSchemaID)
	writeField(h, input.Method)
	writeField(h, input.AgentSQLSessionID)
	writeUint64(h, input.OwnerEpoch)
	writeField(h, input.RequestID)
	if input.ExpectedSeq == nil {
		writeField(h, "absent")
	} else {
		writeField(h, "present")
		writeUint64(h, *input.ExpectedSeq)
	}
	writeBytes(h, input.BodyDigest[:])
}

func validateContinuationInput(input ContinuationInput) error {
	if !bounded(input.AgentSQLSessionID, maxIdentityBytes) || !bounded(input.PrincipalID, maxIdentityBytes) || !bounded(input.InstanceID, maxIdentityBytes) || !bounded(input.Method, maxMethodBytes) || !bounded(input.RequestID, maxIdentityBytes) || input.OwnerEpoch == 0 || len(input.Proof) != base64.RawURLEncoding.EncodedLen(sha256.Size) {
		return ErrInvalidContinuation
	}
	if len(input.McpSessionID) > maxIdentityBytes || input.McpSessionID != "" && input.McpSessionID == input.AgentSQLSessionID {
		return ErrInvalidContinuation
	}
	return nil
}

func (d *Directory) openKey(value store.B5Session) ([]byte, error) {
	if value.ContinuationSchemaID != b5.ContinuationProofSchemaID || value.ContinuationSchemaVersion != b5.ContinuationProofVersion {
		return nil, ErrInvalidContinuation
	}
	key, err := d.sealer.Open(value.ContinuationKeyCiphertext, d.aad(value))
	if err != nil || len(key) != sha256.Size {
		return nil, ErrInvalidContinuation
	}
	digest := sha256.Sum256(key)
	if subtle.ConstantTimeCompare(digest[:], value.ContinuationHMACDigest) != 1 {
		return nil, ErrInvalidContinuation
	}
	return key, nil
}

func (d *Directory) aad(value store.B5Session) []byte {
	h := sha256.New()
	writeField(h, "agentsql.b5.continuation-key-aad.v1")
	writeField(h, d.sealer.KeyID())
	writeField(h, value.SessionID)
	writeField(h, value.AgentID)
	writeField(h, value.TenantID)
	writeField(h, value.PrincipalID)
	writeUint64(h, value.OwnerEpoch)
	return h.Sum(nil)
}

func deriveSessionKey(secret []byte, sessionID string) []byte {
	// RFC 5869 HKDF-SHA-256, with the session ID as salt and a fixed versioned
	// info label. Keeping it local avoids making the raw open-session secret a
	// long-lived MAC key.
	salt := sha256.Sum256([]byte(sessionID))
	extract := hmac.New(sha256.New, salt[:])
	_, _ = extract.Write(secret)
	prk := extract.Sum(nil)
	expand := hmac.New(sha256.New, prk)
	_, _ = expand.Write([]byte("agentsql.b5.continuation-key.v2"))
	_, _ = expand.Write([]byte{1})
	return expand.Sum(nil)
}

func randomToken(size int) (string, error) {
	value := make([]byte, size)
	if _, err := io.ReadFull(rand.Reader, value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func denied(code b5.ErrorCode) RouteDecision { return RouteDecision{Code: code} }

func writeField(h hash.Hash, value string) {
	writeBytes(h, []byte(value))
}

func bounded(value string, maximum int) bool { return len(value) > 0 && len(value) <= maximum }

func writeBytes(h hash.Hash, value []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = h.Write(size[:])
	_, _ = h.Write(value)
}

func writeUint64(h hash.Hash, value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	writeBytes(h, encoded[:])
}

func (d *Directory) String() string {
	return fmt.Sprintf("b5session.Directory(idle=%s absolute=%s)", d.idleTTL, d.absoluteTTL)
}
