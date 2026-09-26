package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/b2release"
)

type observations struct {
	ArtifactDigest string                   `json:"artifact_digest"`
	CreatedAt      time.Time                `json:"created_at"`
	Cases          []b2release.FallbackCase `json:"cases"`
}

func main() {
	inputPath := flag.String("observations", "", "path to seven fallback observations")
	keyPath := flag.String("signing-key", "", "path to a base64 Ed25519 seed or private key")
	outputPath := flag.String("output", "", "path for signed fallback evidence")
	flag.Parse()
	if *inputPath == "" || *keyPath == "" || *outputPath == "" {
		fatalf("-observations, -signing-key, and -output are required")
	}
	input, err := os.ReadFile(*inputPath)
	if err != nil {
		fatalf("read observations: %v", err)
	}
	var observed observations
	if err := json.Unmarshal(input, &observed); err != nil {
		fatalf("decode observations: %v", err)
	}
	if observed.CreatedAt.IsZero() {
		observed.CreatedAt = time.Now().UTC()
	}
	report, err := b2release.BuildFallbackDrill(observed.ArtifactDigest, observed.CreatedAt, observed.Cases)
	if err != nil {
		fatalf("validate observations: %v", err)
	}
	keyBytes, err := os.ReadFile(*keyPath)
	if err != nil {
		fatalf("read signing key: %v", err)
	}
	decoded, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(string(keyBytes)))
	if err != nil {
		fatalf("decode signing key: %v", err)
	}
	var privateKey ed25519.PrivateKey
	switch len(decoded) {
	case ed25519.SeedSize:
		privateKey = ed25519.NewKeyFromSeed(decoded)
	case ed25519.PrivateKeySize:
		privateKey = ed25519.PrivateKey(decoded)
	default:
		fatalf("signing key must decode to %d-byte seed or %d-byte private key", ed25519.SeedSize, ed25519.PrivateKeySize)
	}
	if err := report.Sign(privateKey); err != nil {
		fatalf("sign report: %v", err)
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fatalf("encode report: %v", err)
	}
	if err := os.WriteFile(*outputPath, append(encoded, '\n'), 0o600); err != nil {
		fatalf("write report: %v", err)
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)
	_, _ = fmt.Fprintf(os.Stdout, "evidence_id=%s\nsigner_key_id=%s\npublic_key=%s\n", report.EvidenceID,
		report.SignerKeyID, base64.RawStdEncoding.EncodeToString(publicKey))
}

func fatalf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
