package b5dml

type StatementShape uint8

const (
	ShapeUnknown StatementShape = iota
	ShapeSimple
	ShapeUpdateFrom
	ShapeDeleteUsing
	ShapeInsertSelect
	ShapeUpsert
	ShapeMerge
	ShapeCTE
	ShapeWritableCTE
	ShapeDMLSubquery
)

type AssignmentKind uint8

const (
	AssignmentUnknown AssignmentKind = iota
	AssignmentExplicitValue
	AssignmentExplicitDefault
	AssignmentOmitted
)

// InsertColumnFact is one live target-table user column. The binder must emit
// every column, including omitted columns, so authorization cannot confuse an
// omitted implicit NULL with "not written".
type InsertColumnFact struct {
	Column       ColumnIdentity
	Assignment   AssignmentKind
	HasDefault   bool
	Identity     bool
	Generated    bool
	UsesSequence bool
}

type StatementFacts struct {
	Dialect       Dialect
	Action        Action
	Shape         StatementShape
	Target        RelationIdentity
	Writes        []WriteTarget
	References    []Reference
	InsertColumns []InsertColumnFact
	Returning     bool
}

type StatementReason string

const (
	StatementAllowed                 StatementReason = "DML_STATEMENT_ALLOWED"
	StatementDialectUnsupported      StatementReason = "DML_DIALECT_UNSUPPORTED"
	StatementActionUnsupported       StatementReason = "DML_ACTION_UNSUPPORTED"
	StatementReturningUnsupported    StatementReason = "DML_RETURNING_UNSUPPORTED"
	StatementComplexShapeUnsupported StatementReason = "DML_COMPLEX_SHAPE_UNSUPPORTED"
	StatementIdentityIncomplete      StatementReason = "DML_STATEMENT_IDENTITY_INCOMPLETE"
	StatementDefaultUnsupported      StatementReason = "DML_DEFAULT_UNSUPPORTED"
	StatementGeneratedUnsupported    StatementReason = "DML_GENERATED_UNSUPPORTED"
	StatementIdentityUnsupported     StatementReason = "DML_IDENTITY_COLUMN_UNSUPPORTED"
	StatementSequenceUnsupported     StatementReason = "DML_SEQUENCE_UNSUPPORTED"
	StatementWriteSetInvalid         StatementReason = "DML_WRITE_SET_INVALID"
)

type StatementDecision struct {
	Allowed    bool
	Reason     StatementReason
	Writes     []WriteTarget
	References []Reference
}

// AnalyzeStatement freezes statement meaning but does not parse SQL. Its
// input is the future binder's typed output. For UPDATE FROM and DELETE USING,
// the split is retained in the returned decision for audit/golden tests even
// though the v0.4 shape is rejected before authorization or execution.
func AnalyzeStatement(facts StatementFacts) StatementDecision {
	decision := StatementDecision{
		Reason:     StatementAllowed,
		Writes:     append([]WriteTarget(nil), facts.Writes...),
		References: append([]Reference(nil), facts.References...),
	}
	deny := func(reason StatementReason) StatementDecision {
		decision.Allowed = false
		decision.Reason = reason
		return decision
	}
	if facts.Dialect != DialectPostgreSQL {
		return deny(StatementDialectUnsupported)
	}
	if !validAction(facts.Action) {
		return deny(StatementActionUnsupported)
	}
	if !facts.Target.complete() {
		return deny(StatementIdentityIncomplete)
	}
	if facts.Returning {
		return deny(StatementReturningUnsupported)
	}

	switch facts.Action {
	case ActionInsert:
		writes, reason := insertWriteSet(facts)
		decision.Writes = writes
		if reason != StatementAllowed {
			return deny(reason)
		}
	case ActionUpdate:
		if !validColumnWriteSet(facts.Target, facts.Writes) {
			return deny(StatementWriteSetInvalid)
		}
	case ActionDelete:
		// DELETE writes one row object at the target relation. It does not
		// require a fabricated grant for every table column.
		decision.Writes = []WriteTarget{RowDelete(facts.Target)}
	}

	if facts.Shape != ShapeSimple {
		return deny(StatementComplexShapeUnsupported)
	}
	decision.Allowed = true
	return decision
}

func insertWriteSet(facts StatementFacts) ([]WriteTarget, StatementReason) {
	if len(facts.InsertColumns) == 0 {
		return nil, StatementWriteSetInvalid
	}
	writes := make([]WriteTarget, 0, len(facts.InsertColumns))
	seen := make(map[int16]struct{}, len(facts.InsertColumns))
	for _, fact := range facts.InsertColumns {
		if !fact.Column.completeUserColumn() || fact.Column.Relation != facts.Target {
			return writes, StatementIdentityIncomplete
		}
		if _, duplicate := seen[fact.Column.Attnum]; duplicate {
			return writes, StatementWriteSetInvalid
		}
		seen[fact.Column.Attnum] = struct{}{}
		switch {
		case fact.Generated:
			return writes, StatementGeneratedUnsupported
		case fact.Identity:
			return writes, StatementIdentityUnsupported
		case fact.UsesSequence:
			return writes, StatementSequenceUnsupported
		case fact.Assignment == AssignmentExplicitDefault:
			return writes, StatementDefaultUnsupported
		case fact.Assignment == AssignmentOmitted && fact.HasDefault:
			return writes, StatementDefaultUnsupported
		case fact.Assignment == AssignmentExplicitValue:
			writes = append(writes, ColumnWrite(fact.Column, WriteSourceExplicit))
		case fact.Assignment == AssignmentOmitted:
			// PostgreSQL physically inserts NULL for a plain omitted column.
			// It remains an actual write target and needs a write grant.
			writes = append(writes, ColumnWrite(fact.Column, WriteSourceImplicitNull))
		default:
			return writes, StatementWriteSetInvalid
		}
	}
	return writes, StatementAllowed
}

func validColumnWriteSet(target RelationIdentity, writes []WriteTarget) bool {
	if len(writes) == 0 {
		return false
	}
	seen := make(map[int16]struct{}, len(writes))
	for _, write := range writes {
		if write.Kind != WriteTargetColumn || !write.Column.completeUserColumn() ||
			write.Relation != target || write.Column.Relation != target ||
			write.Source != WriteSourceExplicit {
			return false
		}
		if _, duplicate := seen[write.Column.Attnum]; duplicate {
			return false
		}
		seen[write.Column.Attnum] = struct{}{}
	}
	return true
}
