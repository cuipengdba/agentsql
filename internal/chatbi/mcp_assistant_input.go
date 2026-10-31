package chatbi

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
)

// mcpAssistantInput is the unchanged SQLBot third-party datasource-catalog
// request. Parsing it grants no authority to fetch a URL or use a datasource.
type mcpAssistantInput struct {
	Question      string `json:"question"`
	URL           string `json:"url"`
	Authorization string `json:"authorization"`
	Stream        *bool  `json:"stream"`
}

type mcpAssistantCredential struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Target string `json:"target"`
}

var errInvalidMCPAssistantInput = errors.New("invalid SQLBot third-party assistant request")

// parseMCPAssistantInput validates the public wire shape before a future
// server-owned catalog mapping can be considered. The authorization field is
// a JSON-encoded array in the original API, not an AgentSQL login token.
func parseMCPAssistantInput(input mcpAssistantInput) ([]mcpAssistantCredential, bool, error) {
	if strings.TrimSpace(input.Question) == "" || len(input.Question) > 16000 {
		return nil, false, errInvalidMCPAssistantInput
	}
	credentials, err := parseMCPAssistantCatalogIdentity(input.URL, input.Authorization)
	if err != nil {
		return nil, false, err
	}
	stream := input.Stream == nil || *input.Stream
	return credentials, stream, nil
}

func parseMCPAssistantCatalogIdentity(endpoint, authorization string) ([]mcpAssistantCredential, error) {
	if endpoint == "" || len(endpoint) > 2048 || strings.TrimSpace(endpoint) != endpoint ||
		authorization == "" || len(authorization) > 8192 {
		return nil, errInvalidMCPAssistantInput
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Opaque != "" || u.Host == "" || u.Hostname() == "" || u.User != nil ||
		u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") || u.String() != endpoint {
		return nil, errInvalidMCPAssistantInput
	}
	var credentials []mcpAssistantCredential
	if json.Unmarshal([]byte(authorization), &credentials) != nil || len(credentials) == 0 || len(credentials) > 16 {
		return nil, errInvalidMCPAssistantInput
	}
	seen := map[string]bool{}
	for _, item := range credentials {
		if item.Target != "header" && item.Target != "cookie" && item.Target != "param" ||
			item.Key == "" || len(item.Key) > 128 || len(item.Value) > 2048 || item.Value == "" ||
			!mcpAssistantCredentialKey(item.Key) || strings.ContainsAny(item.Value, "\r\n\x00") {
			return nil, errInvalidMCPAssistantInput
		}
		if item.Target == "header" {
			switch strings.ToLower(item.Key) {
			case "host", "connection", "proxy-connection", "proxy-authorization", "transfer-encoding", "content-length", "te", "trailer", "upgrade", "keep-alive":
				return nil, errInvalidMCPAssistantInput
			}
		}
		key := item.Target + "\x00" + strings.ToLower(item.Key)
		if seen[key] {
			return nil, errInvalidMCPAssistantInput
		}
		seen[key] = true
	}
	return credentials, nil
}

func mcpAssistantCredentialKey(key string) bool {
	for _, c := range key {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			strings.ContainsRune("!#$%&'*+-.^_`|~", c) {
			continue
		}
		return false
	}
	return true
}
