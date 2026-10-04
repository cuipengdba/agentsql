package b5dml

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sort"
	"strconv"

	"github.com/cuipengdba/agentsql/internal/b5"
)

type GrantEffect = b5.GrantEffect

const (
	GrantEffectUnknown = b5.GrantEffectUnknown
	GrantAllow         = b5.GrantAllow
	GrantDeny          = b5.GrantDeny
)

type GrantElement = b5.GrantElement

const (
	GrantElementUnknown = b5.GrantElementUnknown
	GrantAction         = b5.GrantElementAction
	GrantWriteTarget    = b5.GrantElementWriteTarget
	GrantReference      = b5.GrantElementReference
)

// Grant is an S5a in-memory contract, not the persisted S1b schema. Every
// selector is exact and catalog-bound. Empty names and wildcard OIDs are not
// representable.
type Grant struct {
	Element       GrantElement
	Action        Action
	Relation      RelationIdentity
	WriteKind     WriteTargetKind
	Column        ColumnIdentity
	ReferenceKind ReferenceKind
}

type Policy struct {
	ID           string
	Revision     uint64
	PrincipalID  string
	DatasourceID string
	Effect       GrantEffect
	Grants       []Grant
}

type AuthorizationReason string

const (
	ReasonAllow                    AuthorizationReason = "ALLOW"
	ReasonPreliminaryDenied        AuthorizationReason = "DML_PRELIMINARY_DENIED"
	ReasonDialectUnsupported       AuthorizationReason = "DML_DIALECT_UNSUPPORTED"
	ReasonReservedTarget           AuthorizationReason = "DML_RESERVED_TARGET_DENIED"
	ReasonActionDenied             AuthorizationReason = "DML_ACTION_DENIED"
	ReasonActionGrantMissing       AuthorizationReason = "DML_ACTION_GRANT_MISSING"
	ReasonIdentityUnproven         AuthorizationReason = "DML_IDENTITY_UNPROVEN"
	ReasonClosureUnproven          AuthorizationReason = "DML_CLOSURE_UNPROVEN"
	ReasonWriteTargetDenied        AuthorizationReason = "DML_WRITE_TARGET_DENIED"
	ReasonWriteTargetGrantMissing  AuthorizationReason = "DML_WRITE_TARGET_GRANT_MISSING"
	ReasonReferenceFormUnsupported AuthorizationReason = "DML_REFERENCE_FORM_UNSUPPORTED"
	ReasonReferenceDenied          AuthorizationReason = "DML_REFERENCE_DENIED"
	ReasonReferenceGrantMissing    AuthorizationReason = "DML_REFERENCE_GRANT_MISSING"
)

type AuthorizationInput struct {
	PrincipalID           string
	DatasourceID          string
	Dialect               Dialect
	CurrentServerMajor    int
	Action                Action
	Target                RelationIdentity
	Writes                []WriteTarget
	References            []Reference
	Policies              []Policy
	PreliminaryAllowed    bool
	DatasourceSupported   bool
	ReservedTarget        bool
	CatalogConsistent     bool
	ClosureProven         bool
	PolicySnapshotDigest  string
	CatalogSnapshotDigest string
	ClosureDigest         string
	PlanDigest            string
	Attestations          []BinderAttestation
}

type AuthorizationDecision struct {
	Schema        string
	SchemaVersion uint16
	allowed       bool
	reason        AuthorizationReason
	digest        [32]byte
}

func (decision AuthorizationDecision) Allowed() bool               { return decision.allowed }
func (decision AuthorizationDecision) Reason() AuthorizationReason { return decision.reason }
func (decision AuthorizationDecision) Digest() string              { return hex.EncodeToString(decision.digest[:]) }

func (decision AuthorizationDecision) Verify(input AuthorizationInput) bool {
	other := Authorize(input)
	return decision.allowed == other.allowed && decision.reason == other.reason &&
		decision.digest == other.digest
}

// Authorize implements an independent DML lattice. The order is compatible
// with B2's fail-closed segments, but DML has no output grant and no mask meet:
// preliminary deny -> unsupported -> reserved target -> action deny/missing ->
// identity/closure -> write deny/missing -> reference-form/deny/missing -> allow.
// Within an element, deny is absorbing; allows from multiple policies union.
func Authorize(input AuthorizationInput) AuthorizationDecision {
	decide := func(reason AuthorizationReason) AuthorizationDecision {
		decision := AuthorizationDecision{Schema: b5.DMLGrantProofSchemaID, SchemaVersion: b5.DMLGrantProofSchemaVersion, allowed: reason == ReasonAllow, reason: reason}
		decision.digest = digestAuthorization(input, reason)
		return decision
	}
	if !input.PreliminaryAllowed || input.PrincipalID == "" || !validAction(input.Action) {
		return decide(ReasonPreliminaryDenied)
	}
	if input.Dialect != DialectPostgreSQL || !input.DatasourceSupported {
		return decide(ReasonDialectUnsupported)
	}
	if input.ReservedTarget {
		return decide(ReasonReservedTarget)
	}
	action := Grant{Element: GrantAction, Action: input.Action, Relation: input.Target}
	if hasEffect(input, action, GrantDeny) {
		return decide(ReasonActionDenied)
	}
	if !hasEffect(input, action, GrantAllow) {
		return decide(ReasonActionGrantMissing)
	}
	if !identityComplete(input) {
		return decide(ReasonIdentityUnproven)
	}
	if !input.ClosureProven || input.ClosureDigest == "" {
		return decide(ReasonClosureUnproven)
	}
	for _, target := range uniqueWrites(input.Writes) {
		grant := grantForWrite(input.Action, target)
		if hasEffect(input, grant, GrantDeny) {
			return decide(ReasonWriteTargetDenied)
		}
	}
	for _, target := range uniqueWrites(input.Writes) {
		if !hasEffect(input, grantForWrite(input.Action, target), GrantAllow) {
			return decide(ReasonWriteTargetGrantMissing)
		}
	}
	for _, reference := range uniqueReferences(input.References) {
		if !supportedReference(reference) {
			return decide(ReasonReferenceFormUnsupported)
		}
	}
	for _, reference := range uniqueReferences(input.References) {
		grant := grantForReference(input.Action, reference)
		if hasEffect(input, grant, GrantDeny) {
			return decide(ReasonReferenceDenied)
		}
	}
	for _, reference := range uniqueReferences(input.References) {
		if !hasEffect(input, grantForReference(input.Action, reference), GrantAllow) {
			return decide(ReasonReferenceGrantMissing)
		}
	}
	return decide(ReasonAllow)
}

func identityComplete(input AuthorizationInput) bool {
	if input.DatasourceID == "" || !input.Target.complete() ||
		input.Target.DatasourceID != input.DatasourceID || !input.CatalogConsistent ||
		input.PolicySnapshotDigest == "" || input.CatalogSnapshotDigest == "" ||
		input.PlanDigest == "" || !completeAttestationSet(input.Attestations, input.CurrentServerMajor) {
		return false
	}
	for _, target := range input.Writes {
		switch target.Kind {
		case WriteTargetColumn:
			if !target.Column.completeUserColumn() || target.Relation != target.Column.Relation || target.Relation != input.Target {
				return false
			}
		case WriteTargetRow:
			if input.Action != ActionDelete || target.Relation != input.Target {
				return false
			}
		default:
			return false
		}
	}
	if input.Action == ActionDelete && (len(uniqueWrites(input.Writes)) != 1 || input.Writes[0].Kind != WriteTargetRow) {
		return false
	}
	for _, reference := range input.References {
		if !reference.Relation.complete() || reference.Relation.DatasourceID != input.DatasourceID {
			return false
		}
	}
	for _, policy := range input.Policies {
		if !policyApplies(policy, input) {
			continue
		}
		if policy.ID == "" || policy.Revision == 0 ||
			(policy.Effect != GrantAllow && policy.Effect != GrantDeny) {
			return false
		}
		for _, grant := range policy.Grants {
			if !validGrant(grant) {
				return false
			}
		}
	}
	return true
}

func completeAttestationSet(values []BinderAttestation, current int) bool {
	if len(values) == 1 {
		value := values[0]
		return value.Mode == BinderAttestationCatalogClosed && value.ServerMajor == current && value.complete()
	}
	if len(values) != 5 || current < 14 || current > 18 {
		return false
	}
	seen := [19]bool{}
	for _, value := range values {
		if value.Mode != BinderAttestationNative || !value.complete() || seen[value.ServerMajor] {
			return false
		}
		seen[value.ServerMajor] = true
	}
	for major := 14; major <= 18; major++ {
		if !seen[major] {
			return false
		}
	}
	return seen[current]
}

func validGrant(grant Grant) bool {
	if !validAction(grant.Action) || !grant.Relation.complete() {
		return false
	}
	switch grant.Element {
	case GrantAction:
		return grant.WriteKind == WriteTargetUnknown && grant.ReferenceKind == ReferenceUnknown && grant.Column == (ColumnIdentity{})
	case GrantWriteTarget:
		if grant.ReferenceKind != ReferenceUnknown {
			return false
		}
		if grant.WriteKind == WriteTargetRow {
			return grant.Action == ActionDelete && grant.Column == (ColumnIdentity{})
		}
		return grant.WriteKind == WriteTargetColumn && grant.Column.completeUserColumn() && grant.Column.Relation == grant.Relation
	case GrantReference:
		return grant.WriteKind == WriteTargetUnknown && grant.ReferenceKind == ReferenceColumn &&
			grant.Column.completeUserColumn() && grant.Column.Relation == grant.Relation
	default:
		return false
	}
}

func policyApplies(policy Policy, input AuthorizationInput) bool {
	return (policy.PrincipalID == input.PrincipalID || policy.PrincipalID == "*") &&
		(policy.DatasourceID == input.DatasourceID || policy.DatasourceID == "*")
}

func hasEffect(input AuthorizationInput, required Grant, effect GrantEffect) bool {
	for _, policy := range input.Policies {
		if !policyApplies(policy, input) || policy.Effect != effect {
			continue
		}
		for _, grant := range policy.Grants {
			if grantsEqual(grant, required) {
				return true
			}
		}
	}
	return false
}

func grantsEqual(left, right Grant) bool {
	return left.Element == right.Element && left.Action == right.Action &&
		left.Relation == right.Relation && left.WriteKind == right.WriteKind &&
		left.Column == right.Column && left.ReferenceKind == right.ReferenceKind
}

func grantForWrite(action Action, target WriteTarget) Grant {
	grant := Grant{Element: GrantWriteTarget, Action: action, Relation: target.Relation, WriteKind: target.Kind}
	if target.Kind == WriteTargetColumn {
		grant.Column = target.Column
	}
	return grant
}

func grantForReference(action Action, reference Reference) Grant {
	return Grant{Element: GrantReference, Action: action, Relation: reference.Relation,
		Column: reference.Column, ReferenceKind: reference.Kind}
}

func uniqueWrites(values []WriteTarget) []WriteTarget {
	byKey := make(map[string]WriteTarget, len(values))
	for _, value := range values {
		byKey[value.key()] = value
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]WriteTarget, 0, len(keys))
	for _, key := range keys {
		result = append(result, byKey[key])
	}
	return result
}

func uniqueReferences(values []Reference) []Reference {
	byKey := make(map[string]Reference, len(values))
	for _, value := range values {
		byKey[value.key()] = value
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]Reference, 0, len(keys))
	for _, key := range keys {
		result = append(result, byKey[key])
	}
	return result
}

func digestAuthorization(input AuthorizationInput, reason AuthorizationReason) [32]byte {
	hash := sha256.New()
	writeDigestString(hash, AuthorizationContractSchema)
	writeDigestString(hash, input.PrincipalID)
	writeDigestString(hash, input.DatasourceID)
	writeDigestString(hash, strconv.Itoa(int(input.Dialect)))
	writeDigestString(hash, strconv.Itoa(input.CurrentServerMajor))
	writeDigestString(hash, input.Action.String())
	writeDigestString(hash, input.Target.key())
	writeDigestString(hash, strconv.FormatBool(input.PreliminaryAllowed))
	writeDigestString(hash, strconv.FormatBool(input.DatasourceSupported))
	writeDigestString(hash, strconv.FormatBool(input.ReservedTarget))
	writeDigestString(hash, strconv.FormatBool(input.CatalogConsistent))
	writeDigestString(hash, strconv.FormatBool(input.ClosureProven))
	writeDigestString(hash, input.PolicySnapshotDigest)
	writeDigestString(hash, input.CatalogSnapshotDigest)
	writeDigestString(hash, input.ClosureDigest)
	writeDigestString(hash, input.PlanDigest)

	writes := uniqueWrites(input.Writes)
	for _, target := range writes {
		writeDigestString(hash, target.key())
		writeDigestString(hash, strconv.Itoa(int(target.Source)))
	}
	for _, reference := range uniqueReferences(input.References) {
		writeDigestString(hash, reference.key())
	}
	policies := append([]Policy(nil), input.Policies...)
	sort.Slice(policies, func(i, j int) bool { return policyKey(policies[i]) < policyKey(policies[j]) })
	for _, policy := range policies {
		writeDigestString(hash, policyKey(policy))
	}
	attestations := append([]BinderAttestation(nil), input.Attestations...)
	sort.Slice(attestations, func(i, j int) bool { return attestations[i].ServerMajor < attestations[j].ServerMajor })
	for _, attestation := range attestations {
		writeDigestString(hash, attestation.key())
	}
	writeDigestString(hash, string(reason))
	var result [32]byte
	copy(result[:], hash.Sum(nil))
	return result
}

func policyKey(policy Policy) string {
	grants := append([]Grant(nil), policy.Grants...)
	sort.Slice(grants, func(i, j int) bool { return grantKey(grants[i]) < grantKey(grants[j]) })
	value := policy.ID + "\x00" + strconv.FormatUint(policy.Revision, 10) + "\x00" +
		policy.PrincipalID + "\x00" + policy.DatasourceID + "\x00" + string(policy.Effect)
	for _, grant := range grants {
		value += "\x00" + grantKey(grant)
	}
	return value
}

func grantKey(grant Grant) string {
	return string(grant.Element) + "\x00" + grant.Action.String() + "\x00" +
		grant.Relation.key() + "\x00" + strconv.Itoa(int(grant.WriteKind)) + "\x00" +
		grant.Column.key() + "\x00" + strconv.Itoa(int(grant.ReferenceKind))
}

type digestWriter interface{ Write([]byte) (int, error) }

func writeDigestString(writer digestWriter, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = writer.Write(length[:])
	_, _ = writer.Write([]byte(value))
}
