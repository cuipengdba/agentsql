package b2release

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const GateEvidenceSchema = "agentsql.b2.ga-evidence/v1"

type GateMode string

const (
	GateModeDryRun     GateMode = "dry-run"
	GateModeActivation GateMode = "activation"
	GateModeGA         GateMode = "ga"
)

type EvidenceItem struct {
	Complete bool   `json:"complete"`
	Artifact string `json:"artifact,omitempty"`
	Digest   string `json:"digest,omitempty"`
}

type ActivationEvidence struct {
	DryRunObserved       bool   `json:"dry_run_observed"`
	TwoPhaseVerified     bool   `json:"two_phase_verified"`
	StrongETag           string `json:"strong_etag,omitempty"`
	CapabilityProbe      bool   `json:"capability_probe"`
	StagingRows          int64  `json:"staging_rows"`
	WildcardPolicies     int64  `json:"wildcard_policies"`
	UnhealthyEnrollments int64  `json:"unhealthy_enrollments"`
	MySQLEnabled         bool   `json:"mysql_enabled"`
	MySQLWatcherReady    bool   `json:"mysql_watcher_ready"`
	MySQLInspectorReady  bool   `json:"mysql_inspector_ready"`
	MySQLThreatAccepted  bool   `json:"mysql_threat_model_accepted"`
}

type ArtifactEvidence struct {
	Protocol3BundleComplete bool     `json:"protocol3_bundle_complete"`
	Signed                  bool     `json:"signed"`
	SBOM                    bool     `json:"sbom"`
	Provenance              bool     `json:"provenance"`
	Versions                []string `json:"versions,omitempty"`
}

type GateEvidence struct {
	Schema             string             `json:"schema"`
	GeneratedAt        time.Time          `json:"generated_at"`
	Commit             string             `json:"commit"`
	Version            string             `json:"version"`
	ProductionFlagsOff bool               `json:"production_flags_off"`
	ZeroUnauthorized   bool               `json:"zero_unauthorized_references"`
	AttackMatrix       EvidenceItem       `json:"attack_matrix"`
	SLO                EvidenceItem       `json:"slo"`
	Fallback           EvidenceItem       `json:"fallback"`
	Activation         ActivationEvidence `json:"activation"`
	Artifacts          ArtifactEvidence   `json:"artifacts"`
	M2SignedOff        bool               `json:"m2_signed_off"`
	M3SignedOff        bool               `json:"m3_signed_off"`
	VersionBumped      bool               `json:"version_bumped"`
	ReleaseBundleReady bool               `json:"release_bundle_ready"`
}

type GateResult struct {
	Schema      string    `json:"schema"`
	EvaluatedAt time.Time `json:"evaluated_at"`
	Mode        GateMode  `json:"mode"`
	Conclusion  string    `json:"conclusion"`
	Ready       bool      `json:"ready"`
	Mutated     bool      `json:"mutated"`
	Missing     []string  `json:"missing,omitempty"`
	Disclaimer  string    `json:"disclaimer"`
}

type attackMatrixCell struct {
	PostgresMajor int  `json:"postgres_major"`
	Passed        bool `json:"passed"`
}

type sloMatrixCell struct {
	PostgresMajor int                        `json:"postgres_major"`
	Passed        bool                       `json:"passed"`
	Modes         map[string]json.RawMessage `json:"modes"`
}

func LoadEvidence(path string) (GateEvidence, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return GateEvidence{}, err
	}
	var evidence GateEvidence
	if err := json.Unmarshal(contents, &evidence); err != nil {
		return GateEvidence{}, err
	}
	if evidence.Schema != GateEvidenceSchema {
		return GateEvidence{}, fmt.Errorf("unsupported evidence schema %q", evidence.Schema)
	}
	return evidence, nil
}

// VerifyFallbackEvidence cryptographically binds the drill to its report.
// Relative report paths are resolved next to the GA evidence file.
func VerifyFallbackEvidence(evidencePath string, evidence *GateEvidence, trustedPublicKey string) error {
	if evidence == nil || !evidence.Fallback.Complete || evidence.Fallback.Artifact == "" || trustedPublicKey == "" {
		return errors.New("fallback evidence is incomplete")
	}
	publicKey, err := base64.RawStdEncoding.DecodeString(trustedPublicKey)
	if err != nil {
		return fmt.Errorf("decode fallback public key: %w", err)
	}
	path := evidence.Fallback.Artifact
	if !filepath.IsAbs(path) {
		path = filepath.Join(filepath.Dir(evidencePath), path)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var report FallbackDrillReport
	if err := json.Unmarshal(contents, &report); err != nil {
		return err
	}
	if err := report.Verify(ed25519.PublicKey(publicKey)); err != nil {
		return err
	}
	if evidence.Fallback.Digest != report.EvidenceID {
		return errors.New("fallback report digest does not match GA evidence")
	}
	return nil
}

// VerifyReferencedArtifacts refuses checkbox-only evidence. Attack and SLO
// entries must point to parseable, passing reports whose raw file digest is
// the digest recorded in the release manifest.
func VerifyReferencedArtifacts(evidencePath string, evidence *GateEvidence) []error {
	if evidence == nil {
		return []error{errors.New("nil gate evidence")}
	}
	var failures []error
	if evidence.AttackMatrix.Complete {
		var report struct {
			Schema string             `json:"schema"`
			Passed bool               `json:"passed"`
			Cells  []attackMatrixCell `json:"cells"`
		}
		if err := verifyJSONArtifact(evidencePath, evidence.AttackMatrix, &report); err != nil ||
			report.Schema != "agentsql.b2.s7-attack-matrix/v1" || !report.Passed || !hasPassingMajors(report.Cells) {
			evidence.AttackMatrix.Complete = false
			if err == nil {
				err = errors.New("attack matrix report is incomplete")
			}
			failures = append(failures, err)
		}
	}
	if evidence.SLO.Complete {
		var report struct {
			Schema             string          `json:"schema"`
			Passed             bool            `json:"passed"`
			QualifyingEvidence bool            `json:"qualifying_evidence"`
			Cells              []sloMatrixCell `json:"cells"`
		}
		if err := verifyJSONArtifact(evidencePath, evidence.SLO, &report); err != nil || report.Schema != "agentsql.b2.s8-slo/v1" ||
			!report.Passed || !report.QualifyingEvidence || !hasPassingSLOMajors(report.Cells) {
			evidence.SLO.Complete = false
			if err == nil {
				err = errors.New("SLO report is incomplete or non-qualifying")
			}
			failures = append(failures, err)
		}
	}
	return failures
}

func verifyJSONArtifact(evidencePath string, item EvidenceItem, target any) error {
	if item.Artifact == "" || item.Digest == "" {
		return errors.New("artifact path or digest is empty")
	}
	path := item.Artifact
	if !filepath.IsAbs(path) {
		path = filepath.Join(filepath.Dir(evidencePath), path)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(contents)
	if item.Digest != "sha256:"+hex.EncodeToString(digest[:]) {
		return errors.New("artifact digest mismatch")
	}
	return json.Unmarshal(contents, target)
}

func hasPassingMajors(cells []attackMatrixCell) bool {
	passing := make(map[int]bool)
	for _, cell := range cells {
		passing[cell.PostgresMajor] = cell.Passed
	}
	return passing[14] && passing[18]
}

func hasPassingSLOMajors(cells []sloMatrixCell) bool {
	passing := make(map[int]bool)
	for _, cell := range cells {
		passing[cell.PostgresMajor] = cell.Passed && cell.Modes["closed"] != nil && cell.Modes["native"] != nil
	}
	return passing[14] && passing[18]
}

func Evaluate(evidence GateEvidence, mode GateMode, now time.Time) GateResult {
	missing := make([]string, 0)
	require := func(ok bool, name string) {
		if !ok {
			missing = append(missing, name)
		}
	}
	require(evidence.ProductionFlagsOff, "flag-off isolation evidence")
	require(evidence.ZeroUnauthorized, "zero unauthorized references scan")
	require(evidence.AttackMatrix.Complete && evidence.AttackMatrix.Artifact != "" && evidence.AttackMatrix.Digest != "", "S7 attack matrix evidence")
	require(evidence.SLO.Complete && evidence.SLO.Artifact != "" && evidence.SLO.Digest != "", "PG14/18 closed/native >=10m SLO evidence")
	require(evidence.Fallback.Complete && evidence.Fallback.Artifact != "" && evidence.Fallback.Digest != "", "signed fallback drill evidence")
	require(evidence.Activation.DryRunObserved, "dry-run observation")
	require(evidence.Activation.TwoPhaseVerified && evidence.Activation.StrongETag != "", "two-phase strong ETag activation")
	require(evidence.Activation.CapabilityProbe, "activation capability probe")
	require(evidence.Activation.StagingRows == 0, "staging rows = 0")
	require(evidence.Activation.WildcardPolicies == 0, "staging wildcard = 0")
	require(evidence.Activation.UnhealthyEnrollments == 0, "all enrollments healthy")
	if evidence.Activation.MySQLEnabled {
		require(evidence.Activation.MySQLWatcherReady, "MySQL watcher ready")
		require(evidence.Activation.MySQLInspectorReady, "MySQL inspector ready")
		require(evidence.Activation.MySQLThreatAccepted, "MySQL threat model sign-off")
	}
	require(evidence.Artifacts.Protocol3BundleComplete, "same-release protocol-3 safety bundle")
	require(evidence.Artifacts.Signed, "artifact signatures")
	require(evidence.Artifacts.SBOM, "SBOM")
	require(evidence.Artifacts.Provenance, "provenance")
	require(len(evidence.Artifacts.Versions) > 0, "version/artifact inventory")
	if mode == GateModeGA {
		require(evidence.M2SignedOff, "M2 infrastructure owner sign-off")
		require(evidence.M3SignedOff, "M3 SRE owner sign-off")
		require(evidence.VersionBumped, "version bump")
		require(evidence.ReleaseBundleReady, "release engineering bundle")
	}
	sort.Strings(missing)
	result := GateResult{Schema: "agentsql.b2.gate-result/v1", EvaluatedAt: now.UTC(), Mode: mode,
		Ready: len(missing) == 0, Mutated: false, Missing: missing,
		Disclaimer: "This is an engineering gate result, not release-owner approval."}
	switch mode {
	case GateModeDryRun:
		result.Conclusion = "OBSERVE_ONLY"
	case GateModeActivation:
		if result.Ready {
			result.Conclusion = "ACTIVATION_READY"
		} else {
			result.Conclusion = "NO-GO"
		}
	case GateModeGA:
		if result.Ready {
			result.Conclusion = "GO"
		} else {
			result.Conclusion = "NO-GO"
		}
	default:
		result.Ready = false
		result.Conclusion = "NO-GO"
		result.Missing = append(result.Missing, "valid gate mode")
	}
	return result
}
