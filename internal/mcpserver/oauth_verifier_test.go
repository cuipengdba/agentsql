package mcpserver

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/rbac"
	"github.com/stretchr/testify/require"
)

func TestOAuthAccessVerifierChecksJWTAndLiveLocalPermission(t *testing.T) {
	fixture := newMCPFixture(t, "readonly")
	ctx := context.Background()
	roles := rbac.NewService(fixture.runtime.Store.RBAC())
	require.NoError(t, roles.Bootstrap(ctx, "admin", "test-password"))
	user, err := fixture.runtime.Store.HumanAuth().CreateExternalIdentity(ctx, model.User{
		ID: "oauth-user", TenantID: rbac.DefaultTenantID, Username: "oauth-user", DisplayName: "OAuth User",
	}, "oidc", "issuer-subject")
	require.NoError(t, err)
	require.NoError(t, fixture.runtime.Store.RBAC().SetUserRoles(ctx, user.TenantID, user.ID, []string{rbac.OperatorRoleID}))
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	kid := "key-1"
	var issuer string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(writer).Encode(map[string]string{"issuer": issuer, "jwks_uri": issuer + "/jwks"})
		case "/jwks":
			jwk := map[string]any{"x5c": []string{"ignored-certificate-chain"}}
			for name, value := range oauthTestJWK(&key.PublicKey, kid) {
				jwk[name] = value
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{"keys": []map[string]any{jwk}})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	issuer = server.URL
	cfg := config.MCPOAuthConfig{
		Enabled: true, IssuerURL: issuer, ResourceURL: "http://127.0.0.1:7780/mcp/oauth",
		TenantID: rbac.DefaultTenantID, RequiredScope: "agentsql:mcp",
	}
	verifier, err := newOAuthAccessVerifierWithClient(ctx, cfg, fixture.runtime, server.Client())
	require.NoError(t, err)
	now := time.Now().Unix()
	valid := map[string]any{
		"iss": issuer, "sub": "issuer-subject", "aud": cfg.ResourceURL,
		"scope": "openid agentsql:mcp", "iat": now - 5, "exp": now + 300,
	}
	sign := func(claims map[string]any) string { return oauthTestJWT(t, key, kid, claims) }
	principal, err := verifier.Verify(ctx, sign(valid), fixture.agent.ID)
	require.NoError(t, err)
	require.Equal(t, user.ID, principal.UserID)
	require.Equal(t, rbac.DefaultTenantID, principal.TenantID)

	for _, item := range []struct {
		name string
		edit func(map[string]any)
	}{
		{"wrong issuer", func(claims map[string]any) { claims["iss"] = "http://other" }},
		{"wrong audience", func(claims map[string]any) { claims["aud"] = "http://127.0.0.1:7780/mcp" }},
		{"expired", func(claims map[string]any) { claims["exp"] = now - 1 }},
		{"future not before", func(claims map[string]any) { claims["nbf"] = now + 300 }},
		{"long lifetime", func(claims map[string]any) { claims["exp"] = now + 7200 }},
		{"unknown subject", func(claims map[string]any) { claims["sub"] = "unmapped" }},
		{"missing scope", func(claims map[string]any) { claims["scope"] = "openid" }},
	} {
		t.Run(item.name, func(t *testing.T) {
			claims := make(map[string]any, len(valid))
			for name, value := range valid {
				claims[name] = value
			}
			item.edit(claims)
			_, err := verifier.Verify(ctx, sign(claims), fixture.agent.ID)
			require.Error(t, err)
		})
	}
	wrongKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	_, err = verifier.Verify(ctx, oauthTestJWT(t, wrongKey, kid, valid), fixture.agent.ID)
	require.Error(t, err)
	require.NoError(t, fixture.runtime.Store.RBAC().SetUserRoles(ctx, user.TenantID, user.ID, nil))
	_, err = verifier.Verify(ctx, sign(valid), fixture.agent.ID)
	require.ErrorIs(t, err, errOAuthForbidden)
	key, kid = wrongKey, "key-2"
	require.NoError(t, fixture.runtime.Store.RBAC().SetUserRoles(ctx, user.TenantID, user.ID, []string{rbac.OperatorRoleID}))
	_, err = verifier.Verify(ctx, sign(valid), fixture.agent.ID)
	require.NoError(t, err, "new kid should refresh JWKS")
}

func oauthTestJWK(key *rsa.PublicKey, kid string) map[string]string {
	return map[string]string{
		"kty": "RSA", "alg": "RS256", "use": "sig", "kid": kid,
		"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
	}
}

func oauthTestJWT(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": kid})
	require.NoError(t, err)
	payload, err := json.Marshal(claims)
	require.NoError(t, err)
	unsigned := fmt.Sprintf("%s.%s", base64.RawURLEncoding.EncodeToString(header), base64.RawURLEncoding.EncodeToString(payload))
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	require.NoError(t, err)
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature)
}
