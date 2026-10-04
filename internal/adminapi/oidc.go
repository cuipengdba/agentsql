package adminapi

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
	"math/big"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/rbac"
	"github.com/cuipengdba/agentsql/internal/store"
)

type oidcDiscovery struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
	EndSessionEndpoint    string `json:"end_session_endpoint"`
}

type oidcProvider struct {
	config      config.OIDCConfig
	client      *http.Client
	secret      string
	metadata    oidcDiscovery
	mu          sync.Mutex
	keys        map[string]*rsa.PublicKey
	keysExpires time.Time
}

func newOIDCProvider(ctx context.Context, value config.OIDCConfig) (*oidcProvider, error) {
	return newOIDCProviderWithClient(ctx, value, &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("OIDC endpoint redirects are forbidden") },
	})
}

func newOIDCProviderWithClient(ctx context.Context, value config.OIDCConfig, client *http.Client) (*oidcProvider, error) {
	if client == nil {
		return nil, errors.New("OIDC HTTP client is required")
	}
	provider := &oidcProvider{config: value, client: client}
	if value.ClientSecretEnv != "" {
		provider.secret = os.Getenv(value.ClientSecretEnv)
		if provider.secret == "" {
			return nil, fmt.Errorf("environment variable %s is required", value.ClientSecretEnv)
		}
	}
	discoveryURL := strings.TrimSuffix(value.IssuerURL, "/") + "/.well-known/openid-configuration"
	if err := provider.getJSON(ctx, discoveryURL, &provider.metadata); err != nil {
		return nil, fmt.Errorf("discovery: %w", err)
	}
	if provider.metadata.Issuer != value.IssuerURL {
		return nil, errors.New("discovery issuer does not exactly match configured issuer")
	}
	for name, raw := range map[string]string{"authorization_endpoint": provider.metadata.AuthorizationEndpoint, "token_endpoint": provider.metadata.TokenEndpoint, "jwks_uri": provider.metadata.JWKSURI} {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
			return nil, fmt.Errorf("discovery %s is not an absolute HTTPS URL", name)
		}
	}
	if endpoint := provider.metadata.EndSessionEndpoint; endpoint != "" {
		parsed, parseErr := url.Parse(endpoint)
		if parseErr != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
			return nil, errors.New("discovery end_session_endpoint is not an absolute HTTPS URL")
		}
	}
	return provider, nil
}

func (handler *Handler) oidcStart(writer http.ResponseWriter, request *http.Request) {
	if handler.oidc == nil {
		handler.fail(writer, http.StatusNotFound, "not found")
		return
	}
	state, stateHash, err := newOpaqueToken()
	if err != nil {
		handler.internal(writer, err)
		return
	}
	nonce, _, err := newOpaqueToken()
	if err != nil {
		handler.internal(writer, err)
		return
	}
	verifier, _, err := newOpaqueToken()
	if err != nil {
		handler.internal(writer, err)
		return
	}
	encryptedVerifier, err := encryptMFASecret(handler.tokenKey, verifier)
	if err != nil {
		handler.internal(writer, err)
		return
	}
	returnTo := request.URL.Query().Get("return_to")
	if returnTo == "" {
		returnTo = "/"
	}
	if !strings.HasPrefix(returnTo, "/") || strings.HasPrefix(returnTo, "//") || strings.ContainsAny(returnTo, "\r\n") {
		handler.fail(writer, http.StatusBadRequest, "invalid return_to")
		return
	}
	expires := time.Now().UTC().Add(10 * time.Minute)
	if err = handler.deps.Runtime.Store.HumanAuth().CreateOIDCRequest(request.Context(), store.OIDCAuthRequest{StateHash: stateHash, Nonce: nonce, PKCEVerifier: encryptedVerifier, ReturnTo: returnTo, ExpiresAt: expires}); err != nil {
		handler.internal(writer, err)
		return
	}
	challenge := sha256.Sum256([]byte(verifier))
	scopes := append([]string(nil), handler.oidc.config.Scopes...)
	if len(scopes) == 0 {
		scopes = []string{"openid", "profile", "email", "groups"}
	}
	if !containsString(scopes, "openid") {
		scopes = append(scopes, "openid")
	}
	query := url.Values{
		"response_type": {"code"}, "client_id": {handler.oidc.config.ClientID}, "redirect_uri": {handler.oidc.config.RedirectURL},
		"scope": {strings.Join(scopes, " ")}, "state": {state}, "nonce": {nonce},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"},
	}
	http.Redirect(writer, request, appendURLQuery(handler.oidc.metadata.AuthorizationEndpoint, query), http.StatusFound)
}

func (handler *Handler) oidcCallback(writer http.ResponseWriter, request *http.Request) {
	if handler.oidc == nil {
		handler.fail(writer, http.StatusNotFound, "not found")
		return
	}
	if request.URL.Query().Get("error") != "" {
		handler.fail(writer, http.StatusUnauthorized, "OIDC authorization failed")
		return
	}
	code, state := request.URL.Query().Get("code"), request.URL.Query().Get("state")
	stateHash, ok := opaqueTokenHash(state)
	if code == "" || !ok {
		handler.fail(writer, http.StatusUnauthorized, "OIDC authorization failed")
		return
	}
	transaction, err := handler.deps.Runtime.Store.HumanAuth().ConsumeOIDCRequest(request.Context(), stateHash, time.Now().UTC())
	if err != nil {
		handler.fail(writer, http.StatusUnauthorized, "OIDC authorization failed")
		return
	}
	verifier, err := decryptMFASecret(handler.tokenKey, transaction.PKCEVerifier)
	if err != nil {
		handler.fail(writer, http.StatusUnauthorized, "OIDC authorization failed")
		return
	}
	idToken, err := handler.oidc.exchange(request.Context(), code, verifier)
	if err != nil {
		handler.logger.Warn().Str("error_type", fmt.Sprintf("%T", err)).Msg("OIDC token exchange rejected")
		handler.fail(writer, http.StatusUnauthorized, "OIDC authorization failed")
		return
	}
	claims, err := handler.oidc.verifyIDToken(request.Context(), idToken, transaction.Nonce, time.Now().UTC())
	if err != nil {
		handler.logger.Warn().Str("error_type", fmt.Sprintf("%T", err)).Msg("OIDC ID token rejected")
		handler.fail(writer, http.StatusUnauthorized, "OIDC authorization failed")
		return
	}
	principal, err := handler.externalPrincipal(request.Context(), "oidc", claims.Subject, claims.Username, claims.Groups,
		handler.oidc.config.TenantID, handler.oidc.config.AutoProvision, handler.oidc.config.GroupRoleMap)
	if err != nil {
		handler.fail(writer, http.StatusUnauthorized, "OIDC identity is not authorized")
		return
	}
	session, err := handler.newSession(request.Context(), principal)
	if err != nil {
		handler.internal(writer, err)
		return
	}
	if strings.Contains(request.Header.Get("Accept"), "text/html") {
		handler.writeOIDCBrowserSession(writer, session, principal.Username, transaction.ReturnTo)
		return
	}
	handler.ok(writer, session)
}

func (handler *Handler) oidcLogout(writer http.ResponseWriter, request *http.Request) {
	if handler.oidc == nil {
		handler.fail(writer, http.StatusNotFound, "not found")
		return
	}
	if err := handler.revokeRequestSession(request); err != nil {
		handler.internal(writer, err)
		return
	}
	logoutURL := handler.oidc.metadata.EndSessionEndpoint
	if logoutURL != "" {
		query := url.Values{"client_id": {handler.oidc.config.ClientID}}
		if handler.oidc.config.PostLogoutURL != "" {
			query.Set("post_logout_redirect_uri", handler.oidc.config.PostLogoutURL)
		}
		logoutURL = appendURLQuery(logoutURL, query)
	}
	if logoutURL != "" && strings.Contains(request.Header.Get("Accept"), "text/html") {
		http.Redirect(writer, request, logoutURL, http.StatusFound)
		return
	}
	handler.ok(writer, map[string]any{"ok": true, "logout_url": logoutURL})
}

type verifiedOIDCClaims struct {
	Subject, Username string
	Groups            []string
}

func (provider *oidcProvider) exchange(ctx context.Context, code, verifier string) (string, error) {
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {provider.config.RedirectURL}, "client_id": {provider.config.ClientID}, "code_verifier": {verifier}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.metadata.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if provider.secret != "" {
		request.SetBasicAuth(provider.config.ClientID, provider.secret)
	}
	response, err := provider.client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return "", fmt.Errorf("token endpoint returned %d", response.StatusCode)
	}
	var payload struct {
		IDToken string `json:"id_token"`
	}
	if err = decodeLimitedJSON(response.Body, &payload); err != nil || payload.IDToken == "" {
		return "", errors.New("token response omitted id_token")
	}
	return payload.IDToken, nil
}

func (provider *oidcProvider) verifyIDToken(ctx context.Context, token, expectedNonce string, now time.Time) (verifiedOIDCClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return verifiedOIDCClaims{}, errors.New("malformed ID token")
	}
	var header struct {
		Algorithm string `json:"alg"`
		KeyID     string `json:"kid"`
	}
	if err := decodeJWTPart(parts[0], &header); err != nil || header.Algorithm != "RS256" || header.KeyID == "" {
		return verifiedOIDCClaims{}, errors.New("unsupported ID token header")
	}
	key, err := provider.signingKey(ctx, header.KeyID)
	if err != nil {
		return verifiedOIDCClaims{}, err
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return verifiedOIDCClaims{}, errors.New("invalid ID token signature")
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err = rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature); err != nil {
		return verifiedOIDCClaims{}, errors.New("invalid ID token signature")
	}
	var raw map[string]any
	if err = decodeJWTPart(parts[1], &raw); err != nil {
		return verifiedOIDCClaims{}, err
	}
	issuer, _ := raw["iss"].(string)
	subject, _ := raw["sub"].(string)
	nonce, _ := raw["nonce"].(string)
	if issuer != provider.config.IssuerURL || subject == "" || nonce != expectedNonce {
		return verifiedOIDCClaims{}, errors.New("invalid ID token identity claims")
	}
	if !validAudience(raw["aud"], raw["azp"], provider.config.ClientID) {
		return verifiedOIDCClaims{}, errors.New("invalid ID token audience")
	}
	expiry, expOK := numericClaim(raw["exp"])
	issued, iatOK := numericClaim(raw["iat"])
	if !expOK || !iatOK || now.Unix() >= expiry || issued > now.Add(time.Minute).Unix() {
		return verifiedOIDCClaims{}, errors.New("invalid ID token time claims")
	}
	if rawNotBefore, present := raw["nbf"]; present {
		notBefore, valid := numericClaim(rawNotBefore)
		if !valid || now.Add(time.Minute).Unix() < notBefore {
			return verifiedOIDCClaims{}, errors.New("invalid ID token not-before claim")
		}
	}
	username, _ := raw[provider.config.UsernameClaim].(string)
	username = strings.TrimSpace(username)
	groups := stringSliceClaim(raw[provider.config.GroupsClaim])
	if username == "" || len(groups) == 0 {
		return verifiedOIDCClaims{}, errors.New("required OIDC claims are missing")
	}
	return verifiedOIDCClaims{Subject: subject, Username: username, Groups: groups}, nil
}

func (provider *oidcProvider) signingKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if time.Now().Before(provider.keysExpires) && provider.keys[kid] != nil {
		return provider.keys[kid], nil
	}
	// A string map keeps the compact JWK names explicit.
	var raw struct {
		Keys []map[string]string `json:"keys"`
	}
	if err := provider.getJSON(ctx, provider.metadata.JWKSURI, &raw); err != nil {
		return nil, err
	}
	keys := make(map[string]*rsa.PublicKey)
	for _, item := range raw.Keys {
		if item["kty"] != "RSA" || item["kid"] == "" || (item["use"] != "" && item["use"] != "sig") || (item["alg"] != "" && item["alg"] != "RS256") {
			continue
		}
		nBytes, nErr := base64.RawURLEncoding.DecodeString(item["n"])
		eBytes, eErr := base64.RawURLEncoding.DecodeString(item["e"])
		if nErr != nil || eErr != nil || len(nBytes) < 256 || len(eBytes) == 0 || len(eBytes) > 4 {
			continue
		}
		exponent := 0
		for _, value := range eBytes {
			exponent = exponent<<8 | int(value)
		}
		if exponent < 3 {
			continue
		}
		keys[item["kid"]] = &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: exponent}
	}
	provider.keys, provider.keysExpires = keys, time.Now().Add(5*time.Minute)
	if keys[kid] == nil {
		return nil, errors.New("OIDC signing key not found")
	}
	return keys[kid], nil
}

func (handler *Handler) externalPrincipal(ctx context.Context, provider, subject, username string, groups []string, tenantID string, autoProvision bool, mapping map[string][]string) (rbac.Principal, error) {
	roleSet := make(map[string]struct{})
	for _, group := range groups {
		for _, role := range mapping[group] {
			roleSet[role] = struct{}{}
		}
	}
	roleIDs := make([]string, 0, len(roleSet))
	for role := range roleSet {
		roleIDs = append(roleIDs, role)
	}
	sort.Strings(roleIDs)
	if len(roleIDs) == 0 {
		return rbac.Principal{}, errors.New("external identity has no mapped roles")
	}
	repository := handler.deps.Runtime.Store.HumanAuth()
	user, err := repository.UserByExternalSubject(ctx, tenantID, provider, subject)
	if errors.Is(err, store.ErrNotFound) && autoProvision {
		user, err = repository.CreateExternalIdentity(ctx, model.User{ID: rbac.NewID("user"), TenantID: tenantID, Username: username, DisplayName: username}, provider, subject)
	}
	if err != nil || user.Status != "active" || user.Username != username {
		return rbac.Principal{}, errors.New("external identity is not provisioned")
	}
	if err = handler.deps.Runtime.Store.RBAC().SetUserRoles(ctx, tenantID, user.ID, roleIDs); err != nil {
		return rbac.Principal{}, errors.New("external role mapping is invalid")
	}
	return handler.rbac.Principal(ctx, tenantID, user.ID)
}

func (provider *oidcProvider) getJSON(ctx context.Context, endpoint string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	response, err := provider.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("endpoint returned %d", response.StatusCode)
	}
	return decodeLimitedJSON(response.Body, target)
}

func decodeLimitedJSON(reader io.Reader, target any) error {
	return json.NewDecoder(io.LimitReader(reader, 1<<20)).Decode(target)
}
func decodeJWTPart(part string, target any) error {
	value, err := base64.RawURLEncoding.DecodeString(part)
	if err != nil || len(value) > 1<<20 {
		return errors.New("invalid JWT part")
	}
	return json.Unmarshal(value, target)
}
func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
func stringSliceClaim(value any) []string {
	raw, ok := value.([]any)
	if !ok {
		if one, ok := value.(string); ok && one != "" {
			return []string{one}
		}
		return nil
	}
	result := make([]string, 0, len(raw))
	for _, item := range raw {
		if text, ok := item.(string); ok && text != "" {
			result = append(result, text)
		}
	}
	return result
}
func numericClaim(value any) (int64, bool) {
	switch item := value.(type) {
	case float64:
		return int64(item), item == float64(int64(item))
	case json.Number:
		value, err := item.Int64()
		return value, err == nil
	default:
		return 0, false
	}
}
func validAudience(value, azp any, clientID string) bool {
	audiences := stringSliceClaim(value)
	if len(audiences) == 0 {
		return false
	}
	found := false
	for _, audience := range audiences {
		if audience == clientID {
			found = true
		}
	}
	if !found {
		return false
	}
	authorized, _ := azp.(string)
	if len(audiences) > 1 {
		return authorized == clientID
	}
	return authorized == "" || authorized == clientID
}

func appendURLQuery(endpoint string, values url.Values) string {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return endpoint
	}
	query := parsed.Query()
	for name, items := range values {
		for _, item := range items {
			query.Add(name, item)
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func (handler *Handler) writeOIDCBrowserSession(writer http.ResponseWriter, session map[string]any, username, returnTo string) {
	persisted := map[string]any{"state": map[string]any{
		"token": session["token"], "expiresAt": session["expires_at"], "username": username,
	}, "version": 0}
	payload, _ := json.Marshal(persisted)
	destination, _ := json.Marshal(returnTo)
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'")
	_, _ = fmt.Fprintf(writer, "<!doctype html><meta charset=utf-8><script>localStorage.setItem('agentsql.auth',%s);location.replace(%s)</script>", strconv.Quote(string(payload)), destination)
}
