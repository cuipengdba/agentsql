package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"

	"github.com/cuipengdba/agentsql/internal/mask"
	"gopkg.in/yaml.v3"
)

const (
	RedactionHashKeyEnv      = "AGENTSQL_REDACTION_HASH_KEY"
	RedactionHashKeysFileEnv = "AGENTSQL_REDACTION_HASH_KEYS_FILE"
	maxHashKeysManifestBytes = 64 << 10
	maxHashKeyFileBytes      = 8 << 10
	maxHashKeyDecodedBytes   = 256
	maxHashKeyEntries        = 64
)

var (
	ErrInvalidRedactionHashKey  = errors.New("redaction hash key must be at least 32 bytes")
	ErrRedactionSourceConflict  = errors.New("redaction hash key configuration sources conflict")
	ErrInvalidHashKeysManifest  = errors.New("redaction hash keys manifest is invalid")
	ErrWeakHashKey              = errors.New("redaction key does not satisfy the minimum diversity policy")
	ErrDuplicateHashKeyMaterial = errors.New("different redaction key versions must not use identical key material")
	canonicalKeyVersion         = regexp.MustCompile(`^[1-9][0-9]{0,3}$`)
)

// RedactionHashKeySpec is one manifest entry. Pointer source fields preserve
// the distinction between omitted and explicitly empty YAML values.
type RedactionHashKeySpec struct {
	ID      int     `yaml:"id"`
	KeyFile *string `yaml:"key_file,omitempty"`
	KeyB64  *string `yaml:"key_b64,omitempty"`
}

// RedactionHashKeysConfig is the sole accepted root shape for both inline
// redaction.hash_keys and AGENTSQL_REDACTION_HASH_KEYS_FILE.
type RedactionHashKeysConfig struct {
	ActiveVersion int                    `yaml:"active_version"`
	Keys          []RedactionHashKeySpec `yaml:"keys"`
	Revision      *string                `yaml:"revision,omitempty"`
}

// RedactionConfig contains process-local redaction secret sources. Resolved
// key bytes are unexported, copied only into the immutable assembly, and can be
// actively cleared with Clear after assembly.
type RedactionConfig struct {
	HashKey  string                   `yaml:"hash_key"`
	HashKeys *RedactionHashKeysConfig `yaml:"hash_keys,omitempty"`

	hashKeySet   bool
	hashKeysSet  bool
	resolvedKeys map[int][]byte
	multiKey     bool
}

func (spec *RedactionHashKeySpec) UnmarshalYAML(node *yaml.Node) error {
	fields, err := strictYAMLMapping(node, map[string]bool{"id": true, "key_file": true, "key_b64": true})
	if err != nil {
		return err
	}
	id, err := decodeCanonicalVersion(fields["id"], "id")
	if err != nil {
		return err
	}
	spec.ID = id
	if value, exists := fields["key_file"]; exists {
		decoded, err := decodeYAMLString(value, "key_file")
		if err != nil {
			return err
		}
		spec.KeyFile = &decoded
	}
	if value, exists := fields["key_b64"]; exists {
		decoded, err := decodeYAMLString(value, "key_b64")
		if err != nil {
			return err
		}
		spec.KeyB64 = &decoded
	}
	return nil
}

func (manifest *RedactionHashKeysConfig) UnmarshalYAML(node *yaml.Node) error {
	fields, err := strictYAMLMapping(node, map[string]bool{"active_version": true, "keys": true, "revision": true})
	if err != nil {
		return err
	}
	active, err := decodeCanonicalVersion(fields["active_version"], "active_version")
	if err != nil {
		return err
	}
	manifest.ActiveVersion = active
	keysNode, exists := fields["keys"]
	if !exists || keysNode.Kind != yaml.SequenceNode {
		return fmt.Errorf("hash_keys.keys must be a YAML sequence")
	}
	if err := keysNode.Decode(&manifest.Keys); err != nil {
		return err
	}
	if value, exists := fields["revision"]; exists {
		decoded, err := decodeYAMLString(value, "revision")
		if err != nil {
			return err
		}
		manifest.Revision = &decoded
	}
	return nil
}

func strictYAMLMapping(node *yaml.Node, allowed map[string]bool) (map[string]*yaml.Node, error) {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("hash keys value must be a YAML mapping")
	}
	fields := make(map[string]*yaml.Node, len(node.Content)/2)
	for index := 0; index < len(node.Content); index += 2 {
		nameNode, valueNode := node.Content[index], node.Content[index+1]
		if nameNode.Kind != yaml.ScalarNode || nameNode.Tag != "!!str" {
			return nil, fmt.Errorf("hash keys field name must be a string")
		}
		name := nameNode.Value
		if !allowed[name] {
			return nil, fmt.Errorf("field %q not found in redaction hash keys schema", name)
		}
		if _, duplicate := fields[name]; duplicate {
			return nil, fmt.Errorf("field %q is duplicated in redaction hash keys schema", name)
		}
		fields[name] = valueNode
	}
	return fields, nil
}

func decodeCanonicalVersion(node *yaml.Node, field string) (int, error) {
	if node == nil || node.Kind != yaml.ScalarNode || node.Tag != "!!int" || !canonicalKeyVersion.MatchString(node.Value) {
		return 0, fmt.Errorf("hash_keys.%s must be a canonical decimal integer in range 1..9999", field)
	}
	var value int
	if err := node.Decode(&value); err != nil || value < 1 || value > 9999 {
		return 0, fmt.Errorf("hash_keys.%s must be a canonical decimal integer in range 1..9999", field)
	}
	return value, nil
}

func decodeYAMLString(node *yaml.Node, field string) (string, error) {
	if node == nil || node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return "", fmt.Errorf("hash_keys.%s must be a string", field)
	}
	return node.Value, nil
}

// ResolveRedaction atomically selects one source, completes both manifest
// validation phases, and returns a minimally exposed assembly input.
func ResolveRedaction(cfg *Config, lookup func(string) (string, bool)) (RedactionConfig, error) {
	if cfg == nil {
		return RedactionConfig{}, fmt.Errorf("resolve redaction: configuration is required")
	}
	if lookup == nil {
		lookup = func(string) (string, bool) { return "", false }
	}
	keysFile, keysFileSet := lookup(RedactionHashKeysFileEnv)
	legacyEnv, legacyEnvSet := lookup(RedactionHashKeyEnv)
	inlineMulti := cfg.Redaction.HashKeys != nil || cfg.Redaction.hashKeysSet
	inlineLegacy := cfg.Redaction.HashKey != "" || cfg.Redaction.hashKeySet

	if keysFileSet {
		for _, conflict := range []struct {
			present bool
			name    string
		}{
			{inlineMulti, "redaction.hash_keys"},
			{inlineLegacy, "redaction.hash_key"},
			{legacyEnvSet, RedactionHashKeyEnv},
		} {
			if conflict.present {
				return RedactionConfig{}, sourceConflict(RedactionHashKeysFileEnv, conflict.name)
			}
		}
		if keysFile == "" {
			return RedactionConfig{}, fmt.Errorf("%s is present but empty", RedactionHashKeysFileEnv)
		}
		if !filepath.IsAbs(keysFile) {
			return RedactionConfig{}, fmt.Errorf("%s must contain an absolute path", RedactionHashKeysFileEnv)
		}
		contents, err := readBoundedRegularFile(keysFile, maxHashKeysManifestBytes, false)
		if err != nil {
			return RedactionConfig{}, fmt.Errorf("read %s manifest: %w", RedactionHashKeysFileEnv, err)
		}
		manifest, err := parseHashKeysDocument(contents)
		clearBytes(contents)
		if err != nil {
			return RedactionConfig{}, err
		}
		return resolveHashKeys(manifest)
	}

	if inlineMulti {
		if inlineLegacy {
			return RedactionConfig{}, sourceConflict("redaction.hash_keys", "redaction.hash_key")
		}
		if legacyEnvSet {
			return RedactionConfig{}, sourceConflict("redaction.hash_keys", RedactionHashKeyEnv)
		}
		if cfg.Redaction.HashKeys == nil {
			return RedactionConfig{}, fmt.Errorf("redaction.hash_keys is present but empty: %w", ErrInvalidHashKeysManifest)
		}
		return resolveHashKeys(cloneHashKeysConfig(cfg.Redaction.HashKeys))
	}

	if legacyEnvSet {
		if legacyEnv == "" {
			return RedactionConfig{}, fmt.Errorf("%s is present but empty", RedactionHashKeyEnv)
		}
		return resolveLegacyHashKey(legacyEnv)
	}
	return resolveLegacyHashKey(cfg.Redaction.HashKey)
}

func sourceConflict(first, second string) error {
	return fmt.Errorf("redaction sources %s and %s cannot be combined: %w", first, second, ErrRedactionSourceConflict)
}

func parseHashKeysDocument(contents []byte) (*RedactionHashKeysConfig, error) {
	if len(contents) == 0 {
		return nil, fmt.Errorf("redaction hash keys manifest is empty: %w", ErrInvalidHashKeysManifest)
	}
	if len(contents) > maxHashKeysManifestBytes {
		return nil, fmt.Errorf("redaction hash keys manifest exceeds 64 KiB: %w", ErrInvalidHashKeysManifest)
	}
	var manifest RedactionHashKeysConfig
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	decoder.KnownFields(true)
	if err := decoder.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("decode redaction hash keys manifest: %w", errors.Join(ErrInvalidHashKeysManifest, err))
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return nil, fmt.Errorf("redaction hash keys manifest must contain exactly one document: %w", ErrInvalidHashKeysManifest)
	} else if !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("decode trailing redaction hash keys data: %w", errors.Join(ErrInvalidHashKeysManifest, err))
	}
	if err := validateHashKeysStructure(&manifest); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func validateInlineHashKeys(contents []byte, manifest *RedactionHashKeysConfig) error {
	if manifest == nil {
		return nil
	}
	if inlineHashKeysSourceSize(contents) > maxHashKeysManifestBytes {
		return fmt.Errorf("redaction.hash_keys manifest exceeds 64 KiB: %w", ErrInvalidHashKeysManifest)
	}
	return validateHashKeysStructure(manifest)
}

func inlineHashKeysSourceSize(contents []byte) int {
	var document yaml.Node
	if yaml.Unmarshal(contents, &document) != nil || len(document.Content) == 0 {
		return 0
	}
	root := document.Content[0]
	if root.Kind != yaml.MappingNode {
		return 0
	}
	startLine, endLine := 0, lineCount(contents)+1
	for rootIndex := 0; rootIndex < len(root.Content); rootIndex += 2 {
		if root.Content[rootIndex].Value != "redaction" {
			continue
		}
		if rootIndex+2 < len(root.Content) {
			endLine = root.Content[rootIndex+2].Line
		}
		redactionNode := root.Content[rootIndex+1]
		if redactionNode.Kind != yaml.MappingNode {
			return 0
		}
		for fieldIndex := 0; fieldIndex < len(redactionNode.Content); fieldIndex += 2 {
			if redactionNode.Content[fieldIndex].Value != "hash_keys" {
				continue
			}
			startLine = redactionNode.Content[fieldIndex].Line
			if fieldIndex+2 < len(redactionNode.Content) {
				endLine = redactionNode.Content[fieldIndex+2].Line
			}
			break
		}
		break
	}
	if startLine == 0 {
		return 0
	}
	return lineOffset(contents, endLine) - lineOffset(contents, startLine)
}

func lineCount(contents []byte) int { return bytes.Count(contents, []byte{'\n'}) + 1 }

func lineOffset(contents []byte, line int) int {
	if line <= 1 {
		return 0
	}
	current := 1
	for index, value := range contents {
		if value == '\n' {
			current++
			if current == line {
				return index + 1
			}
		}
	}
	return len(contents)
}

func validateHashKeysStructure(manifest *RedactionHashKeysConfig) error {
	if manifest == nil {
		return fmt.Errorf("redaction hash keys manifest is required: %w", ErrInvalidHashKeysManifest)
	}
	if len(manifest.Keys) > maxHashKeyEntries {
		return fmt.Errorf("redaction hash keys manifest has more than 64 entries: %w", ErrInvalidHashKeysManifest)
	}
	ids := make(map[int]struct{}, len(manifest.Keys))
	activeMatches := 0
	for _, key := range manifest.Keys {
		if key.ID < 1 || key.ID > 9999 {
			return fmt.Errorf("redaction hash key id must be in range 1..9999: %w", ErrInvalidHashKeysManifest)
		}
		if _, duplicate := ids[key.ID]; duplicate {
			return fmt.Errorf("redaction hash key id %d is duplicated: %w", key.ID, ErrInvalidHashKeysManifest)
		}
		ids[key.ID] = struct{}{}
		if key.ID == manifest.ActiveVersion {
			activeMatches++
		}
		sources := 0
		if key.KeyFile != nil {
			sources++
		}
		if key.KeyB64 != nil {
			sources++
		}
		if sources != 1 {
			return fmt.Errorf("redaction hash key version %d must specify exactly one of key_file and key_b64: %w", key.ID, ErrInvalidHashKeysManifest)
		}
	}
	if manifest.ActiveVersion < 1 || manifest.ActiveVersion > 9999 || activeMatches != 1 {
		return fmt.Errorf("redaction active_version must match exactly one key entry: %w", ErrInvalidHashKeysManifest)
	}
	return nil
}

func resolveHashKeys(manifest *RedactionHashKeysConfig) (RedactionConfig, error) {
	if err := validateHashKeysStructure(manifest); err != nil {
		return RedactionConfig{}, err
	}
	defer scrubHashKeySources(manifest)
	resolved := RedactionConfig{
		HashKeys:     sanitizeHashKeysConfig(manifest),
		resolvedKeys: make(map[int][]byte, len(manifest.Keys)),
		multiKey:     true,
	}
	loaded := make([][]byte, 0, len(manifest.Keys))
	for _, key := range manifest.Keys {
		var material []byte
		var err error
		if key.KeyFile != nil {
			if *key.KeyFile == "" {
				err = errors.New("redaction key_file is empty")
			} else {
				material, err = readBoundedRegularFile(*key.KeyFile, maxHashKeyFileBytes, true)
			}
		} else {
			material, err = decodeCanonicalBase64(*key.KeyB64)
		}
		if err != nil {
			resolved.Clear()
			return RedactionConfig{}, fmt.Errorf("load redaction key version %d: %w", key.ID, err)
		}
		if len(material) == 0 {
			resolved.Clear()
			return RedactionConfig{}, fmt.Errorf("load redaction key version %d: %w", key.ID, mask.ErrHashKeyRequired)
		}
		if len(material) < 32 {
			clearBytes(material)
			resolved.Clear()
			return RedactionConfig{}, fmt.Errorf("load redaction key version %d: %w", key.ID, mask.ErrHashKeyTooShort)
		}
		if distinctByteCount(material) < 16 {
			clearBytes(material)
			resolved.Clear()
			return RedactionConfig{}, fmt.Errorf("redaction key version %d: %w", key.ID, ErrWeakHashKey)
		}
		for _, previous := range loaded {
			if bytes.Equal(previous, material) {
				clearBytes(material)
				resolved.Clear()
				return RedactionConfig{}, fmt.Errorf("redaction key version %d: %w", key.ID, ErrDuplicateHashKeyMaterial)
			}
		}
		resolved.resolvedKeys[key.ID] = material
		loaded = append(loaded, material)
	}
	return resolved, nil
}

func scrubHashKeySources(manifest *RedactionHashKeysConfig) {
	if manifest == nil {
		return
	}
	for index := range manifest.Keys {
		if manifest.Keys[index].KeyB64 != nil {
			*manifest.Keys[index].KeyB64 = ""
		}
		if manifest.Keys[index].KeyFile != nil {
			*manifest.Keys[index].KeyFile = ""
		}
	}
}

func resolveLegacyHashKey(value string) (RedactionConfig, error) {
	if value != "" && len([]byte(value)) < 32 {
		return RedactionConfig{}, fmt.Errorf(
			"validate redaction.hash_key: %w",
			errors.Join(ErrInvalidRedactionHashKey, mask.ErrHashKeyTooShort),
		)
	}
	return RedactionConfig{HashKey: value}, nil
}

func decodeCanonicalBase64(value string) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil || base64.StdEncoding.EncodeToString(decoded) != value {
		clearBytes(decoded)
		return nil, errors.New("redaction key_b64 must be canonical standard base64")
	}
	if len(decoded) > maxHashKeyDecodedBytes {
		clearBytes(decoded)
		return nil, errors.New("decoded redaction key_b64 exceeds 256 bytes")
	}
	return decoded, nil
}

func readBoundedRegularFile(path string, maximum int64, rejectNewlines bool) ([]byte, error) {
	// A preflight Stat avoids blocking on an already-present FIFO. The opened
	// descriptor is still fstat'd below and is the authoritative check.
	preflight, err := os.Stat(path)
	if err != nil {
		return nil, errors.New("stat redaction file failed")
	}
	if !preflight.Mode().IsRegular() {
		return nil, errors.New("redaction file must be a regular file")
	}
	file, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, errors.New("open redaction file failed")
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return nil, errors.New("stat redaction file failed")
	}
	if !before.Mode().IsRegular() {
		return nil, errors.New("redaction file must be a regular file")
	}
	if before.Size() > maximum {
		return nil, fmt.Errorf("redaction file exceeds %d bytes", maximum)
	}
	contents, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		clearBytes(contents)
		return nil, errors.New("read redaction file failed")
	}
	if int64(len(contents)) > maximum {
		clearBytes(contents)
		return nil, fmt.Errorf("redaction file exceeds %d bytes", maximum)
	}
	after, err := file.Stat()
	if err != nil || before.Size() != after.Size() || after.Size() != int64(len(contents)) {
		clearBytes(contents)
		return nil, errors.New("redaction file changed while it was read")
	}
	if rejectNewlines && bytes.ContainsAny(contents, "\r\n") {
		clearBytes(contents)
		return nil, errors.New("redaction key file must not contain newlines; use key_b64 for newline-bearing material")
	}
	// This is only a best-effort permission heuristic; Windows does not expose
	// POSIX mode bits here.
	if rejectNewlines && runtime.GOOS != "windows" && before.Mode().Perm() != 0 && before.Mode().Perm()&0o077 != 0 {
		slog.Warn("redaction key file permissions allow group or other access")
	}
	return contents, nil
}

func distinctByteCount(value []byte) int {
	var seen [256]bool
	count := 0
	for _, item := range value {
		if !seen[item] {
			seen[item] = true
			count++
		}
	}
	return count
}

func cloneHashKeysConfig(source *RedactionHashKeysConfig) *RedactionHashKeysConfig {
	if source == nil {
		return nil
	}
	cloned := &RedactionHashKeysConfig{ActiveVersion: source.ActiveVersion}
	if source.Revision != nil {
		value := *source.Revision
		cloned.Revision = &value
	}
	cloned.Keys = make([]RedactionHashKeySpec, len(source.Keys))
	for index, key := range source.Keys {
		cloned.Keys[index].ID = key.ID
		if key.KeyFile != nil {
			value := *key.KeyFile
			cloned.Keys[index].KeyFile = &value
		}
		if key.KeyB64 != nil {
			value := *key.KeyB64
			cloned.Keys[index].KeyB64 = &value
		}
	}
	return cloned
}

func sanitizeHashKeysConfig(source *RedactionHashKeysConfig) *RedactionHashKeysConfig {
	sanitized := &RedactionHashKeysConfig{ActiveVersion: source.ActiveVersion, Keys: make([]RedactionHashKeySpec, len(source.Keys))}
	if source.Revision != nil {
		value := *source.Revision
		sanitized.Revision = &value
	}
	for index, key := range source.Keys {
		sanitized.Keys[index].ID = key.ID
	}
	return sanitized
}

// DeriveRedactionRevision hashes canonical JSON containing only the active
// selector and sorted id-to-commitment pairs. Source bytes and paths are absent.
func DeriveRedactionRevision(activeVersion int, commitments map[int]string) string {
	ids := make([]int, 0, len(commitments))
	for id := range commitments {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	type canonicalKey struct {
		ID         int    `json:"id"`
		Commitment string `json:"commitment"`
	}
	projection := struct {
		ActiveVersion int            `json:"active_version"`
		Keys          []canonicalKey `json:"keys"`
	}{ActiveVersion: activeVersion, Keys: make([]canonicalKey, 0, len(ids))}
	for _, id := range ids {
		projection.Keys = append(projection.Keys, canonicalKey{ID: id, Commitment: commitments[id]})
	}
	encoded, _ := json.Marshal(projection)
	digest := sha256.Sum256(encoded)
	clearBytes(encoded)
	return hex.EncodeToString(digest[:])[:16]
}

func (config *RedactionConfig) Clear() {
	if config == nil {
		return
	}
	for id, key := range config.resolvedKeys {
		clearBytes(key)
		delete(config.resolvedKeys, id)
	}
	config.HashKey = ""
	if config.HashKeys != nil {
		for index := range config.HashKeys.Keys {
			if config.HashKeys.Keys[index].KeyB64 != nil {
				*config.HashKeys.Keys[index].KeyB64 = ""
			}
		}
	}
	config.resolvedKeys = nil
	config.HashKeys = nil
	config.multiKey = false
}

func (config RedactionConfig) isMultiKey() bool { return config.multiKey }

func clearBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
