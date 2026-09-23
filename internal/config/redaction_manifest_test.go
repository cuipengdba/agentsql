package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/stretchr/testify/require"
)

func TestHashKeysManifestStructuralValidation(t *testing.T) {
	valid := manifestYAML(1, []manifestTestKey{{id: "1", keyB64: canonicalTestKey(0)}})
	_, err := parseHashKeysDocument([]byte(valid))
	require.NoError(t, err)

	tests := []struct {
		name     string
		manifest string
	}{
		{name: "unknown root", manifest: valid + "unknown: true\n"},
		{name: "duplicate root", manifest: valid + "active_version: 1\n"},
		{name: "multiple documents", manifest: valid + "---\nactive_version: 1\nkeys: []\n"},
		{name: "array root", manifest: "- id: 1\n  key_b64: " + canonicalTestKey(0)},
		{name: "wrapped root", manifest: "redaction:\n  hash_keys:\n    active_version: 1\n    keys: []\n"},
		{name: "duplicate id", manifest: manifestYAML(1, []manifestTestKey{{id: "1", keyB64: canonicalTestKey(0)}, {id: "1", keyB64: canonicalTestKey(32)}})},
		{name: "active missing", manifest: manifestYAML(2, []manifestTestKey{{id: "1", keyB64: canonicalTestKey(0)}})},
		{name: "both sources", manifest: fmt.Sprintf("active_version: 1\nkeys:\n  - id: 1\n    key_file: x\n    key_b64: %s\n", canonicalTestKey(0))},
		{name: "neither source", manifest: "active_version: 1\nkeys:\n  - id: 1\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseHashKeysDocument([]byte(test.manifest))
			require.Error(t, err)
		})
	}

	for _, id := range []string{`"02"`, "02", "2.0", "+2", "0x2", "0", "10000"} {
		t.Run("noncanonical id "+id, func(t *testing.T) {
			_, err := parseHashKeysDocument([]byte(manifestYAML(1, []manifestTestKey{{id: id, keyB64: canonicalTestKey(0)}})))
			require.Error(t, err)
		})
	}

	many := make([]manifestTestKey, 65)
	for index := range many {
		many[index] = manifestTestKey{id: fmt.Sprint(index + 1), keyB64: canonicalTestKey(byte(index))}
	}
	_, err = parseHashKeysDocument([]byte(manifestYAML(1, many)))
	require.Error(t, err)

	oversized := []byte(strings.Repeat("#", maxHashKeysManifestBytes+1))
	_, err = parseHashKeysDocument(oversized)
	require.Error(t, err)
}

func TestInlineHashKeysRawManifestLimit(t *testing.T) {
	contents := fmt.Sprintf(validConfig, filepath.ToSlash(filepath.Join(t.TempDir(), "config.db"))) +
		"redaction:\n  hash_keys:\n    active_version: 1\n    keys:\n      - id: 1\n        key_b64: " + strings.Repeat("A", maxHashKeysManifestBytes) + "\n"
	_, err := Parse([]byte(contents))
	require.ErrorIs(t, err, ErrInvalidHashKeysManifest)
}

func TestHashKeysMaterialValidation(t *testing.T) {
	key := canonicalTestKey(0)
	valid := func(value string) *RedactionHashKeysConfig {
		return &RedactionHashKeysConfig{ActiveVersion: 1, Keys: []RedactionHashKeySpec{{ID: 1, KeyB64: stringPointer(value)}}}
	}
	resolved, err := resolveHashKeys(valid(key))
	require.NoError(t, err)
	defer resolved.Clear()
	require.True(t, resolved.isMultiKey())

	for _, test := range []struct {
		name  string
		value string
		err   error
	}{
		{name: "URL safe", value: base64.URLEncoding.EncodeToString([]byte(strings.Repeat("\xff", 32)))},
		{name: "bad padding", value: strings.TrimSuffix(key, "=")},
		{name: "embedded newline", value: key[:8] + "\n" + key[8:]},
		{name: "too short", value: base64.StdEncoding.EncodeToString([]byte("0123456789abcdef")), err: mask.ErrHashKeyTooShort},
		{name: "weak", value: base64.StdEncoding.EncodeToString([]byte(strings.Repeat("a", 32))), err: ErrWeakHashKey},
		{name: "too large", value: base64.StdEncoding.EncodeToString(make([]byte, 257))},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := resolveHashKeys(valid(test.value))
			require.Error(t, err)
			if test.err != nil {
				require.ErrorIs(t, err, test.err)
			}
			require.NotContains(t, err.Error(), test.value)
		})
	}

	duplicate := &RedactionHashKeysConfig{ActiveVersion: 1, Keys: []RedactionHashKeySpec{
		{ID: 1, KeyB64: stringPointer(key)}, {ID: 2, KeyB64: stringPointer(key)},
	}}
	_, err = resolveHashKeys(duplicate)
	require.ErrorIs(t, err, ErrDuplicateHashKeyMaterial)
}

func TestHashKeyFileAtomicRulesAndLimits(t *testing.T) {
	directory := t.TempDir()
	validPath := filepath.Join(directory, "key.bin")
	require.NoError(t, os.WriteFile(validPath, diverseTestBytes(32), 0o600))
	manifest := &RedactionHashKeysConfig{ActiveVersion: 1, Keys: []RedactionHashKeySpec{{ID: 1, KeyFile: stringPointer(validPath)}}}
	resolved, err := resolveHashKeys(manifest)
	require.NoError(t, err)
	resolved.Clear()

	for _, test := range []struct {
		name     string
		contents []byte
		err      error
	}{
		{name: "empty", contents: nil, err: mask.ErrHashKeyRequired},
		{name: "short", contents: []byte("0123456789abcdef"), err: mask.ErrHashKeyTooShort},
		{name: "newline", contents: append(diverseTestBytes(0), '\n')},
		{name: "oversized", contents: make([]byte, maxHashKeyFileBytes+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(directory, test.name+".bin")
			require.NoError(t, os.WriteFile(path, test.contents, 0o600))
			_, err := resolveHashKeys(&RedactionHashKeysConfig{ActiveVersion: 1, Keys: []RedactionHashKeySpec{{ID: 1, KeyFile: stringPointer(path)}}})
			require.Error(t, err)
			if test.err != nil {
				require.ErrorIs(t, err, test.err)
			}
			require.NotContains(t, err.Error(), path)
		})
	}

	_, err = resolveHashKeys(&RedactionHashKeysConfig{ActiveVersion: 1, Keys: []RedactionHashKeySpec{{ID: 1, KeyFile: stringPointer(directory)}}})
	require.Error(t, err)
}

func TestResolveRedactionSourceMutualExclusionAndFileMode(t *testing.T) {
	manifestPath := filepath.Join(t.TempDir(), "keys.yaml")
	require.NoError(t, os.WriteFile(manifestPath, []byte(manifestYAML(1, []manifestTestKey{{id: "1", keyB64: canonicalTestKey(0)}})), 0o600))

	lookup := func(values map[string]string) func(string) (string, bool) {
		return func(name string) (string, bool) { value, exists := values[name]; return value, exists }
	}
	resolved, err := ResolveRedaction(&Config{}, lookup(map[string]string{RedactionHashKeysFileEnv: manifestPath}))
	require.NoError(t, err)
	resolved.Clear()

	for _, test := range []struct {
		name   string
		cfg    Config
		values map[string]string
		first  string
		second string
	}{
		{name: "file and inline multi", cfg: Config{Redaction: RedactionConfig{HashKeys: &RedactionHashKeysConfig{}}}, values: map[string]string{RedactionHashKeysFileEnv: manifestPath}, first: RedactionHashKeysFileEnv, second: "redaction.hash_keys"},
		{name: "file and yaml legacy", cfg: Config{Redaction: RedactionConfig{HashKey: strings.Repeat("x", 32)}}, values: map[string]string{RedactionHashKeysFileEnv: manifestPath}, first: RedactionHashKeysFileEnv, second: "redaction.hash_key"},
		{name: "file and env legacy", values: map[string]string{RedactionHashKeysFileEnv: manifestPath, RedactionHashKeyEnv: strings.Repeat("x", 32)}, first: RedactionHashKeysFileEnv, second: RedactionHashKeyEnv},
		{name: "inline and yaml legacy", cfg: Config{Redaction: RedactionConfig{HashKey: strings.Repeat("x", 32), HashKeys: &RedactionHashKeysConfig{}}}, first: "redaction.hash_keys", second: "redaction.hash_key"},
		{name: "inline and env legacy", cfg: Config{Redaction: RedactionConfig{HashKeys: &RedactionHashKeysConfig{}}}, values: map[string]string{RedactionHashKeyEnv: strings.Repeat("x", 32)}, first: "redaction.hash_keys", second: RedactionHashKeyEnv},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := ResolveRedaction(&test.cfg, lookup(test.values))
			require.ErrorIs(t, err, ErrRedactionSourceConflict)
			require.ErrorContains(t, err, test.first)
			require.ErrorContains(t, err, test.second)
		})
	}

	_, err = ResolveRedaction(&Config{}, lookup(map[string]string{RedactionHashKeysFileEnv: ""}))
	require.ErrorContains(t, err, "present but empty")
	_, err = ResolveRedaction(&Config{}, lookup(map[string]string{RedactionHashKeysFileEnv: "relative.yaml"}))
	require.ErrorContains(t, err, "absolute path")
	_, err = ResolveRedaction(&Config{}, lookup(map[string]string{RedactionHashKeyEnv: ""}))
	require.ErrorContains(t, err, "present but empty")
}

func TestDerivedRevisionGoldenAndExplicitRevision(t *testing.T) {
	commitments := map[int]string{2: strings.Repeat("b", 64), 1: strings.Repeat("a", 64)}
	require.Equal(t, "d5ed2333a35da65f", DeriveRedactionRevision(1, commitments))
	require.Equal(t, DeriveRedactionRevision(1, commitments), DeriveRedactionRevision(1, map[int]string{1: strings.Repeat("a", 64), 2: strings.Repeat("b", 64)}))

	revision := "20260923-1"
	resolved, err := resolveHashKeys(&RedactionHashKeysConfig{
		ActiveVersion: 1, Revision: &revision,
		Keys: []RedactionHashKeySpec{{ID: 1, KeyB64: stringPointer(canonicalTestKey(0))}},
	})
	require.NoError(t, err)
	assembly, err := BuildRedactionAssembly(resolved)
	require.NoError(t, err)
	require.Equal(t, revision, assembly.Observed.Revision)
	resolved.Clear()
}

func TestResolvedRedactionClearRemovesMaterial(t *testing.T) {
	marker := canonicalTestKey(0)
	resolved, err := resolveHashKeys(&RedactionHashKeysConfig{ActiveVersion: 1, Keys: []RedactionHashKeySpec{{ID: 1, KeyB64: &marker}}})
	require.NoError(t, err)
	require.NotEmpty(t, resolved.resolvedKeys)
	resolved.Clear()
	require.Empty(t, resolved.HashKey)
	require.Nil(t, resolved.HashKeys)
	require.Nil(t, resolved.resolvedKeys)
}

type manifestTestKey struct {
	id      string
	keyB64  string
	keyFile string
}

func manifestYAML(active int, keys []manifestTestKey) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "active_version: %d\nkeys:\n", active)
	for _, key := range keys {
		fmt.Fprintf(&builder, "  - id: %s\n", key.id)
		if key.keyB64 != "" {
			fmt.Fprintf(&builder, "    key_b64: %s\n", key.keyB64)
		}
		if key.keyFile != "" {
			fmt.Fprintf(&builder, "    key_file: %q\n", key.keyFile)
		}
	}
	return builder.String()
}

func canonicalTestKey(offset byte) string {
	return base64.StdEncoding.EncodeToString(diverseTestBytes(offset))
}

func diverseTestBytes(offset byte) []byte {
	value := make([]byte, 32)
	for index := range value {
		value[index] = byte(index) + offset
	}
	return value
}

func stringPointer(value string) *string { return &value }

func TestErrorsDoNotContainMarkedMaterial(t *testing.T) {
	marker := "S3_SECRET_MARKER_0123456789ABCDEF"
	marked := base64.StdEncoding.EncodeToString([]byte(strings.Repeat(marker, 2)))
	_, err := resolveHashKeys(&RedactionHashKeysConfig{ActiveVersion: 1, Keys: []RedactionHashKeySpec{
		{ID: 1, KeyB64: &marked}, {ID: 2, KeyB64: &marked},
	}})
	require.Error(t, err)
	require.False(t, errors.Is(err, nil))
	require.NotContains(t, err.Error(), marker)
}
