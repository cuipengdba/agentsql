package businessdb

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	closedparser "github.com/cuipengdba/agentsql/internal/parser"
)

// ClosedRequestDisposition is established before the request transaction.
// ClosedProven means both the raw closed grammar and the candidate catalog
// shape are supported. SyntaxOnlyClosed still requires the catalog caller to
// downgrade views and other ineligible relation shapes to NativeRequired.
type ClosedRequestDisposition string

const (
	ClosedRequestProven         ClosedRequestDisposition = "closed_proven"
	ClosedRequestNativeRequired ClosedRequestDisposition = "native_required"
	ClosedRequestMustReject     ClosedRequestDisposition = "must_reject"
)

const (
	ModeSelectionClosedDefault      = "closed_subset_default"
	ModeSelectionNativeRequired     = "native_required_capability_healthy"
	ModeSelectionNativeAbsent       = "native_unavailable_fallback_closed"
	ModeSelectionNativeMismatch     = "native_attestation_mismatch_fallback_closed"
	ModeSelectionNativeUnhealthy    = "native_unhealthy_fallback_closed"
	ModeSelectionManagedService     = "managed_service_fallback_closed"
	ModeSelectionStatementRejected  = "statement_fail_closed"
	BinderNativeHealthHealthy       = "healthy"
	BinderNativeHealthDivergent     = "divergence"
	BinderNativeHealthUninitialized = "uninitialized"
)

type BinderSelectionRequest struct {
	DatasourceIdentity string
	RequestDigest      string
	StatementClass     BinderStatementClass
	ClosedDisposition  ClosedRequestDisposition
	RequiresMatview    bool
	ClosedShapeID      string
	Provider           string
	Handshake          BinderCapabilityHandshake
}

// BinderSelectionDecision is safe to audit: it contains no SQL or literal.
type BinderSelectionDecision struct {
	Mode                  BinderMode
	Reason                string
	DatasourceIdentity    string
	RequestDigest         string
	StatementClass        BinderStatementClass
	ClosedDisposition     ClosedRequestDisposition
	ClosedShapeID         string
	CapabilityDigest      string
	NativeCapabilityState string
	Rejected              bool
}

type NativeHealthState struct {
	DatasourceIdentity string
	CapabilityDigest   string
	Healthy            bool
	Reason             string
	Generation         uint64
	ChangedAt          time.Time
}

// NativeHealthRegistry is process-local safety state intended to be mirrored
// to S9 metrics/control storage later. A repeated probe with the same digest
// never heals a divergence. Only a newly attested artifact digest can do so.
type NativeHealthRegistry struct {
	mu     sync.RWMutex
	states map[string]NativeHealthState
	now    func() time.Time
}

func NewNativeHealthRegistry() *NativeHealthRegistry {
	return &NativeHealthRegistry{states: make(map[string]NativeHealthState), now: time.Now}
}

func (registry *NativeHealthRegistry) ObserveProbe(datasource string, handshake BinderCapabilityHandshake) NativeHealthState {
	if registry == nil || strings.TrimSpace(datasource) == "" {
		return NativeHealthState{DatasourceIdentity: datasource, Reason: BinderNativeHealthUninitialized}
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	previous, exists := registry.states[datasource]
	digest := handshake.Native.Digest
	valid := nativeHandshakeUsable(handshake)
	if exists && !previous.Healthy && previous.Reason == BinderNativeHealthDivergent && previous.CapabilityDigest == digest {
		return previous
	}
	if exists && previous.CapabilityDigest == digest && previous.Healthy == valid {
		return previous
	}
	reason := handshake.NativeHealth
	if valid {
		reason = BinderNativeHealthHealthy
	} else if reason == "" {
		reason = BinderCodeModeRequired
	}
	state := NativeHealthState{DatasourceIdentity: datasource, CapabilityDigest: digest, Healthy: valid,
		Reason: reason, Generation: previous.Generation + 1, ChangedAt: registry.now().UTC()}
	registry.states[datasource] = state
	return state
}

func (registry *NativeHealthRegistry) MarkDivergent(datasource, capabilityDigest string) (NativeHealthState, bool) {
	if registry == nil || strings.TrimSpace(datasource) == "" {
		return NativeHealthState{DatasourceIdentity: datasource, CapabilityDigest: capabilityDigest,
			Reason: BinderNativeHealthDivergent}, false
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	previous := registry.states[datasource]
	if !previous.Healthy && previous.Reason == BinderNativeHealthDivergent && previous.CapabilityDigest == capabilityDigest {
		return previous, false
	}
	state := NativeHealthState{DatasourceIdentity: datasource, CapabilityDigest: capabilityDigest, Healthy: false,
		Reason: BinderNativeHealthDivergent, Generation: previous.Generation + 1, ChangedAt: registry.now().UTC()}
	registry.states[datasource] = state
	return state, true
}

func (registry *NativeHealthRegistry) State(datasource string) NativeHealthState {
	if registry == nil {
		return NativeHealthState{DatasourceIdentity: datasource, Reason: BinderNativeHealthUninitialized}
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	state, ok := registry.states[datasource]
	if !ok {
		return NativeHealthState{DatasourceIdentity: datasource, Reason: BinderNativeHealthUninitialized}
	}
	return state
}

type BinderModeSelector struct{ health *NativeHealthRegistry }

func NewBinderModeSelector(health *NativeHealthRegistry) *BinderModeSelector {
	if health == nil {
		health = NewNativeHealthRegistry()
	}
	return &BinderModeSelector{health: health}
}

// Select chooses exactly once, before the request transaction. An unavailable
// native accelerator is not an error: the request is routed to closed mode,
// which may then return MODE_REQUIRED for a shape it cannot prove.
func (selector *BinderModeSelector) Select(request BinderSelectionRequest) (BinderSelectionDecision, error) {
	decision := BinderSelectionDecision{Mode: BinderModeCatalogClosedV1, DatasourceIdentity: request.DatasourceIdentity,
		RequestDigest: request.RequestDigest, StatementClass: request.StatementClass,
		ClosedDisposition: request.ClosedDisposition, ClosedShapeID: request.ClosedShapeID,
		CapabilityDigest: request.Handshake.Closed.Digest}
	if selector == nil || selector.health == nil || strings.TrimSpace(request.DatasourceIdentity) == "" ||
		!closedHandshakeUsable(request.Handshake) {
		return decision, NewCapabilityFailure("AUTH_BINDER_CAPABILITY_MISMATCH")
	}
	state := selector.health.ObserveProbe(request.DatasourceIdentity, request.Handshake)
	decision.NativeCapabilityState = state.Reason
	if request.RequiresMatview {
		request.ClosedDisposition = ClosedRequestNativeRequired
		decision.ClosedDisposition = ClosedRequestNativeRequired
	}
	switch request.ClosedDisposition {
	case ClosedRequestMustReject:
		decision.Reason, decision.Rejected = ModeSelectionStatementRejected, true
		return decision, NewCapabilityFailure(BinderCodeModeUnsupported)
	case ClosedRequestProven:
		decision.Reason = ModeSelectionClosedDefault
		return decision, nil
	case ClosedRequestNativeRequired:
	default:
		return decision, NewCapabilityFailure(BinderCodeModeUnsupported)
	}
	if isManagedPostgresProvider(request.Provider) {
		decision.Reason = ModeSelectionManagedService
		if request.RequiresMatview {
			decision.Rejected = true
			return decision, NewPrecisionFailure("AUTH_RELATION_SHAPE_UNSUPPORTED")
		}
		return decision, nil
	}
	if state.Healthy && nativeHandshakeUsable(request.Handshake) {
		decision.Mode, decision.Reason = BinderModeNativeCV1, ModeSelectionNativeRequired
		decision.CapabilityDigest = request.Handshake.Native.Digest
		return decision, nil
	}
	decision.Reason = ModeSelectionNativeAbsent
	if state.Reason == BinderNativeHealthDivergent {
		decision.Reason = ModeSelectionNativeUnhealthy
	} else if request.Handshake.NativeFilesAvailable && request.Handshake.NativeInstalled {
		decision.Reason = ModeSelectionNativeMismatch
	}
	if request.RequiresMatview {
		decision.Rejected = true
		return decision, NewPrecisionFailure("AUTH_RELATION_SHAPE_UNSUPPORTED")
	}
	return decision, nil
}

func closedHandshakeUsable(handshake BinderCapabilityHandshake) bool {
	closed := handshake.Closed
	if closed.Mode != BinderModeCatalogClosedV1 || !closed.Available || closed.Digest == "" {
		return false
	}
	digest, err := closed.CanonicalDigest()
	return err == nil && digest == closed.Digest
}

func nativeHandshakeUsable(handshake BinderCapabilityHandshake) bool {
	native := handshake.Native
	if !handshake.NativeFilesAvailable || !handshake.NativeInstalled || handshake.NativeHealth != BinderNativeHealthHealthy ||
		native.Mode != BinderModeNativeCV1 || !native.Available || native.Digest == "" {
		return false
	}
	expected, ok := PostgresBinderNativeExpectation(native.ServerMajor)
	if !ok || native.ABI != expected.ABI || native.ExtensionVersion != expected.ExtensionVersion ||
		native.BuildHash != expected.BuildHash || native.ExtensionHash != expected.ExtensionHash ||
		native.NodeManifestHash != expected.NodeManifestHash || native.AllowlistHash != expected.AllowlistHash {
		return false
	}
	digest, err := native.CanonicalDigest()
	return err == nil && digest == native.Digest
}

func isManagedPostgresProvider(provider string) bool {
	switch provider {
	case postgresProviderAWSManaged, postgresProviderAzure, postgresProviderCloudSQL:
		return true
	default:
		return false
	}
}

// ClassifyEasyDeployClosedSyntax performs only the raw, connection-free part
// of preclassification. A caller must still check catalog relation shape and
// implicit objects before upgrading the result to ClosedRequestProven.
func ClassifyEasyDeployClosedSyntax(rawSQL string) (BinderStatementClass, ClosedRequestDisposition, string) {
	if parsed, err := closedparser.ParsePostgresClosedSelect(rawSQL); err == nil {
		return BinderStatementSelect, ClosedRequestProven, parsed.ASTDigest
	} else if kind, ok := closedparser.PostgresClosedErrorClass(err); ok && kind == closedparser.PostgresClosedModeRequired {
		return BinderStatementSelect, ClosedRequestNativeRequired, ""
	}
	if parsed, err := closedparser.ParsePostgresClosedDML(rawSQL); err == nil {
		class := BinderStatementClass(parsed.Action)
		return class, ClosedRequestProven, parsed.ASTDigest
	} else if kind, ok := closedparser.PostgresClosedErrorClass(err); ok && kind == closedparser.PostgresClosedModeRequired {
		return inferDMLStatementClass(rawSQL), ClosedRequestNativeRequired, ""
	}
	return "", ClosedRequestMustReject, ""
}

func inferDMLStatementClass(rawSQL string) BinderStatementClass {
	trimmed := strings.TrimSpace(strings.ToUpper(rawSQL))
	for _, value := range []BinderStatementClass{BinderStatementInsert, BinderStatementUpdate, BinderStatementDelete} {
		if strings.HasPrefix(trimmed, string(value)+" ") || strings.HasPrefix(trimmed, "WITH ") {
			return value
		}
	}
	return ""
}

func EasyDeployRequestDigest(rawSQL string) string {
	digest := sha256.Sum256([]byte(rawSQL))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func isBinderDivergence(err error) bool {
	var binderErr *BinderError
	return errors.As(err, &binderErr) && binderErr.Class == BinderFailureDivergence
}
