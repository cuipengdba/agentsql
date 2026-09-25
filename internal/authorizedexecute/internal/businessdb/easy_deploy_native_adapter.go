package businessdb

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"

	"github.com/cuipengdba/agentsql/internal/b5dml"
)

// NativeSelectSemanticFacts adapts the existing C analyzed-tree ABI and
// catalog frame into the same facts consumed by the closed mode authorizers.
func NativeSelectSemanticFacts(datasourceID string, session SemanticIdentity, manifest PostgresPreparedManifest, frame PostgresCatalogFrame) (SemanticFacts, error) {
	if datasourceID == "" || manifest.CommandType != "SELECT" || manifest.AnalyzedDigest == "" ||
		manifest.DependencyDigest == "" || manifest.RoleOID == 0 || manifest.RoleName == "" ||
		manifest.Invalidated || manifest.ReplanCount != 0 || manifest.PlanGeneration == 0 ||
		frame.Fingerprint == "" || frame.DatabaseOID == 0 {
		return SemanticFacts{}, NewCatalogFailure("AUTH_BINDER_INCOMPLETE")
	}
	identity, err := nativeSemanticIdentity(session, manifest, frame)
	if err != nil {
		return SemanticFacts{}, err
	}
	facts := SemanticFacts{Schema: SemanticFactsSchemaID, SchemaVersion: SemanticFactsVersion,
		StatementClass: BinderStatementSelect, Identity: identity}
	boundByOID := make(map[uint32]PostgresBoundRelation, len(manifest.Relations))
	for _, relation := range manifest.Relations {
		if _, exists := boundByOID[relation.OID]; !exists {
			boundByOID[relation.OID] = relation
		}
	}
	for _, relation := range frame.Relations {
		bound, ok := boundByOID[relation.OID]
		if !ok {
			return SemanticFacts{}, NewCatalogFailure("AUTH_BINDER_INCOMPLETE")
		}
		facts.Relations = append(facts.Relations, SemanticRelation{DatasourceID: datasourceID,
			DatabaseOID: relation.DatabaseOID, RelationOID: relation.OID, NamespaceOID: relation.NamespaceOID,
			Schema: relation.Schema, Name: relation.Name, Kind: relation.Kind, Persistence: relation.Persistence,
			CatalogFingerprint: frame.Fingerprint, BindingAlias: bound.Path, ViewPath: nativeViewPath(bound)})
	}
	columns := make(map[string]PostgresColumnIdentity, len(frame.Columns))
	for _, column := range frame.Columns {
		columns[closedColumnKey(column.RelationOID, column.Attnum)] = column
	}
	outputCount := 0
	for _, use := range manifest.Columns {
		if use.Usage == string(SemanticUsageOutput) && use.QueryDepth == 0 && use.ContributorGroup > outputCount {
			outputCount = use.ContributorGroup
		}
	}
	for _, use := range manifest.Columns {
		column, ok := columns[closedColumnKey(use.RelationOID, use.Attnum)]
		if !ok {
			return SemanticFacts{}, NewCatalogFailure("AUTH_BINDER_INCOMPLETE")
		}
		bound := boundByOID[use.RelationOID]
		outputIndex := -1
		usage := SemanticUsage(use.Usage)
		site := use.Site
		if usage == SemanticUsageOutput && use.QueryDepth > 0 {
			// A nested SELECT target contributes to the outer predicate, not to
			// the client-visible result. Normalize the native walker into the
			// common B2 reference semantics used by the closed resolver.
			usage = SemanticUsageReference
			site = "subquery"
		}
		if usage == SemanticUsageOutput && use.ContributorGroup > 0 && use.ContributorGroup <= outputCount {
			outputIndex = use.ContributorGroup - 1
		}
		semantic := SemanticColumnUse{RelationOID: use.RelationOID, Attnum: use.Attnum, Name: column.Name,
			TypeOID: column.TypeOID, TypeModifier: column.Typmod, CollationOID: column.Collation,
			Usage: usage, Site: site, OutputIndex: outputIndex,
			BindingAlias: bound.Path, ViewPath: nativeViewPath(bound), WholeRow: use.WholeRow,
			SystemColumn: use.Attnum < 0}
		facts.ColumnUses = append(facts.ColumnUses, semantic)
		facts.HasWholeRow = facts.HasWholeRow || semantic.WholeRow || semantic.Attnum == 0
		facts.HasSystemColumn = facts.HasSystemColumn || semantic.SystemColumn
	}
	for _, object := range manifest.Objects {
		facts.ObjectUses = append(facts.ObjectUses, SemanticObjectUse{Kind: SemanticObjectKind(object.Kind), ObjectOID: object.OID})
	}
	return facts, nil
}

// NativeDMLSemanticFacts reuses postgres_dml_binder's already-reviewed
// StatementFacts rather than deriving write/reference sets a second time.
func NativeDMLSemanticFacts(datasourceID string, session SemanticIdentity, enrollment PostgresDMLEnrollment) (SemanticFacts, error) {
	manifest, frame := enrollment.Manifest, enrollment.Catalog
	if datasourceID == "" || manifest.PlanGeneration == 0 || manifest.Invalidated || manifest.ReplanCount != 0 ||
		frame.Fingerprint == "" || !validNativeDMLAction(enrollment.Facts.Action) {
		return SemanticFacts{}, NewCatalogFailure("AUTH_BINDER_INCOMPLETE")
	}
	identity, err := nativeSemanticIdentity(session, manifest.PostgresPreparedManifest, frame)
	if err != nil {
		return SemanticFacts{}, err
	}
	// SemanticFacts carries a request-local lifecycle generation shared by both
	// modes. The extension's private invalidation generation remains bound in
	// NativeBoundProgram.EngineEvidenceDigest and must not create a false
	// cross-mode semantic divergence after seal_prepared increments it.
	identity.PlanGeneration = 1
	facts := SemanticFacts{Schema: SemanticFactsSchemaID, SchemaVersion: SemanticFactsVersion,
		StatementClass: binderStatementClassForDML(enrollment.Facts.Action), Action: enrollment.Facts.Action, Identity: identity}
	for _, relation := range frame.Relations {
		facts.Relations = append(facts.Relations, SemanticRelation{DatasourceID: datasourceID,
			DatabaseOID: relation.DatabaseOID, RelationOID: relation.OID, NamespaceOID: relation.NamespaceOID,
			Schema: relation.Schema, Name: relation.Name, Kind: relation.Kind, Persistence: relation.Persistence,
			CatalogFingerprint: frame.Fingerprint})
	}
	for _, write := range enrollment.Facts.Writes {
		value := SemanticWriteTarget{RelationOID: write.Relation.RelationOID, Kind: write.Kind, Source: write.Source}
		if write.Kind == b5dml.WriteTargetColumn {
			value.Attnum, value.Name, value.TypeOID, value.TypeModifier, value.CollationOID = write.Column.Attnum,
				write.Column.Name, write.Column.TypeOID, write.Column.TypeModifier, write.Column.CollationOID
		}
		facts.WriteTargets = append(facts.WriteTargets, value)
	}
	for _, reference := range enrollment.Facts.References {
		semantic := SemanticColumnUse{RelationOID: reference.Relation.RelationOID, Attnum: reference.Column.Attnum,
			Name: reference.Column.Name, TypeOID: reference.Column.TypeOID, TypeModifier: reference.Column.TypeModifier,
			CollationOID: reference.Column.CollationOID, Usage: SemanticUsageReference,
			Site: nativeReferenceSite(reference.Site), OutputIndex: -1,
			WholeRow:     reference.Kind == b5dml.ReferenceWholeRow || reference.Column.Attnum == 0,
			SystemColumn: reference.Kind == b5dml.ReferenceSystemColumn || reference.Column.Attnum < 0}
		facts.ColumnUses = append(facts.ColumnUses, semantic)
		facts.HasWholeRow = facts.HasWholeRow || semantic.WholeRow
		facts.HasSystemColumn = facts.HasSystemColumn || semantic.SystemColumn
	}
	return facts, nil
}

func NativeBoundProgram(facts SemanticFacts, manifest PostgresPreparedManifest, frame PostgresCatalogFrame) (BoundProgram, error) {
	factsDigest, err := facts.Digest()
	if err != nil {
		return BoundProgram{}, err
	}
	capability, err := nativeCapabilityAttestation(manifest.Capability, frame.DatabaseOID, frame.ServerVersion)
	if err != nil {
		return BoundProgram{}, err
	}
	h := sha256.New()
	writeEDString(h, manifest.AnalyzedDigest)
	writeEDString(h, manifest.DependencyDigest)
	writeEDUint(h, manifest.PlanGeneration)
	writeEDUint(h, manifest.ReplanCount)
	writeEDBool(h, manifest.Invalidated)
	oids := manifestRelationOIDs(manifest)
	return BoundProgram{Mode: BinderModeNativeCV1, Facts: facts, SemanticFactsDigest: factsDigest,
		EngineEvidenceDigest: "native-analyzed:" + hex.EncodeToString(h.Sum(nil)), Capability: capability,
		LockExpectation: makeLockExpectations(oids), CatalogRoots: oids, ExecutionHandle: manifest.StatementName}, nil
}

func nativeCapabilityAttestation(capability PostgresBinderCapability, databaseOID uint32, serverVersion int) (CapabilityAttestation, error) {
	if capability.ABI != postgresBinderABI || capability.ServerMajor < 14 || capability.ServerMajor > 18 ||
		capability.ExtensionHash == "" || capability.NodeManifestHash == "" || capability.AllowlistHash == "" ||
		databaseOID == 0 || serverVersion/10000 != capability.ServerMajor {
		return CapabilityAttestation{}, NewCapabilityFailure("AUTH_BINDER_CAPABILITY_MISMATCH")
	}
	value := CapabilityAttestation{Schema: BinderProofSchemaID, SchemaVersion: BinderProofSchemaVersion,
		Mode: BinderModeNativeCV1, Available: true, ServerVersionNum: serverVersion,
		ServerMajor: capability.ServerMajor, DatabaseOID: databaseOID, ABI: capability.ABI,
		ExtensionVersion: capability.ExtensionVersion, BuildHash: capability.BuildHash,
		ExtensionHash: capability.ExtensionHash, NodeManifestHash: capability.NodeManifestHash,
		AllowlistHash: capability.AllowlistHash,
		Capabilities:  []string{"analyzed_tree", "exact_expression_oids", "ordinary_view_lineage", "prepared_generation"},
		Precision:     []PrecisionDeclaration{{Name: "semantic_facts", Exact: true}, {Name: "plan_generation", Exact: true}}}
	value.Digest, _ = value.CanonicalDigest()
	return value, nil
}

func nativeSemanticIdentity(session SemanticIdentity, manifest PostgresPreparedManifest, frame PostgresCatalogFrame) (SemanticIdentity, error) {
	if session.DatabaseOID != 0 && session.DatabaseOID != frame.DatabaseOID ||
		session.RoleOID != 0 && session.RoleOID != manifest.RoleOID ||
		session.CurrentUser != "" && session.CurrentUser != manifest.RoleName ||
		session.FixedSearchPath != "" && session.FixedSearchPath != manifest.SearchPath {
		return SemanticIdentity{}, NewIdentityDriftFailure()
	}
	value := session
	value.DatabaseOID, value.RoleOID, value.CurrentUser = frame.DatabaseOID, manifest.RoleOID, manifest.RoleName
	if value.SessionUser == "" {
		value.SessionUser = manifest.RoleName
	}
	value.FixedSearchPath = manifest.SearchPath
	path := sha256.Sum256([]byte(value.FixedSearchPath))
	value.SearchPathDigest = hex.EncodeToString(path[:])
	value.CatalogDigest, value.PlanGeneration = frame.Fingerprint, manifest.PlanGeneration
	return value, nil
}

func nativeViewPath(value PostgresBoundRelation) string {
	if value.ViewDepth > 0 {
		return value.Path
	}
	return ""
}
func validNativeDMLAction(value b5dml.Action) bool {
	return value == b5dml.ActionInsert || value == b5dml.ActionUpdate || value == b5dml.ActionDelete
}
func binderStatementClassForDML(value b5dml.Action) BinderStatementClass {
	switch value {
	case b5dml.ActionInsert:
		return BinderStatementInsert
	case b5dml.ActionUpdate:
		return BinderStatementUpdate
	case b5dml.ActionDelete:
		return BinderStatementDelete
	}
	return ""
}
func nativeReferenceSite(value b5dml.ReferenceSite) string {
	switch value {
	case b5dml.ReferenceWhere:
		return "where"
	case b5dml.ReferenceJoin:
		return "join"
	case b5dml.ReferenceUsing:
		return "using"
	case b5dml.ReferenceExpression:
		return "expression"
	case b5dml.ReferenceSubquery:
		return "subquery"
	case b5dml.ReferenceForeignKey:
		return "foreign_key"
	case b5dml.ReferenceConstraint:
		return "constraint"
	case b5dml.ReferenceInternalRead:
		return "internal_read"
	case b5dml.ReferenceConflictCheck:
		return "conflict"
	case b5dml.ReferenceReturning:
		return "returning"
	}
	return "unknown:" + strconv.Itoa(int(value))
}
