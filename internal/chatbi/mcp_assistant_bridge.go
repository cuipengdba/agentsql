package chatbi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"sort"
	"strings"
)

// AssistantCatalogBridge is a private operator registration. Its Authorization
// is used only to derive an HMAC lookup key and is never persisted or audited.
// Registering a catalog cannot register database credentials or grant a query.
type AssistantCatalogBridge struct {
	Endpoint      string                   `json:"endpoint"`
	Authorization string                   `json:"authorization"`
	Workspace     ID                       `json:"workspace"`
	User          ID                       `json:"user"`
	Sources       []AssistantCatalogSource `json:"sources"`
	AllowedCIDRs  []string                 `json:"allowed_cidrs,omitempty"`
	AllowHTTP     bool                     `json:"allow_http,omitempty"`
	Enabled       *bool                    `json:"enabled,omitempty"`
}

type AssistantCatalogSource struct {
	CatalogID  ID `json:"catalog_id"`
	Datasource ID `json:"datasource"`
}

type assistantCatalogBridgeRecord struct {
	Endpoint     string                   `json:"endpoint"`
	Workspace    ID                       `json:"workspace"`
	User         ID                       `json:"user"`
	Sources      []AssistantCatalogSource `json:"sources"`
	AllowedCIDRs []string                 `json:"allowed_cidrs,omitempty"`
	AllowHTTP    bool                     `json:"allow_http,omitempty"`
	Enabled      bool                     `json:"enabled"`
}

func validAssistantCatalogNetworks(cidrs []string) bool {
	if len(cidrs) == 0 || len(cidrs) > 16 {
		return false
	}
	seen := map[string]bool{}
	for _, cidr := range cidrs {
		if cidr == "" || len(cidr) > 64 || seen[cidr] {
			return false
		}
		ip, network, err := net.ParseCIDR(cidr)
		if err != nil {
			return false
		}
		ones, _ := network.Mask.Size()
		if ip.To4() != nil && ones < 24 || ip.To4() == nil && ones < 64 {
			return false
		}
		seen[cidr] = true
	}
	return true
}

func (s *Store) assistantCatalogBridgeKey(endpoint string, credentials []mcpAssistantCredential) objectKey {
	normalized := append([]mcpAssistantCredential(nil), credentials...)
	for i := range normalized {
		if normalized[i].Target == "header" {
			normalized[i].Key = strings.ToLower(normalized[i].Key)
		}
	}
	sort.Slice(normalized, func(i, j int) bool {
		if normalized[i].Target != normalized[j].Target {
			return normalized[i].Target < normalized[j].Target
		}
		if normalized[i].Key != normalized[j].Key {
			return normalized[i].Key < normalized[j].Key
		}
		return normalized[i].Value < normalized[j].Value
	})
	raw, _ := json.Marshal(normalized)
	mac := hmac.New(sha256.New, s.indexKey)
	_, _ = mac.Write([]byte("agentsql:chatbi:assistant-catalog:v1\x00"))
	_, _ = mac.Write([]byte(endpoint))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(raw)
	return objectKey{"assistant-catalog", "system", "system", hex.EncodeToString(mac.Sum(nil))}
}

// resolveAssistantCatalogBridge treats the old authorization array as a
// revocable bearer credential only after an operator has bound its fingerprint
// to a real, currently enabled user and independently registered datasources.
// The caller must re-resolve before releasing any response after async work.
func (service *Service) resolveAssistantCatalogBridge(ctx context.Context, endpoint, authorization string) (Principal, map[ID]ID, int64, error) {
	credentials, err := parseMCPAssistantCatalogIdentity(endpoint, authorization)
	if err != nil {
		return Principal{}, nil, 0, ErrUnauthenticated
	}
	var bridge assistantCatalogBridgeRecord
	revision, err := service.Store.get(ctx, service.Store.assistantCatalogBridgeKey(endpoint, credentials), &bridge)
	if err != nil || !bridge.Enabled || bridge.Endpoint != endpoint || !bridge.User.Valid() || !bridge.Workspace.Valid() ||
		len(bridge.Sources) == 0 || len(bridge.Sources) > 32 {
		return Principal{}, nil, 0, ErrUnauthenticated
	}
	user, _, err := service.Store.User(ctx, bridge.User)
	if err != nil || !user.canUse(bridge.Workspace) || user.Admin {
		return Principal{}, nil, 0, ErrUnauthenticated
	}
	p := Principal{User: user, Workspace: bridge.Workspace}
	if err := service.live(ctx, p); err != nil {
		return Principal{}, nil, 0, ErrUnauthenticated
	}
	mappings := make(map[ID]ID, len(bridge.Sources))
	for _, source := range bridge.Sources {
		if !source.CatalogID.Valid() || !source.Datasource.Valid() || mappings[source.CatalogID] != "" {
			return Principal{}, nil, 0, ErrUnauthenticated
		}
		if _, _, _, err := service.bindingSnapshot(ctx, p, source.Datasource); err != nil {
			return Principal{}, nil, 0, ErrUnauthenticated
		}
		mappings[source.CatalogID] = source.Datasource
	}
	return p, mappings, revision, nil
}
