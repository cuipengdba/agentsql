package adminapi

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOIDCIDTokenRS256ClaimsAndNonce(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	kid := "test-key"
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(writer).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": kid, "use": "sig", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(privateKey.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(privateKey.E)).Bytes()),
		}}})
	}))
	defer server.Close()
	now := time.Now().UTC()
	provider := &oidcProvider{
		config: config.OIDCConfig{IssuerURL: "https://issuer.example.test", ClientID: "agentsql", UsernameClaim: "preferred_username", GroupsClaim: "groups"},
		client: server.Client(), metadata: oidcDiscovery{JWKSURI: server.URL},
	}
	claims := map[string]any{"iss": provider.config.IssuerURL, "sub": "subject-1", "aud": provider.config.ClientID,
		"exp": now.Add(time.Minute).Unix(), "iat": now.Unix(), "nonce": "expected-nonce",
		"preferred_username": "alice", "groups": []string{"agentsql-admins"}}
	token := signTestIDToken(t, privateKey, kid, claims)
	verified, err := provider.verifyIDToken(context.Background(), token, "expected-nonce", now)
	require.NoError(t, err)
	require.Equal(t, "subject-1", verified.Subject)
	require.Equal(t, "alice", verified.Username)
	require.Equal(t, []string{"agentsql-admins"}, verified.Groups)
	_, err = provider.verifyIDToken(context.Background(), token, "different-nonce", now)
	require.Error(t, err)

	claims["azp"] = "different-client"
	_, err = provider.verifyIDToken(context.Background(), signTestIDToken(t, privateKey, kid, claims), "expected-nonce", now)
	require.Error(t, err)
	delete(claims, "azp")
	claims["nbf"] = now.Add(2 * time.Minute).Unix()
	_, err = provider.verifyIDToken(context.Background(), signTestIDToken(t, privateKey, kid, claims), "expected-nonce", now)
	require.Error(t, err)
}

func TestOIDCMockProviderDiscoveryExchangeAndVerification(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	kid, nonce := "mock-provider-key", "mock-nonce"
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(writer).Encode(oidcDiscovery{
				Issuer: server.URL, AuthorizationEndpoint: server.URL + "/authorize",
				TokenEndpoint: server.URL + "/token", JWKSURI: server.URL + "/jwks",
				EndSessionEndpoint: server.URL + "/logout",
			})
		case "/token":
			require.NoError(t, request.ParseForm())
			require.Equal(t, "authorization_code", request.Form.Get("grant_type"))
			require.Equal(t, "test-code", request.Form.Get("code"))
			require.Equal(t, "test-verifier", request.Form.Get("code_verifier"))
			clientID, secret, ok := request.BasicAuth()
			require.True(t, ok)
			require.Equal(t, "agentsql", clientID)
			require.Equal(t, "provider-secret", secret)
			now := time.Now().UTC()
			token := signTestIDToken(t, privateKey, kid, map[string]any{
				"iss": server.URL, "sub": "subject-1", "aud": "agentsql", "exp": now.Add(time.Minute).Unix(),
				"iat": now.Unix(), "nonce": nonce, "preferred_username": "alice", "groups": []string{"agentsql-admins"},
			})
			_ = json.NewEncoder(writer).Encode(map[string]string{"id_token": token})
		case "/jwks":
			_ = json.NewEncoder(writer).Encode(map[string]any{"keys": []map[string]string{{
				"kty": "RSA", "kid": kid, "use": "sig", "alg": "RS256",
				"n": base64.RawURLEncoding.EncodeToString(privateKey.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(privateKey.E)).Bytes()),
			}}})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	t.Setenv("AGENTSQL_TEST_OIDC_SECRET", "provider-secret")
	provider, err := newOIDCProviderWithClient(context.Background(), config.OIDCConfig{
		IssuerURL: server.URL, ClientID: "agentsql", ClientSecretEnv: "AGENTSQL_TEST_OIDC_SECRET",
		RedirectURL:   "https://agentsql.example.test/api/v1/auth/oidc/callback",
		UsernameClaim: "preferred_username", GroupsClaim: "groups",
	}, server.Client())
	require.NoError(t, err)
	idToken, err := provider.exchange(context.Background(), "test-code", "test-verifier")
	require.NoError(t, err)
	verified, err := provider.verifyIDToken(context.Background(), idToken, nonce, time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, "alice", verified.Username)
	require.Equal(t, []string{"agentsql-admins"}, verified.Groups)

	location := appendURLQuery(server.URL+"/authorize?prompt=login", url.Values{"state": {"state-token"}})
	parsed, err := url.Parse(location)
	require.NoError(t, err)
	require.Equal(t, "login", parsed.Query().Get("prompt"))
	require.Equal(t, "state-token", parsed.Query().Get("state"))
}

func signTestIDToken(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]string{"alg": "RS256", "kid": kid, "typ": "JWT"})
	require.NoError(t, err)
	payload, err := json.Marshal(claims)
	require.NoError(t, err)
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	require.NoError(t, err)
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature)
}
