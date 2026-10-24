// Package releaseverify verifies an immutable, local AgentSQL release directory.
// It never downloads, extracts, installs, or executes its inputs.
package releaseverify

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const maxDocument = 32 << 20
const maxPackage = 2 << 30

var versionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)
var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

type PublicKey struct {
	SchemaVersion int    `json:"schemaVersion"`
	Algorithm     string `json:"algorithm"`
	KeyClass      string `json:"keyClass"`
	KeyID         string `json:"keyId"`
	PublicKey     string `json:"publicKeyBase64"`
}

type signature struct {
	SchemaVersion  int    `json:"schemaVersion"`
	Algorithm      string `json:"algorithm"`
	KeyClass       string `json:"keyClass"`
	KeyID          string `json:"keyId"`
	SignedArtifact string `json:"signedArtifact"`
	ArtifactSHA256 string `json:"artifactSHA256"`
	SignedPayload  string `json:"signedPayload"`
	Signature      string `json:"signatureBase64"`
}

type Report struct {
	Version      string `json:"version"`
	SourceCommit string `json:"source_commit"`
	KeyID        string `json:"key_id"`
	Assets       int    `json:"assets"`
	Signatures   int    `json:"signatures"`
	ReadOnly     bool   `json:"read_only"`
	Status       string `json:"status"`
}

func Names(version string) []string {
	return []string{
		"agentsql-" + version + "-linux-amd64.tar.gz", "agentsql-" + version + "-linux-amd64.tar.gz.sha256",
		"agentsql-" + version + "-linux-arm64.tar.gz", "agentsql-" + version + "-linux-arm64.tar.gz.sha256",
		"agentsql-" + version + ".spdx.json", "agentsql-" + version + ".spdx.json.sig.json",
		"ed25519-release-public-key.json", "go-version-metadata.txt", "install.sh", "provenance.json",
		"provenance.json.sig.json", "SBOM-GENERATION.txt", "VERIFYING-SIGNATURES.md", "SHA256SUMS", "SHA256SUMS.sig.json",
	}
}

// Verify requires a public key obtained independently of the release directory.
// Keep the directory immutable during verification and until installation.
func Verify(ctx context.Context, directory, publicPath, version string) (Report, error) {
	report, err := verify(ctx, directory, publicPath, version)
	if err != nil {
		return Report{}, fmt.Errorf("release verification failed: %w", err)
	}
	return report, nil
}

func verify(ctx context.Context, directory, publicPath, version string) (Report, error) {
	fail := func(reason string) (Report, error) { return Report{}, errors.New(reason) }
	if !versionPattern.MatchString(version) {
		return fail("explicit version must have the form vX.Y.Z")
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fail("release directory must be a local directory, not a symlink")
	}
	dirResolved, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return fail("cannot resolve release directory")
	}
	keyResolved, err := filepath.EvalSymlinks(publicPath)
	if err != nil {
		return fail("cannot resolve trusted public key")
	}
	dirAbs, err := filepath.Abs(dirResolved)
	if err != nil {
		return fail("cannot resolve release directory")
	}
	keyAbs, err := filepath.Abs(keyResolved)
	if err != nil {
		return fail("cannot resolve trusted public key")
	}
	rel, err := filepath.Rel(dirAbs, keyAbs)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fail("trusted public key must be outside the release directory")
	}
	keyBefore, err := os.Stat(publicPath)
	if err != nil || !keyBefore.Mode().IsRegular() || keyBefore.Size() > 16384 {
		return fail("invalid trusted public key file")
	}
	keyFile, err := os.Open(publicPath)
	if err != nil {
		return fail("cannot read trusted public key")
	}
	keyInfo, statErr := keyFile.Stat()
	if statErr != nil || !keyInfo.Mode().IsRegular() || !os.SameFile(keyBefore, keyInfo) || keyInfo.Size() > 16384 {
		keyFile.Close()
		return fail("invalid trusted public key file")
	}
	keyBytes, readErr := io.ReadAll(io.LimitReader(keyFile, 16385))
	closeErr := keyFile.Close()
	if readErr != nil || closeErr != nil || len(keyBytes) > 16384 {
		return fail("cannot read trusted public key")
	}
	keyDoc, pub, err := decodeKey(keyBytes)
	if err != nil {
		return fail("invalid release public key")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return fail("cannot open release directory")
	}
	defer root.Close()
	entriesFile, err := root.Open(".")
	if err != nil {
		return fail("cannot enumerate release directory")
	}
	entries, readErr := entriesFile.ReadDir(17)
	closeErr = entriesFile.Close()
	if readErr != nil && readErr != io.EOF || closeErr != nil || len(entries) != 15 {
		return fail("expected exactly 15 release assets")
	}
	expected := make(map[string]bool)
	for _, name := range Names(version) {
		expected[name] = true
	}
	for _, entry := range entries {
		if !expected[entry.Name()] {
			return fail("unexpected release asset")
		}
	}
	manifest, err := readFile(root, "SHA256SUMS", 16384)
	if err != nil {
		return fail("invalid SHA256SUMS file")
	}
	if err := verifySignature(root, "SHA256SUMS", manifest, keyDoc, pub); err != nil {
		return fail("SHA256SUMS signature rejected")
	}
	digests, err := parseManifest(manifest)
	if err != nil || len(digests) != 13 {
		return fail("invalid signed asset inventory")
	}
	sizes := make(map[string]int64)
	for _, name := range Names(version) {
		if name == "SHA256SUMS" || name == "SHA256SUMS.sig.json" {
			continue
		}
		want, ok := digests[name]
		if !ok {
			return fail("signed inventory does not match the expected release")
		}
		limit := int64(maxDocument)
		if strings.HasSuffix(name, ".tar.gz") {
			limit = maxPackage
		}
		actual, size, err := hashFile(ctx, root, name, limit)
		if err != nil {
			return fail("cannot hash release asset: " + name)
		}
		if actual != want {
			return fail("asset digest mismatch: " + name)
		}
		sizes[name] = size
	}
	bundledKey, err := readFile(root, "ed25519-release-public-key.json", 16384)
	if err != nil {
		return fail("invalid bundled public key")
	}
	bundledDoc, _, err := decodeKey(bundledKey)
	if err != nil || bundledDoc != keyDoc {
		return fail("bundled key differs from independently trusted key")
	}
	for _, name := range []string{"agentsql-" + version + ".spdx.json", "provenance.json"} {
		payload, err := readFile(root, name, maxDocument)
		if err != nil || digest(payload) != digests[name] {
			return fail("signed document changed during verification")
		}
		if err := verifySignature(root, name, payload, keyDoc, pub); err != nil {
			return fail("signature rejected: " + name)
		}
		if strings.HasSuffix(name, ".spdx.json") {
			var sbom struct {
				SPDXVersion string            `json:"spdxVersion"`
				SPDXID      string            `json:"SPDXID"`
				Name        string            `json:"name"`
				Namespace   string            `json:"documentNamespace"`
				Packages    []json.RawMessage `json:"packages"`
			}
			if err := decodeJSON(payload, &sbom, false); err != nil || sbom.SPDXVersion != "SPDX-2.3" || sbom.SPDXID != "SPDXRef-DOCUMENT" || sbom.Name != "agentsql" || sbom.Namespace != "https://github.com/cuipengdba/agentsql/releases/download/"+version+"/"+name || len(sbom.Packages) == 0 {
				return fail("SBOM identity does not match the expected release")
			}
		}
	}
	for _, arch := range []string{"amd64", "arm64"} {
		name := "agentsql-" + version + "-linux-" + arch + ".tar.gz"
		data, err := readFile(root, name+".sha256", 1024)
		if err != nil || digest(data) != digests[name+".sha256"] {
			return fail("package checksum file changed")
		}
		entries, err := parseManifest(data)
		if err != nil || len(entries) != 1 || entries[name] != digests[name] {
			return fail("package checksum does not match signed inventory")
		}
	}
	provenance, err := readFile(root, "provenance.json", maxDocument)
	if err != nil || digest(provenance) != digests["provenance.json"] {
		return fail("provenance changed during verification")
	}
	commit, err := checkProvenance(provenance, version, digests, sizes)
	if err != nil {
		return fail("invalid official release provenance")
	}
	if err := ctx.Err(); err != nil {
		return fail("verification canceled")
	}
	return Report{Version: version, SourceCommit: commit, KeyID: keyDoc.KeyID, Assets: 15, Signatures: 3, ReadOnly: true, Status: "verified"}, nil
}

func decodeKey(data []byte) (PublicKey, ed25519.PublicKey, error) {
	var doc PublicKey
	if err := decodeJSON(data, &doc, true); err != nil {
		return doc, nil, err
	}
	pub, err := base64.RawStdEncoding.DecodeString(doc.PublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize || doc.SchemaVersion != 1 || doc.Algorithm != "Ed25519" || doc.KeyClass != "release" || doc.KeyID != "sha256:"+digest(pub) {
		return doc, nil, errors.New("invalid key")
	}
	return doc, ed25519.PublicKey(pub), nil
}

func verifySignature(root *os.Root, name string, payload []byte, key PublicKey, pub ed25519.PublicKey) error {
	data, err := readFile(root, name+".sig.json", 16384)
	if err != nil {
		return err
	}
	var sig signature
	if err := decodeJSON(data, &sig, true); err != nil {
		return err
	}
	value, err := base64.RawStdEncoding.DecodeString(sig.Signature)
	if err != nil || sig.SchemaVersion != 1 || sig.Algorithm != "Ed25519" || sig.KeyClass != "release" || sig.KeyID != key.KeyID || sig.SignedArtifact != name || sig.ArtifactSHA256 != digest(payload) || sig.SignedPayload != "raw artifact bytes" || !ed25519.Verify(pub, payload, value) {
		return errors.New("invalid signature")
	}
	return nil
}

func parseManifest(data []byte) (map[string]string, error) {
	result := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if len(line) < 67 || line[64:66] != "  " || !hashPattern.MatchString(line[:64]) {
			return nil, errors.New("invalid checksum line")
		}
		name := line[66:]
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\:\x00\r\n") || strings.TrimSpace(name) != name {
			return nil, errors.New("unsafe asset name")
		}
		if _, exists := result[name]; exists {
			return nil, errors.New("duplicate asset")
		}
		result[name] = line[:64]
	}
	return result, nil
}

func openFile(root *os.Root, name string, limit int64) (*os.File, error) {
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("invalid asset file")
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, errors.New("cannot open asset")
	}
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) || opened.Size() > limit {
		f.Close()
		return nil, errors.New("asset changed")
	}
	return f, nil
}

func readFile(root *os.Root, name string, limit int64) ([]byte, error) {
	f, err := openFile(root, name, limit)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(f, limit+1))
	closeErr := f.Close()
	if readErr != nil || closeErr != nil || int64(len(data)) > limit {
		return nil, errors.New("cannot read bounded asset")
	}
	return data, nil
}

func hashFile(ctx context.Context, root *os.Root, name string, limit int64) (string, int64, error) {
	f, err := openFile(root, name, limit)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	var size int64
	buffer := make([]byte, 128<<10)
	for {
		if err := ctx.Err(); err != nil {
			return "", 0, err
		}
		n, err := f.Read(buffer)
		size += int64(n)
		if size > limit {
			return "", 0, errors.New("asset exceeds size limit")
		}
		h.Write(buffer[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", 0, err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), size, nil
}

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func checkProvenance(data []byte, version string, digests map[string]string, sizes map[string]int64) (string, error) {
	var doc struct {
		SchemaVersion int    `json:"schemaVersion"`
		DocumentType  string `json:"documentType"`
		Version       string `json:"version"`
		Source        struct {
			Repository string `json:"repository"`
			Commit     string `json:"commit"`
			Dirty      *bool  `json:"dirtyAtBuild"`
		} `json:"source"`
		Subjects []struct {
			Name   string `json:"name"`
			Size   int64  `json:"size"`
			Digest struct {
				SHA256 string `json:"sha256"`
			} `json:"digest"`
		} `json:"subjects"`
	}
	if err := decodeJSON(data, &doc, false); err != nil {
		return "", err
	}
	if doc.SchemaVersion != 1 || doc.DocumentType != "AgentSQL official release provenance" || doc.Version != version || doc.Source.Repository != "https://github.com/cuipengdba/agentsql" || !commitPattern.MatchString(doc.Source.Commit) || doc.Source.Dirty == nil || *doc.Source.Dirty || len(doc.Subjects) != 8 {
		return "", errors.New("invalid source")
	}
	expected := map[string]bool{"agentsql-" + version + "-linux-amd64.tar.gz": true, "agentsql-" + version + "-linux-arm64.tar.gz": true, "agentsql-" + version + "-linux-amd64.tar.gz.sha256": true, "agentsql-" + version + "-linux-arm64.tar.gz.sha256": true, "agentsql-" + version + ".spdx.json": true, "install.sh": true, "go-version-metadata.txt": true, "SBOM-GENERATION.txt": true}
	for _, subject := range doc.Subjects {
		if !expected[subject.Name] || subject.Digest.SHA256 != digests[subject.Name] || subject.Size != sizes[subject.Name] {
			return "", errors.New("invalid subject")
		}
		delete(expected, subject.Name)
	}
	return doc.Source.Commit, nil
}

// Reject duplicate JSON keys before decoding; otherwise different parsers can
// disagree about a signed document's meaning. Bound nesting for hostile input.
func decodeJSON(data []byte, target any, strict bool) error {
	d := json.NewDecoder(bytes.NewReader(data))
	if err := checkJSONValue(d, 0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing JSON data")
	}
	d = json.NewDecoder(bytes.NewReader(data))
	if strict {
		d.DisallowUnknownFields()
	}
	return d.Decode(target)
}

func checkJSONValue(d *json.Decoder, depth int) error {
	if depth > 64 {
		return errors.New("JSON nesting limit exceeded")
	}
	token, err := d.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return errors.New("duplicate JSON key")
			}
			seen[name] = true
			if err := checkJSONValue(d, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := checkJSONValue(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("unexpected delimiter")
	}
	_, err = d.Token()
	return err
}
