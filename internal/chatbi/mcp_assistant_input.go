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
	if strings.TrimSpace(input.Question) == "" || len(input.Question) > 16000 ||
		input.URL == "" || len(input.URL) > 2048 || strings.TrimSpace(input.URL) != input.URL ||
		input.Authorization == "" || len(input.Authorization) > 8192 {
		return nil, false, errInvalidMCPAssistantInput
	}
	u, err := url.Parse(input.URL)
	if err != nil || u.Opaque != "" || u.Host == "" || u.Hostname() == "" || u.User != nil ||
		u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") || u.String() != input.URL {
		return nil, false, errInvalidMCPAssistantInput
	}
	var credentials []mcpAssistantCredential
	if json.Unmarshal([]byte(input.Authorization), &credentials) != nil || len(credentials) == 0 || len(credentials) > 16 {
		return nil, false, errInvalidMCPAssistantInput
	}
	seen := map[string]bool{}
	for _, item := range credentials {
		if item.Target != "header" && item.Target != "cookie" && item.Target != "param" ||
			item.Key == "" || len(item.Key) > 128 || len(item.Value) > 2048 || item.Value == "" ||
			!mcpAssistantCredentialKey(item.Key) || strings.ContainsAny(item.Value, "\r\n\x00") {
			return nil, false, errInvalidMCPAssistantInput
		}
		key := item.Target + "\x00" + strings.ToLower(item.Key)
		if seen[key] {
			return nil, false, errInvalidMCPAssistantInput
		}
		seen[key] = true
	}
	stream := input.Stream == nil || *input.Stream
	return credentials, stream, nil
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
