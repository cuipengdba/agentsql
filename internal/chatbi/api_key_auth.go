package chatbi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
)

// VerifyAsk accepts an HS256 token signed with one of this user's active API
// keys. The access_key claim is only a lookup hint until the signature and
// current account state have both been checked.
func (identity *Identity) VerifyAsk(ctx context.Context, token string) (Principal, error) {
	deny := func() (Principal, error) { return Principal{}, ErrUnauthenticated }
	if len(token) == 0 || len(token) > 8192 {
		return deny()
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return deny()
	}
	headerRaw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(headerRaw) > 1024 {
		return deny()
	}
	var header struct {
		Algorithm string   `json:"alg"`
		Type      string   `json:"typ"`
		Critical  []string `json:"crit"`
	}
	if json.Unmarshal(headerRaw, &header) != nil || header.Algorithm != "HS256" ||
		(header.Type != "" && header.Type != "JWT") || len(header.Critical) != 0 {
		return deny()
	}
	claimRaw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(claimRaw) > 4096 {
		return deny()
	}
	var claim struct {
		Access    string      `json:"access_key"`
		Expiry    json.Number `json:"exp"`
		NotBefore json.Number `json:"nbf"`
	}
	if json.Unmarshal(claimRaw, &claim) != nil || claim.Access == "" || len(claim.Access) > 255 {
		return deny()
	}
	index := identity.store.apiKeyAccessIndex(claim.Access)
	var reference map[string]ID
	if _, err := identity.store.get(ctx, index, &reference); err != nil || !reference["id"].Valid() || !reference["uid"].Valid() {
		return deny()
	}
	var key AccountAPIKey
	if _, err := identity.store.get(ctx, apiKeyObject(reference["id"], reference["uid"]), &key); err != nil ||
		key.ID != reference["id"] || key.Owner != reference["uid"] || key.AccessKey != claim.Access || !key.Status || len(key.SecretKey) < 32 {
		return deny()
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(signature) != sha256.Size {
		return deny()
	}
	mac := hmac.New(sha256.New, []byte(key.SecretKey))
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return deny()
	}
	now := identity.now().Unix()
	if claim.Expiry != "" {
		expiry, err := claim.Expiry.Int64()
		if err != nil || expiry <= now {
			return deny()
		}
	}
	if claim.NotBefore != "" {
		start, err := claim.NotBefore.Int64()
		if err != nil || start > now {
			return deny()
		}
	}
	user, _, err := identity.store.User(ctx, key.Owner)
	if err != nil || user.ID != key.Owner || !user.canUse(user.Workspace) {
		return deny()
	}
	return Principal{User: user, Workspace: user.Workspace}, nil
}

func askRouteAllowed(r *http.Request) bool {
	path := r.URL.Path
	if strings.HasPrefix(path, "/api/v1/chat/") {
		return r.Method == http.MethodGet || r.Method == http.MethodPost || r.Method == http.MethodDelete
	}
	if r.Method == http.MethodGet {
		switch path {
		case "/api/v1/chat/list", "/api/v1/user/info", "/api/v1/user/ws", "/api/v1/datasource/list", "/api/v1/system/aimodel/default", "/api/v1/system/parameter/chat":
			return true
		}
		return strings.HasPrefix(path, "/api/v1/datasource/ws/") || strings.HasPrefix(path, "/api/v1/datasource/check/")
	}
	if r.Method == http.MethodPost {
		for _, prefix := range []string{"/api/v1/datasource/get/", "/api/v1/datasource/getTables/", "/api/v1/datasource/getFields/", "/api/v1/datasource/tableList/", "/api/v1/datasource/fieldList/", "/api/v1/datasource/previewData/"} {
			if strings.HasPrefix(path, prefix) {
				return true
			}
		}
	}
	return false
}
