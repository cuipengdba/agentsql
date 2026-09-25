package b5dml

import "testing"

func TestStatementSemantics(t *testing.T) {
	t.Parallel()
	relation := testRelation("target", 100)
	id := testColumn(relation, 1, "id")
	value := testColumn(relation, 2, "value")
	source := testColumn(testRelation("source", 200), 1, "id")

	tests := []struct {
		name       string
		facts      StatementFacts
		reason     StatementReason
		writeKinds []WriteTargetKind
		sources    []WriteSource
		refs       int
	}{
		{name: "insert-explicit-and-omitted-null", facts: StatementFacts{Dialect: DialectPostgreSQL, Action: ActionInsert, Shape: ShapeSimple, Target: relation,
			InsertColumns: []InsertColumnFact{{Column: id, Assignment: AssignmentExplicitValue}, {Column: value, Assignment: AssignmentOmitted}}},
			reason: StatementAllowed, writeKinds: []WriteTargetKind{WriteTargetColumn, WriteTargetColumn}, sources: []WriteSource{WriteSourceExplicit, WriteSourceImplicitNull}},
		{name: "insert-omitted-default", facts: insertFact(relation, value, AssignmentOmitted, func(f *InsertColumnFact) { f.HasDefault = true }), reason: StatementDefaultUnsupported},
		{name: "insert-explicit-default", facts: insertFact(relation, value, AssignmentExplicitDefault, nil), reason: StatementDefaultUnsupported},
		{name: "insert-generated", facts: insertFact(relation, value, AssignmentOmitted, func(f *InsertColumnFact) { f.Generated = true }), reason: StatementGeneratedUnsupported},
		{name: "insert-identity", facts: insertFact(relation, value, AssignmentOmitted, func(f *InsertColumnFact) { f.Identity = true }), reason: StatementIdentityUnsupported},
		{name: "insert-sequence", facts: insertFact(relation, value, AssignmentOmitted, func(f *InsertColumnFact) { f.UsesSequence = true }), reason: StatementSequenceUnsupported},
		{name: "delete-row-target", facts: StatementFacts{Dialect: DialectPostgreSQL, Action: ActionDelete, Shape: ShapeSimple, Target: relation,
			References: []Reference{ColumnReference(id, ReferenceWhere)}}, reason: StatementAllowed, writeKinds: []WriteTargetKind{WriteTargetRow}, refs: 1},
		{name: "update-from-split-then-reject", facts: StatementFacts{Dialect: DialectPostgreSQL, Action: ActionUpdate, Shape: ShapeUpdateFrom, Target: relation,
			Writes: []WriteTarget{ColumnWrite(value, WriteSourceExplicit)}, References: []Reference{ColumnReference(source, ReferenceJoin), ColumnReference(id, ReferenceWhere)}},
			reason: StatementComplexShapeUnsupported, writeKinds: []WriteTargetKind{WriteTargetColumn}, refs: 2},
		{name: "delete-using-split-then-reject", facts: StatementFacts{Dialect: DialectPostgreSQL, Action: ActionDelete, Shape: ShapeDeleteUsing, Target: relation,
			References: []Reference{ColumnReference(source, ReferenceUsing), ColumnReference(id, ReferenceWhere)}},
			reason: StatementComplexShapeUnsupported, writeKinds: []WriteTargetKind{WriteTargetRow}, refs: 2},
		{name: "returning", facts: StatementFacts{Dialect: DialectPostgreSQL, Action: ActionDelete, Shape: ShapeSimple, Target: relation, Returning: true}, reason: StatementReturningUnsupported},
		{name: "mysql", facts: StatementFacts{Dialect: DialectMySQL, Action: ActionDelete, Shape: ShapeSimple, Target: relation}, reason: StatementDialectUnsupported},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision := AnalyzeStatement(test.facts)
			if decision.Reason != test.reason || decision.Allowed != (test.reason == StatementAllowed) {
				t.Fatalf("decision = %+v", decision)
			}
			if len(decision.Writes) != len(test.writeKinds) {
				t.Fatalf("writes = %+v", decision.Writes)
			}
			for index, kind := range test.writeKinds {
				if decision.Writes[index].Kind != kind {
					t.Fatalf("write[%d].kind = %v", index, decision.Writes[index].Kind)
				}
			}
			for index, source := range test.sources {
				if decision.Writes[index].Source != source {
					t.Fatalf("write[%d].source = %v", index, decision.Writes[index].Source)
				}
			}
			if len(decision.References) != test.refs {
				t.Fatalf("references = %d", len(decision.References))
			}
		})
	}
}

func insertFact(relation RelationIdentity, column ColumnIdentity, assignment AssignmentKind, mutate func(*InsertColumnFact)) StatementFacts {
	fact := InsertColumnFact{Column: column, Assignment: assignment}
	if mutate != nil {
		mutate(&fact)
	}
	return StatementFacts{Dialect: DialectPostgreSQL, Action: ActionInsert, Shape: ShapeSimple,
		Target: relation, InsertColumns: []InsertColumnFact{fact}}
}
