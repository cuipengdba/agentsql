package store

import (
	"context"
	"errors"
)

// ChainManifestProvider resolves trusted, database-external chain authority.
type ChainManifestProvider interface {
	ChainManifestForDomain(ctx context.Context, domain string) (ChainManifest, error)
}

type keylessChainManifestProvider struct{}

// NewKeylessChainManifestProvider returns the explicit version-zero manifest
// used by installations that have not configured an HMAC chain key.
func NewKeylessChainManifestProvider() ChainManifestProvider {
	return keylessChainManifestProvider{}
}

func (keylessChainManifestProvider) ChainManifestForDomain(_ context.Context, domain string) (ChainManifest, error) {
	switch domain {
	case "management", "traffic":
		return KeylessChainManifest{}, nil
	default:
		return nil, errors.New("unsupported audit chain domain")
	}
}

// KeylessChainManifest is the explicit version-zero, no-key chain authority.
type KeylessChainManifest struct{}

func (KeylessChainManifest) ExpectedMode(context.Context) (string, error)   { return "keyless", nil }
func (KeylessChainManifest) CurrentKeyVersion(context.Context) (int, error) { return 0, nil }
func (KeylessChainManifest) ChainKeyForVersion(context.Context, int) ([]byte, error) {
	return nil, errors.New("keyless audit chain has no HMAC key")
}
