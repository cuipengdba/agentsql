package b5dml

type ClosureKind uint8

const (
	ClosureUnknown ClosureKind = iota
	ClosureTriggerWriteOutsideManifest
	ClosureTrigger
	ClosureForeignKeyRead
	ClosureForeignKeyCascadeWrite
	ClosureCheckConstraintRead
	ClosureUniqueConstraintRead
	ClosureExclusionConstraintRead
	ClosureRule
	ClosureView
	ClosurePartitionRouting
	ClosurePartitionChild
	ClosureUserDefinedFunction
	ClosureRowLevelSecurity
	ClosureDefaultExpression
	ClosureIdentitySequence
	ClosureGeneratedExpression
	ClosureExpressionIndex
	ClosurePartialIndexPredicate
	ClosureDomainOrCoercion
)

type ClosureFinding struct {
	Kind       ClosureKind
	Owner      RelationIdentity
	Dependency RelationIdentity
	InManifest bool
}

type ClosureReason string

const (
	ClosureAllowed             ClosureReason = "DML_CLOSURE_ALLOWED"
	ClosureDialectUnsupported  ClosureReason = "DML_DIALECT_UNSUPPORTED"
	ClosureComplexDML          ClosureReason = "DML_COMPLEX_SHAPE_UNSUPPORTED"
	ClosureImplicitUnsupported ClosureReason = "DML_IMPLICIT_OBJECT_UNSUPPORTED"
	ClosureManifestIncomplete  ClosureReason = "DML_CLOSURE_MANIFEST_INCOMPLETE"
)

type ClosureDecision struct {
	Allowed bool
	Reason  ClosureReason
}

// CheckClosure is deliberately a negative v0.4 contract. An empty finding set
// for a simple PostgreSQL statement may proceed to authorization. Every known
// implicit object is rejected until S5b supplies a major-specific closed ABI.
// MySQL is unconditionally unsupported and must acquire no business connection.
func CheckClosure(dialect Dialect, shape StatementShape, findings []ClosureFinding) ClosureDecision {
	if dialect != DialectPostgreSQL {
		return ClosureDecision{Reason: ClosureDialectUnsupported}
	}
	if shape != ShapeSimple {
		return ClosureDecision{Reason: ClosureComplexDML}
	}
	for _, finding := range findings {
		if finding.Kind == ClosureTriggerWriteOutsideManifest || !finding.InManifest {
			return ClosureDecision{Reason: ClosureManifestIncomplete}
		}
		// Even a dependency included in a diagnostic manifest remains denied;
		// presence is not closure authorization.
		return ClosureDecision{Reason: ClosureImplicitUnsupported}
	}
	return ClosureDecision{Allowed: true, Reason: ClosureAllowed}
}

// FrozenClosureNegativeEntries is the reviewed PostgreSQL row count. The same
// rows are exercised for MySQL, yielding FrozenClosureDialectCells cells.
const (
	FrozenClosureNegativeEntries = 27
	FrozenClosureDialectCells    = FrozenClosureNegativeEntries * 2
)
