package config

import (
	"errors"
	"fmt"

	"github.com/cuipengdba/agentsql/internal/mask"
)

const RedactionHashKeyEnv = "AGENTSQL_REDACTION_HASH_KEY"

var ErrInvalidRedactionHashKey = errors.New("redaction hash key must be at least 32 bytes")

// RedactionConfig contains process-local redaction secrets. HashKey is never
// persisted to the metadata store or copied into runtime request objects.
type RedactionConfig struct {
	HashKey string `yaml:"hash_key"`
}

// ResolveRedaction applies the environment override and validates explicit
// non-empty keys. Environment presence, including an empty value, overrides
// YAML exactly like the store DSN resolvers.
func ResolveRedaction(cfg *Config, lookup func(string) (string, bool)) (RedactionConfig, error) {
	if cfg == nil {
		return RedactionConfig{}, fmt.Errorf("resolve redaction: configuration is required")
	}
	if lookup == nil {
		lookup = func(string) (string, bool) { return "", false }
	}
	resolved := cfg.Redaction
	if value, ok := lookup(RedactionHashKeyEnv); ok {
		resolved.HashKey = value
	}
	if resolved.HashKey != "" && len([]byte(resolved.HashKey)) < 32 {
		return RedactionConfig{}, fmt.Errorf(
			"validate redaction.hash_key: %w",
			errors.Join(ErrInvalidRedactionHashKey, mask.ErrHashKeyTooShort),
		)
	}
	return resolved, nil
}
