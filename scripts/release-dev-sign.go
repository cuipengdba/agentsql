// Command release-dev-sign creates and uses an Ed25519 development key for
// local release-candidate signing. It is not a production release signer.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const devNotice = "开发期密钥，正式发布密钥由负责人提供"

type publicKeyDocument struct {
	SchemaVersion int    `json:"schemaVersion"`
	Algorithm     string `json:"algorithm"`
	KeyID         string `json:"keyId"`
	PublicKey     string `json:"publicKeyBase64"`
	Notice        string `json:"notice"`
}

type signatureDocument struct {
	SchemaVersion  int    `json:"schemaVersion"`
	Algorithm      string `json:"algorithm"`
	KeyID          string `json:"keyId"`
	SignedArtifact string `json:"signedArtifact"`
	ArtifactSHA256 string `json:"artifactSHA256"`
	SignedPayload  string `json:"signedPayload"`
	Signature      string `json:"signatureBase64"`
	Notice         string `json:"notice"`
}

func main() {
	mode := flag.String("mode", "", "generate, sign, or verify")
	keyPath := flag.String("key", "", "base64 Ed25519 seed path")
	publicPath := flag.String("public", "", "public-key JSON path")
	inputPath := flag.String("input", "", "artifact path")
	signaturePath := flag.String("signature", "", "detached signature JSON path")
	flag.Parse()

	switch *mode {
	case "generate":
		require(*keyPath != "" && *publicPath != "", "generate requires -key and -public")
		generate(*keyPath, *publicPath)
	case "sign":
		require(*keyPath != "" && *inputPath != "" && *signaturePath != "", "sign requires -key, -input, and -signature")
		sign(*keyPath, *inputPath, *signaturePath)
	case "verify":
		require(*publicPath != "" && *inputPath != "" && *signaturePath != "", "verify requires -public, -input, and -signature")
		verify(*publicPath, *inputPath, *signaturePath)
	default:
		fatalf("-mode must be generate, sign, or verify")
	}
}

func generate(keyPath, publicPath string) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	check(err, "generate Ed25519 key")
	seed := privateKey.Seed()
	check(os.WriteFile(keyPath, []byte(base64.RawStdEncoding.EncodeToString(seed)+"\n"), 0o600), "write private seed")
	doc := publicDocument(publicKey)
	writeJSON(publicPath, doc, 0o644)
	fmt.Printf("generated %s (%s)\n", publicPath, doc.KeyID)
}

func sign(keyPath, inputPath, signaturePath string) {
	seedText, err := os.ReadFile(keyPath)
	check(err, "read private seed")
	seed, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(string(seedText)))
	check(err, "decode private seed")
	require(len(seed) == ed25519.SeedSize, "private key must be a base64 Ed25519 seed")
	privateKey := ed25519.NewKeyFromSeed(seed)
	publicKey := privateKey.Public().(ed25519.PublicKey)
	payload, err := os.ReadFile(inputPath)
	check(err, "read artifact")
	digest := sha256.Sum256(payload)
	doc := signatureDocument{
		SchemaVersion:  1,
		Algorithm:      "Ed25519",
		KeyID:          publicDocument(publicKey).KeyID,
		SignedArtifact: filepath.Base(inputPath),
		ArtifactSHA256: hex.EncodeToString(digest[:]),
		SignedPayload:  "raw artifact bytes",
		Signature:      base64.RawStdEncoding.EncodeToString(ed25519.Sign(privateKey, payload)),
		Notice:         devNotice,
	}
	writeJSON(signaturePath, doc, 0o644)
	fmt.Printf("signed %s -> %s\n", inputPath, signaturePath)
}

func verify(publicPath, inputPath, signaturePath string) {
	var publicDoc publicKeyDocument
	readJSON(publicPath, &publicDoc)
	require(publicDoc.SchemaVersion == 1 && publicDoc.Algorithm == "Ed25519" && publicDoc.Notice == devNotice, "invalid public-key document")
	publicKey, err := base64.RawStdEncoding.DecodeString(publicDoc.PublicKey)
	check(err, "decode public key")
	require(len(publicKey) == ed25519.PublicKeySize, "invalid Ed25519 public-key length")
	require(publicDoc.KeyID == publicDocument(ed25519.PublicKey(publicKey)).KeyID, "public-key ID mismatch")

	var signatureDoc signatureDocument
	readJSON(signaturePath, &signatureDoc)
	require(signatureDoc.SchemaVersion == 1 && signatureDoc.Algorithm == "Ed25519" && signatureDoc.Notice == devNotice, "invalid signature document")
	require(signatureDoc.KeyID == publicDoc.KeyID, "signature key ID mismatch")
	require(signatureDoc.SignedArtifact == filepath.Base(inputPath), "signed artifact name mismatch")
	payload, err := os.ReadFile(inputPath)
	check(err, "read artifact")
	digest := sha256.Sum256(payload)
	require(signatureDoc.ArtifactSHA256 == hex.EncodeToString(digest[:]), "artifact SHA-256 mismatch")
	signature, err := base64.RawStdEncoding.DecodeString(signatureDoc.Signature)
	check(err, "decode signature")
	require(ed25519.Verify(ed25519.PublicKey(publicKey), payload, signature), "Ed25519 signature verification failed")
	fmt.Printf("verified %s with %s\n", inputPath, publicDoc.KeyID)
}

func publicDocument(publicKey ed25519.PublicKey) publicKeyDocument {
	digest := sha256.Sum256(publicKey)
	return publicKeyDocument{
		SchemaVersion: 1,
		Algorithm:     "Ed25519",
		KeyID:         "sha256:" + hex.EncodeToString(digest[:]),
		PublicKey:     base64.RawStdEncoding.EncodeToString(publicKey),
		Notice:        devNotice,
	}
}

func readJSON(path string, target any) {
	data, err := os.ReadFile(path)
	check(err, "read JSON")
	check(json.Unmarshal(data, target), "decode JSON")
}

func writeJSON(path string, value any, mode os.FileMode) {
	data, err := json.MarshalIndent(value, "", "  ")
	check(err, "encode JSON")
	check(os.WriteFile(path, append(data, '\n'), mode), "write JSON")
}

func require(condition bool, message string) {
	if !condition {
		fatalf("%s", message)
	}
}

func check(err error, action string) {
	if err != nil {
		fatalf("%s: %v", action, err)
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
