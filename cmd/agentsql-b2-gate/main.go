package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/b2release"
)

func main() {
	modeText := flag.String("mode", string(b2release.GateModeGA), "dry-run, activation, or ga")
	evidencePath := flag.String("evidence", "", "path to the B2 release evidence JSON")
	outputPath := flag.String("output", "", "optional path for the gate result JSON")
	publicKeyPath := flag.String("fallback-public-key", "", "path to the trusted base64 Ed25519 fallback public key")
	flag.Parse()
	if *evidencePath == "" {
		fatalf("-evidence is required")
	}
	evidence, err := b2release.LoadEvidence(*evidencePath)
	if err != nil {
		fatalf("load evidence: %v", err)
	}
	trustedPublicKey := ""
	if *publicKeyPath != "" {
		contents, readErr := os.ReadFile(*publicKeyPath)
		if readErr != nil {
			fatalf("read fallback public key: %v", readErr)
		}
		trustedPublicKey = strings.TrimSpace(string(contents))
	}
	if evidence.Fallback.Complete {
		if err := b2release.VerifyFallbackEvidence(*evidencePath, &evidence, trustedPublicKey); err != nil {
			evidence.Fallback.Complete = false
		}
	}
	_ = b2release.VerifyReferencedArtifacts(*evidencePath, &evidence)
	mode := b2release.GateMode(*modeText)
	result := b2release.Evaluate(evidence, mode, time.Now())
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fatalf("encode result: %v", err)
	}
	encoded = append(encoded, '\n')
	if *outputPath != "" {
		if err := os.WriteFile(*outputPath, encoded, 0o600); err != nil {
			fatalf("write result: %v", err)
		}
	}
	_, _ = os.Stdout.Write(encoded)
	if mode != b2release.GateModeDryRun && !result.Ready {
		os.Exit(2)
	}
}

func fatalf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
