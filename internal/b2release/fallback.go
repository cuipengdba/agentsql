package b2release

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

const FallbackDrillSchema = "agentsql.b2.protocol3-fallback-drill/v1"

type FallbackScenario string

const (
	ScenarioRuntimeMissing      FallbackScenario = "protocol3_runtime_missing"
	ScenarioExtensionMissing    FallbackScenario = "native_extension_missing"
	ScenarioAttestationMismatch FallbackScenario = "native_version_or_hash_mismatch"
	ScenarioLeaseLost           FallbackScenario = "heartbeat_or_lease_lost"
	ScenarioActivationFailed    FallbackScenario = "activation_failed"
	ScenarioNativeUnhealthy     FallbackScenario = "native_unhealthy"
	ScenarioShadowDivergence    FallbackScenario = "shadow_divergence"
)

var RequiredFallbackScenarios = []FallbackScenario{
	ScenarioRuntimeMissing,
	ScenarioExtensionMissing,
	ScenarioAttestationMismatch,
	ScenarioLeaseLost,
	ScenarioActivationFailed,
	ScenarioNativeUnhealthy,
	ScenarioShadowDivergence,
}

type FallbackRoute string

const (
	RouteClosed     FallbackRoute = "closed"
	RouteTableLevel FallbackRoute = "table_level"
	RouteFailClosed FallbackRoute = "fail_closed"
)

type FallbackCase struct {
	Scenario              FallbackScenario `json:"scenario"`
	ObservedRoute         FallbackRoute    `json:"observed_route"`
	ExpectedRoute         FallbackRoute    `json:"expected_route"`
	AttestationID         string           `json:"attestation_id"`
	NoSilentAllow         bool             `json:"no_silent_allow"`
	NoUnauthorizedColumns bool             `json:"no_unauthorized_columns"`
	Recovered             bool             `json:"recovered"`
	Reactivated           bool             `json:"reactivated"`
	Passed                bool             `json:"passed"`
}

type FallbackDrillReport struct {
	Schema         string         `json:"schema"`
	CreatedAt      time.Time      `json:"created_at"`
	ArtifactDigest string         `json:"artifact_digest"`
	Cases          []FallbackCase `json:"cases"`
	Passed         bool           `json:"passed"`
	EvidenceID     string         `json:"evidence_id"`
	SignerKeyID    string         `json:"signer_key_id"`
	Signature      string         `json:"signature"`
}

// ExpectedFallback encodes the asymmetric safety rule. A missing runtime or
// failed activation is allowed to preserve the pre-existing table-level path
// only before B2 classification. Once protocol 3 was active, lease loss must
// remain on the B2 route and fail closed. Native-only faults may use the
// independently attested closed subset; divergence itself is never allowed.
func ExpectedFallback(scenario FallbackScenario) FallbackRoute {
	switch scenario {
	case ScenarioRuntimeMissing, ScenarioActivationFailed:
		return RouteTableLevel
	case ScenarioExtensionMissing, ScenarioAttestationMismatch, ScenarioNativeUnhealthy:
		return RouteClosed
	case ScenarioLeaseLost, ScenarioShadowDivergence:
		return RouteFailClosed
	default:
		return RouteFailClosed
	}
}

func BuildFallbackDrill(artifactDigest string, createdAt time.Time, cases []FallbackCase) (FallbackDrillReport, error) {
	if artifactDigest == "" || createdAt.IsZero() {
		return FallbackDrillReport{}, errors.New("fallback drill requires artifact digest and timestamp")
	}
	seen := make(map[FallbackScenario]bool, len(cases))
	copyCases := append([]FallbackCase(nil), cases...)
	for index := range copyCases {
		value := &copyCases[index]
		if seen[value.Scenario] || value.AttestationID == "" {
			return FallbackDrillReport{}, fmt.Errorf("invalid or duplicate fallback case %q", value.Scenario)
		}
		seen[value.Scenario] = true
		value.ExpectedRoute = ExpectedFallback(value.Scenario)
		value.Passed = value.ObservedRoute == value.ExpectedRoute && value.NoSilentAllow && value.NoUnauthorizedColumns && value.Recovered && value.Reactivated
	}
	for _, scenario := range RequiredFallbackScenarios {
		if !seen[scenario] {
			return FallbackDrillReport{}, fmt.Errorf("missing fallback case %q", scenario)
		}
	}
	sort.Slice(copyCases, func(i, j int) bool { return copyCases[i].Scenario < copyCases[j].Scenario })
	report := FallbackDrillReport{Schema: FallbackDrillSchema, CreatedAt: createdAt.UTC(), ArtifactDigest: artifactDigest, Cases: copyCases, Passed: true}
	for _, value := range copyCases {
		report.Passed = report.Passed && value.Passed
	}
	canonical, err := report.canonical()
	if err != nil {
		return FallbackDrillReport{}, err
	}
	digest := sha256.Sum256(canonical)
	report.EvidenceID = "b2-fallback:" + hex.EncodeToString(digest[:])
	return report, nil
}

func (report *FallbackDrillReport) Sign(privateKey ed25519.PrivateKey) error {
	if len(privateKey) != ed25519.PrivateKeySize || report == nil || report.EvidenceID == "" {
		return errors.New("invalid fallback signing key or report")
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)
	keyDigest := sha256.Sum256(publicKey)
	report.SignerKeyID = "ed25519:" + hex.EncodeToString(keyDigest[:8])
	canonical, err := report.canonical()
	if err != nil {
		return err
	}
	report.Signature = base64.RawStdEncoding.EncodeToString(ed25519.Sign(privateKey, canonical))
	return nil
}

func (report FallbackDrillReport) Verify(publicKey ed25519.PublicKey) error {
	if len(publicKey) != ed25519.PublicKeySize || report.Schema != FallbackDrillSchema || !report.Passed || len(report.Cases) != len(RequiredFallbackScenarios) {
		return errors.New("fallback report is incomplete")
	}
	signature, err := base64.RawStdEncoding.DecodeString(report.Signature)
	if err != nil {
		return fmt.Errorf("decode fallback signature: %w", err)
	}
	canonical, err := report.canonical()
	if err != nil {
		return err
	}
	if !ed25519.Verify(publicKey, canonical, signature) {
		return errors.New("fallback signature verification failed")
	}
	unsigned := report
	unsigned.EvidenceID, unsigned.SignerKeyID, unsigned.Signature = "", "", ""
	base, err := unsigned.canonical()
	if err != nil {
		return err
	}
	digest := sha256.Sum256(base)
	if report.EvidenceID != "b2-fallback:"+hex.EncodeToString(digest[:]) {
		return errors.New("fallback evidence identifier mismatch")
	}
	return nil
}

func (report FallbackDrillReport) canonical() ([]byte, error) {
	report.Signature = ""
	return json.Marshal(report)
}
