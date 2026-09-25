package b5session

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
)

const (
	dialApplicationPrefix = "as5_"
	dialPayloadBytes      = 1 + 16 + 8
	dialMACBytes          = 16
)

var ErrInvalidDialIdentity = errors.New("b5session: invalid dial permit identity")

type DialIdentity struct {
	PermitID        string
	ApplicationName string
	OwnerHash       [8]byte
}

// DialIdentityCodec produces a PostgreSQL application_name below the 63-byte
// NAMEDATALEN limit. The field carries a random durable permit id, a digest of
// the owner incarnation, and a truncated HMAC. Inventory accepts it only after
// verifying the HMAC and expected incarnation.
type DialIdentityCodec struct{ key [32]byte }

type DialPermitIssuer struct {
	Ledger LedgerStore
	Codec  *DialIdentityCodec
}

func (issuer DialPermitIssuer) Issue(ctx context.Context, claimID string, generation uint64) (Lease, DialIdentity, error) {
	if issuer.Ledger == nil || issuer.Codec == nil {
		return Lease{}, DialIdentity{}, ErrInvalidDialIdentity
	}
	claim, err := issuer.Ledger.Claim(ctx, claimID)
	if err != nil {
		return Lease{}, DialIdentity{}, err
	}
	if claim.State != ClaimActive || claim.Generation != generation {
		return Lease{}, DialIdentity{}, ErrCASConflict
	}
	identity, err := issuer.Codec.New(claim.OwnerIncarnation)
	if err != nil {
		return Lease{}, DialIdentity{}, err
	}
	lease, err := issuer.Ledger.IssueDialPermit(ctx, claim.ID, claim.Generation, identity.PermitID, identity.ApplicationName)
	return lease, identity, err
}

func NewDialIdentityCodec(key []byte) (*DialIdentityCodec, error) {
	if len(key) != 32 {
		return nil, ErrInvalidDialIdentity
	}
	codec := &DialIdentityCodec{}
	copy(codec.key[:], key)
	return codec, nil
}

func (c *DialIdentityCodec) New(ownerIncarnation string) (DialIdentity, error) {
	if ownerIncarnation == "" {
		return DialIdentity{}, ErrInvalidDialIdentity
	}
	payload := make([]byte, dialPayloadBytes)
	payload[0] = 1
	if _, err := io.ReadFull(rand.Reader, payload[1:17]); err != nil {
		return DialIdentity{}, err
	}
	owner := sha256.Sum256([]byte(ownerIncarnation))
	copy(payload[17:], owner[:8])
	mac := hmac.New(sha256.New, c.key[:])
	_, _ = mac.Write([]byte("agentsql.b5.dial-permit.v1"))
	_, _ = mac.Write(payload)
	wire := append(payload, mac.Sum(nil)[:dialMACBytes]...)
	applicationName := dialApplicationPrefix + base64.RawURLEncoding.EncodeToString(wire)
	if len(applicationName) > 63 {
		return DialIdentity{}, ErrInvalidDialIdentity
	}
	var ownerHash [8]byte
	copy(ownerHash[:], payload[17:])
	return DialIdentity{PermitID: base64.RawURLEncoding.EncodeToString(payload[1:17]), ApplicationName: applicationName, OwnerHash: ownerHash}, nil
}

func (c *DialIdentityCodec) Verify(applicationName, ownerIncarnation string) (DialIdentity, error) {
	if len(applicationName) <= len(dialApplicationPrefix) || applicationName[:len(dialApplicationPrefix)] != dialApplicationPrefix || ownerIncarnation == "" {
		return DialIdentity{}, ErrInvalidDialIdentity
	}
	wire, err := base64.RawURLEncoding.DecodeString(applicationName[len(dialApplicationPrefix):])
	if err != nil || len(wire) != dialPayloadBytes+dialMACBytes || wire[0] != 1 {
		return DialIdentity{}, ErrInvalidDialIdentity
	}
	mac := hmac.New(sha256.New, c.key[:])
	_, _ = mac.Write([]byte("agentsql.b5.dial-permit.v1"))
	_, _ = mac.Write(wire[:dialPayloadBytes])
	if subtle.ConstantTimeCompare(wire[dialPayloadBytes:], mac.Sum(nil)[:dialMACBytes]) != 1 {
		return DialIdentity{}, ErrInvalidDialIdentity
	}
	owner := sha256.Sum256([]byte(ownerIncarnation))
	if subtle.ConstantTimeCompare(wire[17:25], owner[:8]) != 1 {
		return DialIdentity{}, ErrInvalidDialIdentity
	}
	var ownerHash [8]byte
	copy(ownerHash[:], wire[17:25])
	return DialIdentity{PermitID: base64.RawURLEncoding.EncodeToString(wire[1:17]), ApplicationName: applicationName, OwnerHash: ownerHash}, nil
}
