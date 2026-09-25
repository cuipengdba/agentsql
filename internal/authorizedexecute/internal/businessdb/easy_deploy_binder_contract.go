package businessdb

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/cuipengdba/agentsql/internal/b5dml"
	"github.com/cuipengdba/agentsql/internal/columnauth"
)

const (
	BinderProofSchemaID      = "agentsql.binder-proof"
	BinderProofSchemaVersion = uint16(1)
	SemanticFactsSchemaID    = "agentsql.semantic-facts"
	SemanticFactsVersion     = uint16(1)
)

// BinderMode is selected before the request transaction starts. A proof may
// never be reused after switching modes.
type BinderMode string

const (
	BinderModeCatalogClosedV1 BinderMode = "CATALOG_CLOSED_V1"
	BinderModeNativeCV1       BinderMode = "NATIVE_C_V1"
)

func (mode BinderMode) valid() bool {
	return mode == BinderModeCatalogClosedV1 || mode == BinderModeNativeCV1
}

type BinderStatementClass string

const (
	BinderStatementSelect BinderStatementClass = "SELECT"
	BinderStatementInsert BinderStatementClass = "INSERT"
	BinderStatementUpdate BinderStatementClass = "UPDATE"
	BinderStatementDelete BinderStatementClass = "DELETE"
)

type SemanticUsage string

const (
	SemanticUsageOutput    SemanticUsage = "output"
	SemanticUsageReference SemanticUsage = "reference"
)

type SemanticRelation struct {
	DatasourceID       string
	DatabaseOID        uint32
	RelationOID        uint32
	NamespaceOID       uint32
	Schema             string
	Name               string
	Kind               byte
	Persistence        byte
	CatalogFingerprint string
	BindingAlias       string
	ViewPath           string
}

type SemanticColumnUse struct {
	RelationOID  uint32
	Attnum       int16
	Name         string
	TypeOID      uint32
	TypeModifier int32
	CollationOID uint32
	Usage        SemanticUsage
	Site         string
	OutputIndex  int
	BindingAlias string
	ViewPath     string
	WholeRow     bool
	SystemColumn bool
}

type SemanticWriteTarget struct {
	RelationOID  uint32
	Attnum       int16
	Name         string
	TypeOID      uint32
	TypeModifier int32
	CollationOID uint32
	Kind         b5dml.WriteTargetKind
	Source       b5dml.WriteSource
}

type SemanticViewExpansion struct {
	ViewOID     uint32
	RelationOID uint32
	Depth       int
	Path        string
}

type SemanticObjectKind string

const (
	SemanticObjectTrigger     SemanticObjectKind = "trigger"
	SemanticObjectRule        SemanticObjectKind = "rule"
	SemanticObjectRLS         SemanticObjectKind = "rls"
	SemanticObjectInheritance SemanticObjectKind = "inheritance"
	SemanticObjectPartition   SemanticObjectKind = "partition"
	SemanticObjectForeignKey  SemanticObjectKind = "foreign_key"
	SemanticObjectCheck       SemanticObjectKind = "check"
	SemanticObjectDefault     SemanticObjectKind = "default"
	SemanticObjectGenerated   SemanticObjectKind = "generated"
	SemanticObjectIdentity    SemanticObjectKind = "identity"
	SemanticObjectIndex       SemanticObjectKind = "index"
	SemanticObjectDependency  SemanticObjectKind = "dependency"
)

type SemanticObjectUse struct {
	Kind       SemanticObjectKind
	OwnerOID   uint32
	ObjectOID  uint32
	RelatedOID uint32
}

// SemanticIdentity binds facts to the execution identity. PlanGeneration is
// zero when a mode cannot supply a PostgreSQL invalidation generation; the
// capability precision declaration makes that absence explicit. Native-only
// generation evidence also belongs in BinderProof.EngineEvidenceDigest.
type SemanticIdentity struct {
	DatasourceIdentity string
	DatabaseOID        uint32
	SessionUser        string
	CurrentUser        string
	RoleOID            uint32
	FixedSearchPath    string
	SearchPathDigest   string
	CatalogDigest      string
	PlanGeneration     uint64
}

// SemanticFacts is the sole mode-neutral input to B2/B5 adapters. Mode-private
// analyzed trees, plans and prepared statement handles are deliberately absent.
type SemanticFacts struct {
	Schema          string
	SchemaVersion   uint16
	StatementClass  BinderStatementClass
	Action          b5dml.Action
	Relations       []SemanticRelation
	ColumnUses      []SemanticColumnUse
	WriteTargets    []SemanticWriteTarget
	ViewExpansions  []SemanticViewExpansion
	ObjectUses      []SemanticObjectUse
	HasWholeRow     bool
	HasSystemColumn bool
	Identity        SemanticIdentity
}

type PrecisionDeclaration struct {
	Name  string
	Exact bool
	Note  string
}

type CapabilityAttestation struct {
	Schema                  string
	SchemaVersion           uint16
	Mode                    BinderMode
	Available               bool
	ServerVersionNum        int
	ServerMajor             int
	DatabaseOID             uint32
	ABI                     string
	ExtensionVersion        string
	BuildHash               string
	ExtensionHash           string
	NodeManifestHash        string
	AllowlistHash           string
	GrammarManifestHash     string
	CatalogQueryPackHash    string
	CanonicalEncoderVersion string
	BuiltinManifestHash     string
	Capabilities            []string
	Precision               []PrecisionDeclaration
	Digest                  string
}

type RelationLockExpectation struct {
	RelationOID uint32
	Mode        string
}

type BindRequest struct {
	RawSQL   string
	Identity SemanticIdentity
}

type BoundProgram struct {
	Mode                 BinderMode
	Facts                SemanticFacts
	SemanticFactsDigest  string
	EngineEvidenceDigest string
	Capability           CapabilityAttestation
	LockExpectation      []RelationLockExpectation
	CatalogRoots         []uint32
	ExecutionHandle      string
}

type PreSeal struct {
	Program          BoundProgram
	CatalogPreDigest string
	ActualLockDigest string
	IdentityDigest   string
}

type BinderProof struct {
	Schema               string
	SchemaVersion        uint16
	Mode                 BinderMode
	StatementClass       BinderStatementClass
	SemanticFactsDigest  string
	EngineEvidenceDigest string
	CapabilityDigest     string
	CatalogPreDigest     string
	CatalogPostDigest    string
	ActualLockDigest     string
	IdentityDigest       string
	AttestationDigest    string
}

// Binder freezes the shared S1 lifecycle. Implementations retain any raw
// execution capability privately; callers receive only an opaque handle.
type Binder interface {
	Probe(context.Context, SemanticIdentity) (CapabilityAttestation, error)
	Bind(context.Context, BindRequest) (BoundProgram, error)
	VerifyPre(context.Context, BoundProgram, PostgresCatalogFrame, []uint32) (PreSeal, error)
	VerifyPost(context.Context, PreSeal, PostgresCatalogFrame, []uint32) (BinderProof, error)
}

type BinderFailureClass string

const (
	BinderFailureCapability BinderFailureClass = "capability_insufficient"
	BinderFailurePrecision  BinderFailureClass = "precision_gap"
	BinderFailureCatalog    BinderFailureClass = "catalog_inconsistent"
	BinderFailureLock       BinderFailureClass = "lock_failure"
	BinderFailurePlanDrift  BinderFailureClass = "plan_drift"
	BinderFailureIdentity   BinderFailureClass = "identity_drift"
	BinderFailureDivergence BinderFailureClass = "mode_divergence"
)

const (
	BinderCodeModeRequired    = "AUTH_BINDER_MODE_REQUIRED"
	BinderCodeModeUnsupported = "AUTH_BINDER_MODE_UNSUPPORTED"
	BinderCodeDivergence      = "AUTH_BINDER_DIVERGENCE"
	BinderCodeLockFailed      = "AUTH_BIND_LOCK_FAILED"
	BinderCodeIdentityDrift   = "AUTH_BIND_IDENTITY_DRIFT"
)

// BinderError carries only a stable code and class. Database text, SQL and
// object names are never retained.
type BinderError struct {
	Class BinderFailureClass
	Code  string
}

func (err *BinderError) Error() string {
	if err == nil || err.Code == "" {
		return "AUTH_BINDER_INCOMPLETE"
	}
	return err.Code
}

func (err *BinderError) AuthorizationReason() string { return err.Error() }

func binderFailure(class BinderFailureClass, code string) error {
	return &BinderError{Class: class, Code: code}
}

func NewCapabilityFailure(code string) error { return binderFailure(BinderFailureCapability, code) }
func NewPrecisionFailure(code string) error  { return binderFailure(BinderFailurePrecision, code) }
func NewCatalogFailure(code string) error    { return binderFailure(BinderFailureCatalog, code) }
func NewLockFailure() error                  { return binderFailure(BinderFailureLock, BinderCodeLockFailed) }
func NewPlanDriftFailure() error {
	return binderFailure(BinderFailurePlanDrift, "AUTH_PREPARED_INVALIDATED")
}
func NewIdentityDriftFailure() error {
	return binderFailure(BinderFailureIdentity, BinderCodeIdentityDrift)
}

func (facts SemanticFacts) Digest() (string, error) {
	if facts.Schema == "" {
		facts.Schema = SemanticFactsSchemaID
	}
	if facts.SchemaVersion == 0 {
		facts.SchemaVersion = SemanticFactsVersion
	}
	if facts.Schema != SemanticFactsSchemaID || facts.SchemaVersion != SemanticFactsVersion || facts.StatementClass == "" {
		return "", binderFailure(BinderFailureCapability, BinderCodeModeUnsupported)
	}
	h := sha256.New()
	writeEDString(h, facts.Schema)
	writeEDUint(h, uint64(facts.SchemaVersion))
	writeEDString(h, string(facts.StatementClass))
	writeEDString(h, facts.Action.String())
	writeEDBool(h, facts.HasWholeRow)
	writeEDBool(h, facts.HasSystemColumn)
	writeSemanticIdentity(h, facts.Identity)

	relations := append([]SemanticRelation(nil), facts.Relations...)
	sort.Slice(relations, func(i, j int) bool { return semanticRelationKey(relations[i]) < semanticRelationKey(relations[j]) })
	for _, value := range relations {
		writeEDString(h, semanticRelationKey(value))
	}
	columns := append([]SemanticColumnUse(nil), facts.ColumnUses...)
	sort.Slice(columns, func(i, j int) bool { return semanticColumnKey(columns[i]) < semanticColumnKey(columns[j]) })
	for _, value := range columns {
		writeEDString(h, semanticColumnKey(value))
	}
	writes := append([]SemanticWriteTarget(nil), facts.WriteTargets...)
	sort.Slice(writes, func(i, j int) bool { return semanticWriteKey(writes[i]) < semanticWriteKey(writes[j]) })
	for _, value := range writes {
		writeEDString(h, semanticWriteKey(value))
	}
	views := append([]SemanticViewExpansion(nil), facts.ViewExpansions...)
	sort.Slice(views, func(i, j int) bool { return semanticViewKey(views[i]) < semanticViewKey(views[j]) })
	for _, value := range views {
		writeEDString(h, semanticViewKey(value))
	}
	objects := append([]SemanticObjectUse(nil), facts.ObjectUses...)
	sort.Slice(objects, func(i, j int) bool { return semanticObjectKey(objects[i]) < semanticObjectKey(objects[j]) })
	for _, value := range objects {
		writeEDString(h, semanticObjectKey(value))
	}
	return SemanticFactsSchemaID + ":" + hex.EncodeToString(h.Sum(nil)), nil
}

func CompareSemanticFacts(left, right SemanticFacts) error {
	a, err := left.Digest()
	if err != nil {
		return err
	}
	b, err := right.Digest()
	if err != nil {
		return err
	}
	if a != b {
		return binderFailure(BinderFailureDivergence, BinderCodeDivergence)
	}
	return nil
}

func (attestation CapabilityAttestation) CanonicalDigest() (string, error) {
	if !attestation.Mode.valid() || attestation.SchemaVersion != BinderProofSchemaVersion || attestation.Schema != BinderProofSchemaID {
		return "", binderFailure(BinderFailureCapability, BinderCodeModeUnsupported)
	}
	h := sha256.New()
	values := []string{attestation.Schema, string(attestation.Mode), attestation.ABI, attestation.ExtensionVersion,
		attestation.BuildHash, attestation.ExtensionHash, attestation.NodeManifestHash, attestation.AllowlistHash,
		attestation.GrammarManifestHash, attestation.CatalogQueryPackHash, attestation.CanonicalEncoderVersion,
		attestation.BuiltinManifestHash}
	for _, value := range values {
		writeEDString(h, value)
	}
	writeEDUint(h, uint64(attestation.SchemaVersion))
	writeEDBool(h, attestation.Available)
	writeEDUint(h, uint64(attestation.ServerVersionNum))
	writeEDUint(h, uint64(attestation.ServerMajor))
	writeEDUint(h, uint64(attestation.DatabaseOID))
	capabilities := append([]string(nil), attestation.Capabilities...)
	sort.Strings(capabilities)
	for _, value := range capabilities {
		writeEDString(h, value)
	}
	precision := append([]PrecisionDeclaration(nil), attestation.Precision...)
	sort.Slice(precision, func(i, j int) bool { return precision[i].Name < precision[j].Name })
	for _, value := range precision {
		writeEDString(h, value.Name)
		writeEDBool(h, value.Exact)
		writeEDString(h, value.Note)
	}
	return BinderProofSchemaID + ":capability:" + hex.EncodeToString(h.Sum(nil)), nil
}

func NewFinalBinderProof(pre PreSeal, post PostgresCatalogFrame, actualLocks []uint32) (BinderProof, error) {
	if !pre.Program.Mode.valid() {
		return BinderProof{}, binderFailure(BinderFailureCapability, BinderCodeModeUnsupported)
	}
	factsDigest, err := pre.Program.Facts.Digest()
	if err != nil {
		return BinderProof{}, err
	}
	if factsDigest != pre.Program.SemanticFactsDigest || post.Fingerprint == "" || post.Fingerprint != pre.CatalogPreDigest {
		return BinderProof{}, binderFailure(BinderFailureCatalog, "AUTH_CATALOG_RACE")
	}
	locks := digestOIDs(actualLocks)
	if locks != pre.ActualLockDigest {
		return BinderProof{}, NewLockFailure()
	}
	capabilityDigest, err := pre.Program.Capability.CanonicalDigest()
	if err != nil || capabilityDigest != pre.Program.Capability.Digest {
		return BinderProof{}, binderFailure(BinderFailureCapability, "AUTH_BINDER_CAPABILITY_MISMATCH")
	}
	proof := BinderProof{Schema: BinderProofSchemaID, SchemaVersion: BinderProofSchemaVersion,
		Mode: pre.Program.Mode, StatementClass: pre.Program.Facts.StatementClass,
		SemanticFactsDigest: factsDigest, EngineEvidenceDigest: pre.Program.EngineEvidenceDigest,
		CapabilityDigest: capabilityDigest, CatalogPreDigest: pre.CatalogPreDigest,
		CatalogPostDigest: post.Fingerprint, ActualLockDigest: locks, IdentityDigest: pre.IdentityDigest}
	h := sha256.New()
	for _, value := range []string{proof.Schema, string(proof.Mode), string(proof.StatementClass), proof.SemanticFactsDigest,
		proof.EngineEvidenceDigest, proof.CapabilityDigest, proof.CatalogPreDigest, proof.CatalogPostDigest,
		proof.ActualLockDigest, proof.IdentityDigest} {
		writeEDString(h, value)
	}
	writeEDUint(h, uint64(proof.SchemaVersion))
	proof.AttestationDigest = BinderProofSchemaID + ":final:" + hex.EncodeToString(h.Sum(nil))
	return proof, nil
}

// AuthorizeB2 consumes only common facts and delegates the frozen seven-stage
// decision to columnauth.Authorize.
func AuthorizeB2(facts SemanticFacts, input columnauth.Input) (columnauth.ProtectionPlan, error) {
	digest, err := facts.Digest()
	if err != nil {
		return columnauth.ProtectionPlan{}, err
	}
	if facts.StatementClass != BinderStatementSelect {
		return columnauth.ProtectionPlan{}, NewCapabilityFailure("AUTH_STATEMENT_CLASS_DENIED")
	}
	relations := make(map[uint32]columnauth.RelationIdentity, len(facts.Relations))
	input.Relations = input.Relations[:0]
	for _, relation := range facts.Relations {
		identity := columnauth.RelationIdentity{DatabaseID: strconv.FormatUint(uint64(relation.DatabaseOID), 10),
			StableObjectID: postgresStableObjectID(relation.DatabaseOID, relation.RelationOID), Schema: relation.Schema,
			Name: relation.Name, CatalogFingerprint: relation.CatalogFingerprint, BindingAlias: relation.BindingAlias,
			ViewPath: relation.ViewPath}
		relations[relation.RelationOID] = identity
		input.Relations = append(input.Relations, identity)
	}
	input.Uses = input.Uses[:0]
	for _, use := range facts.ColumnUses {
		relation, ok := relations[use.RelationOID]
		if !ok || use.WholeRow || use.SystemColumn || use.Attnum <= 0 {
			return columnauth.ProtectionPlan{}, NewPrecisionFailure("AUTH_COLUMN_SHAPE_UNSUPPORTED")
		}
		input.Uses = append(input.Uses, columnauth.BoundColumnUse{Column: columnauth.ColumnIdentity{Relation: relation,
			Ordinal: int(use.Attnum), Name: use.Name, TypeDigest: postgresColumnTypeDigest(use.TypeOID, use.TypeModifier, use.CollationOID)},
			Usage: columnauth.Usage(use.Usage), Site: use.Site, OutputIndex: use.OutputIndex,
			BindingAlias: use.BindingAlias, ViewPath: use.ViewPath})
	}
	input.BinderDigest = digest
	input.CatalogDigest = facts.Identity.CatalogDigest
	input.CatalogConsistent = input.CatalogConsistent && facts.Identity.CatalogDigest != ""
	return columnauth.Authorize(input), nil
}

// AuthorizeB5 maps common facts into the existing independent grant lattice.
// It does not synthesize grants, attestations or a table-level fallback.
func AuthorizeB5(facts SemanticFacts, input b5dml.AuthorizationInput) (b5dml.AuthorizationDecision, error) {
	digest, err := facts.Digest()
	if err != nil {
		return b5dml.AuthorizationDecision{}, err
	}
	if facts.StatementClass == BinderStatementSelect || facts.Action == b5dml.ActionUnknown {
		return b5dml.AuthorizationDecision{}, NewCapabilityFailure("DML_ACTION_UNSUPPORTED")
	}
	relations := make(map[uint32]b5dml.RelationIdentity, len(facts.Relations))
	for _, relation := range facts.Relations {
		relations[relation.RelationOID] = b5dml.RelationIdentity{DatasourceID: relation.DatasourceID,
			DatabaseOID: relation.DatabaseOID, RelationOID: relation.RelationOID, RelationKind: relation.Kind,
			Schema: relation.Schema, Name: relation.Name, CatalogFingerprint: relation.CatalogFingerprint}
	}
	input.Action = facts.Action
	input.Target = b5dml.RelationIdentity{}
	input.Writes = input.Writes[:0]
	input.References = input.References[:0]
	for _, write := range facts.WriteTargets {
		relation, ok := relations[write.RelationOID]
		if !ok {
			return b5dml.AuthorizationDecision{}, NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
		}
		if input.Target.RelationOID == 0 {
			input.Target = relation
		}
		if write.Kind == b5dml.WriteTargetRow {
			input.Writes = append(input.Writes, b5dml.RowDelete(relation))
			continue
		}
		if write.Attnum <= 0 || write.Name == "" || write.TypeOID == 0 {
			return b5dml.AuthorizationDecision{}, NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
		}
		column := b5dml.ColumnIdentity{Relation: relation, Attnum: write.Attnum, Name: write.Name,
			TypeOID: write.TypeOID, TypeModifier: write.TypeModifier, CollationOID: write.CollationOID}
		input.Writes = append(input.Writes, b5dml.ColumnWrite(column, write.Source))
	}
	for _, use := range facts.ColumnUses {
		if use.Usage != SemanticUsageReference {
			continue
		}
		relation, ok := relations[use.RelationOID]
		if !ok {
			return b5dml.AuthorizationDecision{}, NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
		}
		column := b5dml.ColumnIdentity{Relation: relation, Attnum: use.Attnum, Name: use.Name, TypeOID: use.TypeOID,
			TypeModifier: use.TypeModifier, CollationOID: use.CollationOID}
		input.References = append(input.References, b5dml.ColumnReference(column, b5dml.ReferenceExpression))
	}
	input.CatalogSnapshotDigest = facts.Identity.CatalogDigest
	input.PlanDigest = digest
	input.CatalogConsistent = input.CatalogConsistent && facts.Identity.CatalogDigest != ""
	return b5dml.Authorize(input), nil
}

type LegacyFallbackRequest struct {
	BeforeRequestClassification bool
	LegacyTableProfileRequested bool
	ControlProofComplete        bool
	ActiveColumnPolicyCount     int
	DML                         bool
}

// LegacyFallbackAllowed is intentionally unavailable after entering B2 and is
// never available to B5.
func LegacyFallbackAllowed(request LegacyFallbackRequest) bool {
	return request.BeforeRequestClassification && request.LegacyTableProfileRequested &&
		request.ControlProofComplete && request.ActiveColumnPolicyCount == 0 && !request.DML
}

func postgresStableObjectID(databaseOID, relationOID uint32) string {
	return fmt.Sprintf("pg:%d:%d", databaseOID, relationOID)
}

func postgresColumnTypeDigest(typeOID uint32, typmod int32, collationOID uint32) string {
	return fmt.Sprintf("pg:type=%d;typmod=%d;collation=%d", typeOID, typmod, collationOID)
}

func semanticRelationKey(v SemanticRelation) string {
	return strings.Join([]string{v.DatasourceID, strconv.FormatUint(uint64(v.DatabaseOID), 10), strconv.FormatUint(uint64(v.RelationOID), 10), strconv.FormatUint(uint64(v.NamespaceOID), 10), v.Schema, v.Name, string([]byte{v.Kind, v.Persistence}), v.CatalogFingerprint, v.BindingAlias, v.ViewPath}, "\x00")
}
func semanticColumnKey(v SemanticColumnUse) string {
	return strings.Join([]string{strconv.FormatUint(uint64(v.RelationOID), 10), strconv.Itoa(int(v.Attnum)), v.Name, strconv.FormatUint(uint64(v.TypeOID), 10), strconv.FormatInt(int64(v.TypeModifier), 10), strconv.FormatUint(uint64(v.CollationOID), 10), string(v.Usage), v.Site, strconv.Itoa(v.OutputIndex), v.BindingAlias, v.ViewPath, strconv.FormatBool(v.WholeRow), strconv.FormatBool(v.SystemColumn)}, "\x00")
}
func semanticWriteKey(v SemanticWriteTarget) string {
	return fmt.Sprintf("%d\x00%d\x00%s\x00%d\x00%d\x00%d\x00%d\x00%d", v.RelationOID, v.Attnum,
		v.Name, v.TypeOID, v.TypeModifier, v.CollationOID, v.Kind, v.Source)
}
func semanticViewKey(v SemanticViewExpansion) string {
	return fmt.Sprintf("%d\x00%d\x00%d\x00%s", v.ViewOID, v.RelationOID, v.Depth, v.Path)
}
func semanticObjectKey(v SemanticObjectUse) string {
	return fmt.Sprintf("%s\x00%d\x00%d\x00%d", v.Kind, v.OwnerOID, v.ObjectOID, v.RelatedOID)
}

func writeSemanticIdentity(h interface{ Write([]byte) (int, error) }, value SemanticIdentity) {
	for _, item := range []string{value.DatasourceIdentity, value.SessionUser, value.CurrentUser, value.FixedSearchPath, value.SearchPathDigest, value.CatalogDigest} {
		writeEDString(h, item)
	}
	writeEDUint(h, uint64(value.DatabaseOID))
	writeEDUint(h, uint64(value.RoleOID))
	writeEDUint(h, value.PlanGeneration)
}

func digestOIDs(values []uint32) string {
	values = uniqueSortedOIDs(values)
	h := sha256.New()
	for _, value := range values {
		writeEDUint(h, uint64(value))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func writeEDString(w interface{ Write([]byte) (int, error) }, value string) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(len(value)))
	_, _ = w.Write(b[:])
	_, _ = w.Write([]byte(value))
}
func writeEDUint(w interface{ Write([]byte) (int, error) }, value uint64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], value)
	_, _ = w.Write(b[:])
}
func writeEDBool(w interface{ Write([]byte) (int, error) }, value bool) {
	if value {
		_, _ = w.Write([]byte{1})
	} else {
		_, _ = w.Write([]byte{0})
	}
}
