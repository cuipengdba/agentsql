package columnauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
)

// Reason is a stable, identity-only authorization result.  It is safe to put
// in a public error envelope: none of the values contains a relation or column
// name supplied by the database.
type Reason string

const (
	ReasonAllow                 Reason = "ALLOW"
	ReasonAgentDenied           Reason = "AUTH_AGENT_DENIED"
	ReasonStatementDenied       Reason = "AUTH_STATEMENT_CLASS_DENIED"
	ReasonDatasourceUnsupported Reason = "AUTH_DATASOURCE_UNSUPPORTED"
	ReasonRelationDenied        Reason = "AUTH_RELATION_DENIED"
	ReasonRelationGrantMissing  Reason = "AUTH_RELATION_GRANT_MISSING"
	ReasonIdentityUnproven      Reason = "AUTH_IDENTITY_UNPROVEN"
	ReasonColumnGrantMissing    Reason = "AUTH_COLUMN_GRANT_MISSING"
	ReasonMaskMeetUndefined     Reason = "AUTH_MASK_MEET_UNDEFINED"
	ReasonMaskCapabilityMissing Reason = "AUTH_MASK_CAPABILITY_MISSING"
)

type Usage string

const (
	UsageOutput    Usage = "output"
	UsageReference Usage = "reference"
)

// RelationIdentity and ColumnIdentity are the catalog identities used by the
// authorizer. Names aid exact policy matching, while DatabaseID/StableObjectID
// and ordinal/type digest are the authority-bearing identities.
type RelationIdentity struct {
	DatabaseID         string
	StableObjectID     string
	Schema             string
	Name               string
	CatalogFingerprint string
	BindingAlias       string
	ViewPath           string
}

type ColumnIdentity struct {
	Relation   RelationIdentity
	Ordinal    int
	Name       string
	TypeDigest string
}

// BoundColumnUse is the S3 output/reference fact consumed by S4. OutputIndex
// is zero based and is ignored for reference-only sites.
type BoundColumnUse struct {
	Column       ColumnIdentity
	Usage        Usage
	Site         string
	OutputIndex  int
	BindingAlias string
	ViewPath     string
}

// MaskIdentity deliberately contains only immutable transform identity and
// type/capability facts. It never contains a value, result sample, predicate,
// or other data-derived input.
type MaskIdentity struct {
	AlgorithmID         string
	SemanticVersion     string
	KeyID               string
	KeyVersion          int
	CanonicalParameters string
	InputType           string
	OutputType          string
}

func (identity MaskIdentity) empty() bool { return identity.AlgorithmID == "" }

type MaskCandidate struct {
	OutputIndex int
	Column      ColumnIdentity
	Identity    MaskIdentity
	Capable     bool
}

// Input is an immutable request snapshot assembled from authenticated caller
// state, the S3 locked manifest/catalog frame, and the control snapshot.
type Input struct {
	Agent                 model.Agent
	DatasourceID          string
	Statement             model.StmtType
	PreliminaryAllowed    bool
	DatasourceSupported   bool
	CatalogConsistent     bool
	BinderDigest          string
	CatalogDigest         string
	ControlRevisionDigest string
	Relations             []RelationIdentity
	Uses                  []BoundColumnUse
	Policies              []model.Policy
	Masks                 []MaskCandidate
	Nonce                 []byte
	Now                   time.Time
}

type OutputMask struct {
	OutputIndex int
	Identity    MaskIdentity
}

// ProtectionPlan is request-bound. The proof material remains unexported;
// callers can only verify it against the original request facts and consume
// the already-decided mask positions.
type ProtectionPlan struct {
	allowed bool
	reason  Reason
	masks   []OutputMask
	proof   authorizationProof
}

type authorizationProof struct {
	nonceDigest      [32]byte
	bindingDigest    [32]byte
	sourceFreeDigest [32]byte
	referenceDigest  [32]byte
	decisionDigest   [32]byte
}

func (plan ProtectionPlan) Allowed() bool  { return plan.allowed }
func (plan ProtectionPlan) Reason() Reason { return plan.reason }
func (plan ProtectionPlan) Masks() []OutputMask {
	return append([]OutputMask(nil), plan.masks...)
}
func (plan ProtectionPlan) Digest() string { return hex.EncodeToString(plan.proof.decisionDigest[:]) }

// Verify proves that a plan belongs to the same request nonce and immutable
// binding/control/catalog facts. It is intentionally impossible to validate a
// copied plan with only its public digest.
func (plan ProtectionPlan) Verify(input Input) bool {
	proof := makeProof(input, plan.reason, plan.masks)
	return hmac.Equal(plan.proof.nonceDigest[:], proof.nonceDigest[:]) &&
		hmac.Equal(plan.proof.bindingDigest[:], proof.bindingDigest[:]) &&
		hmac.Equal(plan.proof.sourceFreeDigest[:], proof.sourceFreeDigest[:]) &&
		hmac.Equal(plan.proof.referenceDigest[:], proof.referenceDigest[:]) &&
		hmac.Equal(plan.proof.decisionDigest[:], proof.decisionDigest[:])
}

// Authorize applies the frozen priority order. Every failure is represented
// by a stable enum and never includes an unauthorized identifier.
func Authorize(input Input) ProtectionPlan {
	deny := func(reason Reason) ProtectionPlan {
		plan := ProtectionPlan{reason: reason}
		plan.proof = makeProof(input, reason, nil)
		return plan
	}
	now := input.Now
	if now.IsZero() {
		now = time.Now()
	}
	if !input.PreliminaryAllowed || input.Agent.Status != "active" ||
		(input.Agent.ExpiresAt != nil && !input.Agent.ExpiresAt.After(now)) ||
		(input.Agent.Level != "readonly" && input.Agent.Level != "dml" && input.Agent.Level != "ddl") {
		return deny(ReasonAgentDenied)
	}
	if input.Statement != model.StmtType("SELECT") {
		return deny(ReasonStatementDenied)
	}
	if !input.DatasourceSupported {
		return deny(ReasonDatasourceUnsupported)
	}

	for _, relation := range input.Relations {
		for _, policy := range input.Policies {
			if policyApplies(policy, input) && policy.Action == "deny" && policy.ObjectType != "column" && relationPolicyMatches(policy, relation) {
				return deny(ReasonRelationDenied)
			}
		}
	}
	for _, relation := range input.Relations {
		allowed := false
		for _, policy := range input.Policies {
			if policyApplies(policy, input) && policy.Action == "allow" && relationPolicyMatches(policy, relation) {
				allowed = true
				break
			}
		}
		if !allowed {
			return deny(ReasonRelationGrantMissing)
		}
	}

	if !input.CatalogConsistent || input.BinderDigest == "" || input.CatalogDigest == "" ||
		input.ControlRevisionDigest == "" || len(input.Nonce) < 16 || !identitiesComplete(input) || !bindingsConsistent(input) {
		return deny(ReasonIdentityUnproven)
	}

	for _, use := range input.Uses {
		if !hasColumnGrant(use, input.Policies, input) {
			return deny(ReasonColumnGrantMissing)
		}
	}

	masks, reason := meetMasks(input.Masks)
	if reason != ReasonAllow {
		return deny(reason)
	}
	plan := ProtectionPlan{allowed: true, reason: ReasonAllow, masks: masks}
	plan.proof = makeProof(input, plan.reason, masks)
	return plan
}

func bindingsConsistent(input Input) bool {
	for _, policy := range input.Policies {
		if !policyApplies(policy, input) {
			continue
		}
		var relation *RelationIdentity
		for index := range input.Relations {
			if relationPolicyMatches(policy, input.Relations[index]) {
				relation = &input.Relations[index]
				break
			}
		}
		if relation == nil {
			continue
		}
		requiresBinding := policy.ObjectType == "column" || policy.RelationBinding != nil ||
			len(policy.ColumnPermissions) != 0 || len(policy.ColumnStaging) != 0
		if !requiresBinding {
			continue
		}
		if policy.Action != "allow" {
			return false
		}
		binding := policy.RelationBinding
		if binding == nil || policy.RelationBindingID == nil || *policy.RelationBindingID != binding.ID ||
			binding.PolicyID != policy.ID || binding.DatasourceID != policy.DatasourceID ||
			len(policy.ColumnStaging) != 0 || binding.Status != "healthy" ||
			binding.StableObjectID == nil || binding.CatalogFingerprint == nil ||
			*binding.StableObjectID != relation.StableObjectID || *binding.CatalogFingerprint != relation.CatalogFingerprint ||
			binding.SchemaName != relation.Schema || binding.RelationName != relation.Name {
			return false
		}
		for _, permission := range policy.ColumnPermissions {
			if permission.RelationEnrollmentID != binding.ID || permission.ParentRevision != policy.Revision ||
				(permission.Usage != string(UsageOutput) && permission.Usage != string(UsageReference)) {
				return false
			}
		}
	}
	return true
}

func relationPolicyMatches(policy model.Policy, relation RelationIdentity) bool {
	pattern := policy.ObjectName
	if pattern == "*" {
		return true
	}
	if relation.Schema != "" && pattern == relation.Schema+".*" {
		return true
	}
	if pattern == relation.Name || pattern == relation.Schema+"."+relation.Name {
		return true
	}
	if binding := policy.RelationBinding; binding != nil {
		return binding.DatasourceID == policy.DatasourceID &&
			binding.SchemaName == relation.Schema && binding.RelationName == relation.Name
	}
	return false
}

func policyApplies(policy model.Policy, input Input) bool {
	return (policy.AgentID == "" || policy.AgentID == input.Agent.ID) &&
		(policy.DatasourceID == "" || policy.DatasourceID == input.DatasourceID)
}

func identitiesComplete(input Input) bool {
	relations := make(map[string]RelationIdentity, len(input.Relations))
	for _, relation := range input.Relations {
		if relation.DatabaseID == "" || relation.StableObjectID == "" || relation.Name == "" || relation.CatalogFingerprint == "" {
			return false
		}
		relations[relation.StableObjectID] = relation
	}
	for _, use := range input.Uses {
		column := use.Column
		if use.Usage != UsageOutput && use.Usage != UsageReference || column.Ordinal <= 0 ||
			column.Name == "" || column.TypeDigest == "" || column.Relation.StableObjectID == "" {
			return false
		}
		relation, ok := relations[column.Relation.StableObjectID]
		if !ok || relation.DatabaseID != column.Relation.DatabaseID || relation.CatalogFingerprint != column.Relation.CatalogFingerprint {
			return false
		}
	}
	return true
}

func hasColumnGrant(use BoundColumnUse, policies []model.Policy, input Input) bool {
	for _, policy := range policies {
		if !policyApplies(policy, input) {
			continue
		}
		if policy.Action != "allow" || !relationPolicyMatches(policy, use.Column.Relation) {
			continue
		}
		binding := policy.RelationBinding
		if binding == nil || policy.RelationBindingID == nil || *policy.RelationBindingID != binding.ID ||
			binding.PolicyID != policy.ID || binding.DatasourceID != policy.DatasourceID ||
			binding.Status != "healthy" || binding.StableObjectID == nil || binding.CatalogFingerprint == nil ||
			*binding.StableObjectID != use.Column.Relation.StableObjectID ||
			*binding.CatalogFingerprint != use.Column.Relation.CatalogFingerprint {
			continue
		}
		for _, grant := range policy.ColumnPermissions {
			if grant.PolicyID != "" && grant.PolicyID != policy.ID {
				continue
			}
			if grant.RelationEnrollmentID == binding.ID && grant.ColumnOrdinal == use.Column.Ordinal &&
				grant.ColumnName == use.Column.Name && grant.ColumnTypeDigest == use.Column.TypeDigest &&
				grant.Usage == string(use.Usage) && grant.ParentRevision > 0 {
				return true
			}
		}
	}
	return false
}

func meetMasks(candidates []MaskCandidate) ([]OutputMask, Reason) {
	byPosition := make(map[int][]MaskCandidate)
	for _, candidate := range candidates {
		if candidate.OutputIndex < 0 {
			return nil, ReasonMaskMeetUndefined
		}
		byPosition[candidate.OutputIndex] = append(byPosition[candidate.OutputIndex], candidate)
	}
	positions := make([]int, 0, len(byPosition))
	for position := range byPosition {
		positions = append(positions, position)
	}
	sort.Ints(positions)
	result := make([]OutputMask, 0, len(positions))
	for _, position := range positions {
		var selected MaskIdentity
		for _, candidate := range byPosition[position] {
			if candidate.Identity.empty() {
				continue
			}
			if !candidate.Capable {
				return nil, ReasonMaskCapabilityMissing
			}
			if candidate.Identity.InputType != candidate.Column.TypeDigest {
				return nil, ReasonMaskMeetUndefined
			}
			if selected.empty() {
				selected = candidate.Identity
				continue
			}
			if selected != candidate.Identity {
				return nil, ReasonMaskMeetUndefined
			}
		}
		if !selected.empty() {
			result = append(result, OutputMask{OutputIndex: position, Identity: selected})
		}
	}
	return result, ReasonAllow
}

func makeProof(input Input, reason Reason, masks []OutputMask) authorizationProof {
	proof := authorizationProof{nonceDigest: sha256.Sum256(input.Nonce)}
	proof.bindingDigest = digestInput(input, false, true)
	proof.sourceFreeDigest = digestUses(input.Uses, UsageOutput)
	proof.referenceDigest = digestUses(input.Uses, UsageReference)
	h := sha256.New()
	h.Write(proof.nonceDigest[:])
	h.Write(proof.bindingDigest[:])
	h.Write(proof.sourceFreeDigest[:])
	h.Write(proof.referenceDigest[:])
	writeString(h, string(reason))
	for _, item := range masks {
		writeInt(h, item.OutputIndex)
		writeString(h, maskIdentityKey(item.Identity))
	}
	copy(proof.decisionDigest[:], h.Sum(nil))
	return proof
}

func digestInput(input Input, includeUses, includeMasks bool) [32]byte {
	h := sha256.New()
	writeString(h, input.Agent.ID)
	writeString(h, input.Agent.Status)
	writeString(h, input.Agent.Level)
	if input.Agent.ExpiresAt != nil {
		writeString(h, input.Agent.ExpiresAt.UTC().Format(time.RFC3339Nano))
	}
	writeString(h, input.Now.UTC().Format(time.RFC3339Nano))
	writeString(h, input.DatasourceID)
	writeString(h, string(input.Statement))
	writeString(h, strconv.FormatBool(input.PreliminaryAllowed))
	writeString(h, strconv.FormatBool(input.DatasourceSupported))
	writeString(h, strconv.FormatBool(input.CatalogConsistent))
	writeString(h, input.BinderDigest)
	writeString(h, input.CatalogDigest)
	writeString(h, input.ControlRevisionDigest)
	relations := append([]RelationIdentity(nil), input.Relations...)
	sort.Slice(relations, func(i, j int) bool { return relationKey(relations[i]) < relationKey(relations[j]) })
	for _, relation := range relations {
		writeString(h, relationKey(relation))
	}
	policies := append([]model.Policy(nil), input.Policies...)
	sort.Slice(policies, func(i, j int) bool { return policyKey(policies[i]) < policyKey(policies[j]) })
	for _, policy := range policies {
		writeString(h, policyKey(policy))
	}
	if includeUses {
		value := digestUses(input.Uses, "")
		h.Write(value[:])
	}
	if includeMasks {
		masks := make([]string, 0, len(input.Masks))
		for _, candidate := range input.Masks {
			masks = append(masks, strconv.Itoa(candidate.OutputIndex)+"\x00"+columnKey(candidate.Column)+"\x00"+
				maskIdentityKey(candidate.Identity)+"\x00"+strconv.FormatBool(candidate.Capable))
		}
		sort.Strings(masks)
		for _, candidate := range masks {
			writeString(h, candidate)
		}
	}
	var result [32]byte
	copy(result[:], h.Sum(nil))
	return result
}

func policyKey(policy model.Policy) string {
	columns, rowFilter, bindingID := "", "", ""
	if policy.Columns != nil {
		columns = *policy.Columns
	}
	if policy.RowFilter != nil {
		rowFilter = *policy.RowFilter
	}
	if policy.RelationBindingID != nil {
		bindingID = *policy.RelationBindingID
	}
	parts := []string{policy.ID, policy.AgentID, policy.DatasourceID, policy.ObjectType, policy.ObjectName,
		columns, rowFilter, bindingID, policy.Action, strconv.FormatInt(policy.Revision, 10), strconv.FormatBool(policy.LegacyUnrepresentable)}
	if binding := policy.RelationBinding; binding != nil {
		stable, catalog := "", ""
		if binding.StableObjectID != nil {
			stable = *binding.StableObjectID
		}
		if binding.CatalogFingerprint != nil {
			catalog = *binding.CatalogFingerprint
		}
		parts = append(parts, binding.ID, binding.PolicyID, binding.DatasourceID, binding.SchemaName,
			binding.RelationName, stable, catalog, binding.Status, strconv.FormatInt(binding.Revision, 10))
	}
	permissions := append([]model.PolicyColumnPermission(nil), policy.ColumnPermissions...)
	sort.Slice(permissions, func(i, j int) bool {
		return permissionKey(permissions[i]) < permissionKey(permissions[j])
	})
	for _, permission := range permissions {
		parts = append(parts, permissionKey(permission))
	}
	for _, staging := range policy.ColumnStaging {
		errorCode := ""
		if staging.ErrorCode != nil {
			errorCode = *staging.ErrorCode
		}
		parts = append(parts, staging.PolicyID, strconv.Itoa(staging.TokenOrdinal), staging.LegacyToken,
			staging.RequestedUsage, staging.SourceCSVHash, staging.BindStatus, errorCode)
	}
	return strings.Join(parts, "\x00")
}

func permissionKey(permission model.PolicyColumnPermission) string {
	return strings.Join([]string{permission.PolicyID, permission.RelationEnrollmentID,
		strconv.Itoa(permission.ColumnOrdinal), permission.ColumnName, permission.ColumnTypeDigest,
		permission.Usage, strconv.FormatInt(permission.ParentRevision, 10)}, "\x00")
}

func columnKey(column ColumnIdentity) string {
	return relationKey(column.Relation) + "\x00" + strconv.Itoa(column.Ordinal) + "\x00" + column.Name + "\x00" + column.TypeDigest
}

func digestUses(uses []BoundColumnUse, usage Usage) [32]byte {
	values := make([]string, 0, len(uses))
	for _, use := range uses {
		if usage != "" && use.Usage != usage {
			continue
		}
		values = append(values, strings.Join([]string{string(use.Usage), relationKey(use.Column.Relation),
			strconv.Itoa(use.Column.Ordinal), use.Column.Name, use.Column.TypeDigest, use.Site,
			strconv.Itoa(use.OutputIndex), use.BindingAlias, use.ViewPath}, "\x00"))
	}
	sort.Strings(values)
	h := sha256.New()
	for _, value := range values {
		writeString(h, value)
	}
	var result [32]byte
	copy(result[:], h.Sum(nil))
	return result
}

func relationKey(value RelationIdentity) string {
	return strings.Join([]string{value.DatabaseID, value.StableObjectID, value.Schema, value.Name,
		value.CatalogFingerprint, value.BindingAlias, value.ViewPath}, "\x00")
}

func maskIdentityKey(value MaskIdentity) string {
	return strings.Join([]string{value.AlgorithmID, value.SemanticVersion, value.KeyID,
		value.CanonicalParameters, value.InputType, value.OutputType, strconv.Itoa(value.KeyVersion)}, "\x00")
}

type hashWriter interface{ Write([]byte) (int, error) }

func writeString(writer hashWriter, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = writer.Write(length[:])
	_, _ = writer.Write([]byte(value))
}

func writeInt(writer hashWriter, value int) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], uint64(value))
	_, _ = writer.Write(encoded[:])
}
