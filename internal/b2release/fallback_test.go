package b2release

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestProtocol3FallbackDrillSevenOfSevenAndRecovery(t *testing.T) {
	cases := make([]FallbackCase, 0, len(RequiredFallbackScenarios))
	for _, scenario := range RequiredFallbackScenarios {
		cases = append(cases, FallbackCase{Scenario: scenario, ObservedRoute: ExpectedFallback(scenario),
			AttestationID: fallbackTestAttestation(scenario), NoSilentAllow: true, NoUnauthorizedColumns: true,
			Recovered: true, Reactivated: true})
	}
	report, err := BuildFallbackDrill("sha256:protocol3-artifact", time.Unix(1_800_000_000, 0), cases)
	require.NoError(t, err)
	require.True(t, report.Passed)
	require.Len(t, report.Cases, 7)
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	require.NoError(t, report.Sign(privateKey))
	require.NoError(t, report.Verify(publicKey))
	require.NotEmpty(t, report.EvidenceID)
	require.NotEmpty(t, report.SignerKeyID)
	t.Logf("signed fallback drill cases=%d evidence=%s signer=%s", len(report.Cases), report.EvidenceID, report.SignerKeyID)

	tampered := report
	tampered.Cases[0].NoUnauthorizedColumns = false
	require.Error(t, tampered.Verify(publicKey))
}

func fallbackTestAttestation(scenario FallbackScenario) string {
	switch scenario {
	case ScenarioRuntimeMissing:
		return "go-test:internal/bootstrap:TestB2FactoryDefaultIsFeatureOffAndFailedActivationFallsBack:runtime-missing"
	case ScenarioExtensionMissing:
		return "go-test:internal/authorizedexecute/internal/businessdb:TestEasyDeploySelectorRulesAndNativeFallback:extension-missing"
	case ScenarioAttestationMismatch:
		return "go-test:internal/authorizedexecute/internal/businessdb:TestEasyDeploySelectorRulesAndNativeFallback:version-hash-mismatch"
	case ScenarioLeaseLost:
		return "go-test:internal/bootstrap:TestB2ExpiredLeaseIsImmediatelyDegradedAndFailClosed"
	case ScenarioActivationFailed:
		return "go-test:internal/bootstrap:TestB2FactoryDefaultIsFeatureOffAndFailedActivationFallsBack:activation-failed"
	case ScenarioNativeUnhealthy:
		return "go-test:internal/authorizedexecute/internal/businessdb:TestEasyDeploySelectorRulesAndNativeFallback:native-unhealthy"
	case ScenarioShadowDivergence:
		return "go-test:internal/authorizedexecute/internal/businessdb:TestShadowDivergenceMarksNativeUnhealthyAndSelectorFallsBack"
	default:
		return "go-test:unknown"
	}
}

func TestGAGateIsExplicitAndNeverMutates(t *testing.T) {
	evidence := completeGateEvidence()
	result := Evaluate(evidence, GateModeGA, time.Unix(1_800_000_000, 0))
	require.True(t, result.Ready)
	require.Equal(t, "GO", result.Conclusion)
	require.False(t, result.Mutated)
	require.Contains(t, result.Disclaimer, "not release-owner approval")

	evidence.VersionBumped = false
	evidence.SLO.Complete = false
	result = Evaluate(evidence, GateModeGA, time.Now())
	require.False(t, result.Ready)
	require.Equal(t, "NO-GO", result.Conclusion)
	require.Contains(t, result.Missing, "version bump")
	require.Contains(t, result.Missing, "PG14/18 closed/native >=10m SLO evidence")

	dryRun := Evaluate(evidence, GateModeDryRun, time.Now())
	require.Equal(t, "OBSERVE_ONLY", dryRun.Conclusion)
	require.False(t, dryRun.Mutated)
}

func TestGateVerifiesFallbackSignatureAndEvidenceID(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	cases := make([]FallbackCase, 0, len(RequiredFallbackScenarios))
	for _, scenario := range RequiredFallbackScenarios {
		cases = append(cases, FallbackCase{Scenario: scenario, ObservedRoute: ExpectedFallback(scenario),
			AttestationID: "attest:" + string(scenario), NoSilentAllow: true, NoUnauthorizedColumns: true,
			Recovered: true, Reactivated: true})
	}
	report, err := BuildFallbackDrill("artifact", time.Now(), cases)
	require.NoError(t, err)
	require.NoError(t, report.Sign(privateKey))
	directory := t.TempDir()
	reportBytes, err := json.Marshal(report)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "fallback.json"), reportBytes, 0o600))
	evidence := completeGateEvidence()
	evidence.Fallback.Artifact = "fallback.json"
	evidence.Fallback.Digest = report.EvidenceID
	evidencePath := filepath.Join(directory, "evidence.json")
	trustedKey := base64.RawStdEncoding.EncodeToString(publicKey)
	require.NoError(t, VerifyFallbackEvidence(evidencePath, &evidence, trustedKey))
	evidence.Fallback.Digest = "tampered"
	require.Error(t, VerifyFallbackEvidence(evidencePath, &evidence, trustedKey))
}

func completeGateEvidence() GateEvidence {
	return GateEvidence{Schema: GateEvidenceSchema, GeneratedAt: time.Now(), Commit: "commit", Version: "v0.4",
		ProductionFlagsOff: true, ZeroUnauthorized: true,
		AttackMatrix: EvidenceItem{Complete: true, Artifact: "attack.json", Digest: "sha256:a"},
		SLO:          EvidenceItem{Complete: true, Artifact: "slo.json", Digest: "sha256:s"},
		Fallback:     EvidenceItem{Complete: true, Artifact: "fallback.json", Digest: "b2-fallback:f"},
		Activation: ActivationEvidence{DryRunObserved: true, TwoPhaseVerified: true, StrongETag: `"p3-etag"`,
			CapabilityProbe: true},
		Artifacts: ArtifactEvidence{Protocol3BundleComplete: true, Signed: true, SBOM: true, Provenance: true,
			Versions: []string{"pg14-amd64", "pg18-amd64"}},
		M2SignedOff: true, M3SignedOff: true, VersionBumped: true, ReleaseBundleReady: true}
}
