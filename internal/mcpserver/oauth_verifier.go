package mcpserver

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/cuipengdba/agentsql/internal/bootstrap"
	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/rbac"
)

const (
	oauthMaxTokenBytes = 32 << 10
	oauthMaxJSONBytes  = 1 << 20
	oauthKeyCacheTTL   = 5 * time.Minute
	oauthMaxLifetime   = time.Hour
)

var (
	errOAuthInsufficientScope = errors.New("OAuth access token scope insufficient")
	errOAuthForbidden         = errors.New("OAuth subject lacks local query permission")
)

// oauthAccessVerifier accepts only RS256 JWT access tokens minted for this
// exact protected resource. The verified subject must resolve to a currently
// active, locally managed OIDC identity on every MCP request.
type oauthAccessVerifier struct {
	config  config.MCPOAuthConfig
	client  *http.Client
	keysURL string
	runtime *bootstrap.Runtime
	rbac    *rbac.Service
	mu      sync.Mutex
	keys    map[string]*rsa.PublicKey
	expires time.Time
}

func newOAuthAccessVerifier(ctx context.Context, cfg config.MCPOAuthConfig, runtime *bootstrap.Runtime) (*oauthAccessVerifier, error) {
	return newOAuthAccessVerifierWithClient(ctx, cfg, runtime, &http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("OAuth discovery redirect forbidden") },
	})
}

func newOAuthAccessVerifierWithClient(ctx context.Context, cfg config.MCPOAuthConfig, runtime *bootstrap.Runtime, client *http.Client) (*oauthAccessVerifier, error) {
	if !cfg.Enabled || runtime == nil || runtime.Store == nil || client == nil || ctx == nil {
		return nil, errors.New("OAuth verifier requires enabled config, store and HTTP client")
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("OAuth config: %w", err)
	}
	var metadata struct {
		Issuer  string `json:"issuer"`
		JWKSURI string `json:"jwks_uri"`
	}
	verifier := &oauthAccessVerifier{config: cfg, client: client, runtime: runtime, rbac: rbac.NewService(runtime.Store.RBAC())}
	if err := verifier.getJSON(ctx, strings.TrimSuffix(cfg.IssuerURL, "/")+"/.well-known/openid-configuration", &metadata); err != nil {
		return nil, fmt.Errorf("OAuth issuer discovery: %w", err)
	}
	if metadata.Issuer != cfg.IssuerURL || metadata.JWKSURI == "" {
		return nil, errors.New("OAuth issuer metadata mismatch or JWKS URI missing")
	}
	if err := validOAuthFetchURL(metadata.JWKSURI); err != nil {
		return nil, fmt.Errorf("OAuth JWKS URI: %w", err)
	}
	issuerURL, _ := url.Parse(cfg.IssuerURL)
	keysURL, _ := url.Parse(metadata.JWKSURI)
	if issuerURL.Scheme != keysURL.Scheme || !strings.EqualFold(issuerURL.Host, keysURL.Host) {
		return nil, errors.New("OAuth JWKS URI must share the configured issuer origin")
	}
	verifier.keysURL = metadata.JWKSURI
	return verifier, nil
}

func validOAuthFetchURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawFragment != "" || parsed.Opaque != "" {
		return errors.New("endpoint must be an absolute URL without credentials, query or fragment")
	}
	if parsed.Scheme == "https" {
		return nil
	}
	issuerHost := parsed.Hostname()
	if parsed.Scheme == "http" && (issuerHost == "localhost" || issuerHost == "127.0.0.1" || issuerHost == "::1") {
		return nil
	}
	return errors.New("endpoint must use HTTPS except for loopback development")
}

func (verifier *oauthAccessVerifier) getJSON(ctx context.Context, endpoint string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	response, err := verifier.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("OAuth discovery endpoint unavailable")
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, oauthMaxJSONBytes+1))
	if err != nil || len(content) > oauthMaxJSONBytes {
		return errors.New("OAuth discovery response is too large or unreadable")
	}
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("OAuth discovery response is too large or has trailing JSON")
	}
	return nil
}

func (verifier *oauthAccessVerifier) Verify(ctx context.Context, token, agentID string) (rbac.Principal, error) {
	if verifier == nil || ctx == nil || agentID == "" || len(token) == 0 || len(token) > oauthMaxTokenBytes {
		return rbac.Principal{}, errors.New("invalid OAuth access token")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return rbac.Principal{}, errors.New("invalid OAuth access token")
	}
	var header struct {
		Algorithm string   `json:"alg"`
		KeyID     string   `json:"kid"`
		Critical  []string `json:"crit"`
	}
	if err := decodeOAuthPart(parts[0], &header); err != nil || header.Algorithm != "RS256" || header.KeyID == "" || len(header.Critical) != 0 {
		return rbac.Principal{}, errors.New("unsupported OAuth access token header")
	}
	key, err := verifier.signingKey(ctx, header.KeyID)
	if err != nil {
		return rbac.Principal{}, err
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return rbac.Principal{}, errors.New("invalid OAuth access token signature")
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature) != nil {
		return rbac.Principal{}, errors.New("invalid OAuth access token signature")
	}
	var claims map[string]any
	if err := decodeOAuthPart(parts[1], &claims); err != nil {
		return rbac.Principal{}, errors.New("invalid OAuth access token claims")
	}
	issuer, _ := claims["iss"].(string)
	subject, _ := claims["sub"].(string)
	scope, _ := claims["scope"].(string)
	if issuer != verifier.config.IssuerURL || subject == "" || !oauthAudienceMatches(claims["aud"], verifier.config.ResourceURL) {
		return rbac.Principal{}, errors.New("OAuth access token issuer, audience or subject rejected")
	}
	if !oauthScopeContains(scope, verifier.config.RequiredScope) {
		return rbac.Principal{}, errOAuthInsufficientScope
	}
	now := time.Now().UTC()
	exp, expOK := oauthNumericClaim(claims["exp"])
	iat, iatOK := oauthNumericClaim(claims["iat"])
	if !expOK || !iatOK || exp <= now.Unix() || iat > now.Add(30*time.Second).Unix() || exp <= iat || exp-iat > int64(oauthMaxLifetime.Seconds()) {
		return rbac.Principal{}, errors.New("OAuth access token lifetime rejected")
	}
	if raw, present := claims["nbf"]; present {
		nbf, ok := oauthNumericClaim(raw)
		if !ok || nbf > now.Add(30*time.Second).Unix() {
			return rbac.Principal{}, errors.New("OAuth access token not-before rejected")
		}
	}
	user, err := verifier.runtime.Store.HumanAuth().UserByExternalSubject(ctx, verifier.config.TenantID, "oidc", subject)
	if err != nil || user.Status != "active" {
		return rbac.Principal{}, errors.New("OAuth subject is not an active local identity")
	}
	principal, err := verifier.rbac.Principal(ctx, verifier.config.TenantID, user.ID)
	if err != nil || !principal.Has(rbac.PermissionQueryExecute) {
		return rbac.Principal{}, errOAuthForbidden
	}
	return principal, nil
}

func decodeOAuthPart(encoded string, target any) error {
	if encoded == "" || len(encoded) > oauthMaxTokenBytes {
		return errors.New("JWT part exceeds limit")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return err
	}
	return json.Unmarshal(decoded, target)
}

func oauthNumericClaim(raw any) (int64, bool) {
	value, ok := raw.(float64)
	if !ok || math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 || value > math.MaxInt64 || math.Trunc(value) != value {
		return 0, false
	}
	return int64(value), true
}

func oauthAudienceMatches(raw any, wanted string) bool {
	if value, ok := raw.(string); ok {
		return value == wanted
	}
	values, ok := raw.([]any)
	if !ok || len(values) == 0 || len(values) > 16 {
		return false
	}
	for _, rawValue := range values {
		value, ok := rawValue.(string)
		if !ok {
			return false
		}
		if value == wanted {
			return true
		}
	}
	return false
}

func oauthScopeContains(raw, wanted string) bool {
	for _, scope := range strings.Fields(raw) {
		if scope == wanted {
			return true
		}
	}
	return false
}

func (verifier *oauthAccessVerifier) signingKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	verifier.mu.Lock()
	defer verifier.mu.Unlock()
	if time.Now().Before(verifier.expires) && verifier.keys[kid] != nil {
		return verifier.keys[kid], nil
	}
	var jwks struct {
		Keys []struct {
			KeyType   string `json:"kty"`
			KeyID     string `json:"kid"`
			Use       string `json:"use"`
			Algorithm string `json:"alg"`
			Modulus   string `json:"n"`
			Exponent  string `json:"e"`
		} `json:"keys"`
	}
	if err := verifier.getJSON(ctx, verifier.keysURL, &jwks); err != nil {
		return nil, fmt.Errorf("OAuth JWKS unavailable: %w", err)
	}
	keys := make(map[string]*rsa.PublicKey)
	for _, item := range jwks.Keys {
		if item.KeyType != "RSA" || item.KeyID == "" || item.Use != "" && item.Use != "sig" || item.Algorithm != "" && item.Algorithm != "RS256" {
			continue
		}
		n, nErr := base64.RawURLEncoding.DecodeString(item.Modulus)
		e, eErr := base64.RawURLEncoding.DecodeString(item.Exponent)
		if nErr != nil || eErr != nil || len(n) < 256 || len(n) > 1024 || len(e) == 0 || len(e) > 4 {
			continue
		}
		exponent := 0
		for _, value := range e {
			exponent = exponent<<8 | int(value)
		}
		if exponent < 3 || exponent&1 == 0 {
			continue
		}
		if _, exists := keys[item.KeyID]; exists {
			return nil, errors.New("OAuth JWKS has duplicate signing key identifiers")
		}
		keys[item.KeyID] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: exponent}
	}
	verifier.keys, verifier.expires = keys, time.Now().Add(oauthKeyCacheTTL)
	if keys[kid] == nil {
		return nil, errors.New("OAuth signing key not found")
	}
	return keys[kid], nil
}
